// File overview: Independent Web Push delivery for durable due-snooze events.

package web

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"strconv"
	"time"

	"rolltop/backend/store"
)

func (s *Server) notifySnoozeReminderWebPushAsync(userID int64) {
	if s == nil || s.store == nil || userID <= 0 {
		return
	}
	s.snoozePushScheduleMu.Lock()
	if s.snoozePushRunning == nil {
		s.snoozePushRunning = map[int64]bool{}
	}
	if s.snoozePushDirty == nil {
		s.snoozePushDirty = map[int64]bool{}
	}
	if s.snoozePushRunning[userID] {
		s.snoozePushDirty[userID] = true
		s.snoozePushScheduleMu.Unlock()
		return
	}
	s.snoozePushRunning[userID] = true
	s.snoozePushScheduleMu.Unlock()
	go s.runScheduledSnoozeReminderWebPush(userID)
}

func (s *Server) resumeSnoozeReminderWebPushAsync() {
	if s == nil || s.store == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		users, err := s.store.ListUsers(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("snooze web push resume users: %v", err)
			}
			return
		}
		for _, user := range users {
			s.notifySnoozeReminderWebPushAsync(user.ID)
		}
	}()
}

func (s *Server) runScheduledSnoozeReminderWebPush(userID int64) {
	retryAttempt := 0
	for {
		s.snoozePushScheduleMu.Lock()
		delete(s.snoozePushDirty, userID)
		s.snoozePushScheduleMu.Unlock()

		retryNeeded := s.notifySnoozeReminderWebPush(context.Background(), userID)
		s.snoozePushScheduleMu.Lock()
		if s.snoozePushDirty[userID] {
			delete(s.snoozePushDirty, userID)
			s.snoozePushScheduleMu.Unlock()
			retryAttempt = 0
			continue
		}
		if retryNeeded && retryAttempt < webPushMaxRetryAttempts {
			retryAttempt++
			s.snoozePushScheduleMu.Unlock()
			timer := time.NewTimer(s.newMailWebPushRetryDelay(retryAttempt))
			<-timer.C
			continue
		}
		delete(s.snoozePushRunning, userID)
		s.snoozePushScheduleMu.Unlock()
		return
	}
}

func (s *Server) notifySnoozeReminderWebPush(ctx context.Context, userID int64) bool {
	if s == nil || s.store == nil || userID <= 0 {
		return false
	}
	lock := s.webPushDeliveryLock(userID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	subs, err := s.store.ListWebPushSubscriptions(ctx, userID)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("snooze web push subscriptions user_id=%d: %v", userID, err)
		}
		return true
	}
	if len(subs) == 0 {
		return false
	}
	user, userErr := s.store.GetUserByID(ctx, userID)
	subject := ""
	if userErr == nil {
		subject = user.Email
	}
	client := s.webPushClient
	if client == nil {
		client = newWebPushHTTPClient()
	}
	retryNeeded := false
	for _, sub := range subs {
		if ctx.Err() != nil {
			return true
		}
		events, count, cursor, err := s.store.SnoozeReminderEventsAfter(ctx, userID, sub.LastSnoozeReminderEventID, newMailNotificationEnvelopeLimit)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("snooze web push events user_id=%d subscription_id=%d: %v", userID, sub.ID, err)
			}
			retryNeeded = true
			continue
		}
		if count <= 0 || cursor <= sub.LastSnoozeReminderEventID || len(events) == 0 {
			continue
		}
		payload, err := json.Marshal(snoozeReminderWebPushNotification(count, events[len(events)-1]))
		if err != nil {
			log.Printf("snooze web push payload user_id=%d subscription_id=%d: %v", userID, sub.ID, err)
			continue
		}
		result, err := s.sendWebPushNotification(ctx, client, sub, payload, subject)
		if err == nil {
			advanced, advanceErr := s.store.AdvanceWebPushSubscriptionSnoozeReminderCursor(ctx, userID, sub, cursor)
			if advanceErr != nil {
				if ctx.Err() == nil {
					log.Printf("snooze web push cursor user_id=%d subscription_id=%d: %v", userID, sub.ID, advanceErr)
				}
				retryNeeded = true
			} else if !advanced {
				retryNeeded = true
			}
			continue
		}
		if webPushSubscriptionIsStale(result.StatusCode) {
			deleted, deleteErr := s.store.DeleteWebPushSubscriptionIfCurrent(ctx, userID, sub)
			if deleteErr != nil {
				log.Printf("snooze web push delete stale subscription user_id=%d host=%s: %v", userID, webPushEndpointHost(sub.Endpoint), deleteErr)
				retryNeeded = true
			} else if !deleted {
				retryNeeded = true
			}
			continue
		}
		if result.StatusCode >= webPushInvalidStatusMin {
			log.Printf("snooze web push delivery user_id=%d host=%s status=%d", userID, webPushEndpointHost(sub.Endpoint), result.StatusCode)
			if webPushDeliveryIsRetryable(result.StatusCode) {
				retryNeeded = true
			}
			continue
		}
		log.Printf("snooze web push delivery user_id=%d host=%s: %v", userID, webPushEndpointHost(sub.Endpoint), err)
		retryNeeded = true
	}
	return retryNeeded
}

func snoozeReminderWebPushNotification(count int, latest store.SnoozeReminderEvent) webPushNotification {
	sender := displayWebPushSender(latest.FromAddr)
	title := "rolltop reminder"
	if sender != "" {
		title += " - " + sender
	}
	body := truncateWebPushText(latest.Subject, 110)
	if body == "" {
		body = "A snoozed message is due."
	}
	messageURL := "/mail"
	apiURL := "/api/mail?page=1"
	messageID := int64(0)
	if count == 1 && latest.MessageID > 0 {
		params := url.Values{}
		params.Set("back", "/mail")
		messageURL = "/messages/" + strconv.FormatInt(latest.MessageID, 10) + "?" + params.Encode()
		apiURL = "/api/messages/" + strconv.FormatInt(latest.MessageID, 10) + "/prefetch"
		messageID = latest.MessageID
	} else if count > 1 {
		body = strconv.Itoa(count) + " snoozed messages are due. Latest: " + body
	}
	return webPushNotification{
		Title: title, Body: body, Tag: "rolltop-snooze-reminder",
		Icon: "/icon.svg?v=transparent-logo-v2", Badge: "/icon.svg?v=transparent-logo-v2",
		URL: messageURL, APIURL: apiURL, MessageID: messageID,
	}
}
