// File overview: Durable new-mail Web Push notification scheduling and delivery.

package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"rolltop/backend/store"
)

type webPushNotification struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Tag       string `json:"tag"`
	Icon      string `json:"icon"`
	Badge     string `json:"badge"`
	URL       string `json:"url"`
	APIURL    string `json:"api_url,omitempty"`
	MessageID int64  `json:"message_id,omitempty"`
}

const webPushMaxRetryAttempts = 3

func (s *Server) notifyNewMailWebPushAsync(userID int64) {
	if s == nil || s.store == nil || userID <= 0 {
		return
	}
	s.webPushScheduleMu.Lock()
	if s.webPushRunning == nil {
		s.webPushRunning = map[int64]bool{}
	}
	if s.webPushDirty == nil {
		s.webPushDirty = map[int64]bool{}
	}
	if s.webPushRunning[userID] {
		s.webPushDirty[userID] = true
		s.webPushScheduleMu.Unlock()
		return
	}
	s.webPushRunning[userID] = true
	s.webPushScheduleMu.Unlock()
	go s.runScheduledNewMailWebPush(userID)
}

func (s *Server) resumeNewMailWebPushAsync() {
	if s == nil || s.store == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		users, err := s.store.ListUsers(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("web push resume users: %v", err)
			}
			return
		}
		for _, user := range users {
			s.notifyNewMailWebPushAsync(user.ID)
		}
	}()
}

func (s *Server) runScheduledNewMailWebPush(userID int64) {
	retryAttempt := 0
	for {
		s.webPushScheduleMu.Lock()
		delete(s.webPushDirty, userID)
		s.webPushScheduleMu.Unlock()

		retryNeeded := s.notifyNewMailWebPush(context.Background(), userID)
		s.webPushScheduleMu.Lock()
		if s.webPushDirty[userID] {
			delete(s.webPushDirty, userID)
			s.webPushScheduleMu.Unlock()
			retryAttempt = 0
			continue
		}
		if retryNeeded && retryAttempt < webPushMaxRetryAttempts {
			retryAttempt++
			s.webPushScheduleMu.Unlock()
			timer := time.NewTimer(s.newMailWebPushRetryDelay(retryAttempt))
			<-timer.C
			continue
		}
		delete(s.webPushRunning, userID)
		s.webPushScheduleMu.Unlock()
		return
	}
}

func (s *Server) newMailWebPushRetryDelay(attempt int) time.Duration {
	if s.webPushRetryDelay != nil {
		return s.webPushRetryDelay(attempt)
	}
	switch attempt {
	case 1:
		return 2 * time.Second
	case 2:
		return 10 * time.Second
	default:
		return 30 * time.Second
	}
}

func (s *Server) notifyNewMailWebPush(ctx context.Context, userID int64) bool {
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
			log.Printf("web push subscriptions user_id=%d: %v", userID, err)
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
		events, count, cursor, err := s.store.NewMailEventsAfter(ctx, userID, sub.LastNewMailEventID, newMailNotificationEnvelopeLimit)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("web push events user_id=%d subscription_id=%d: %v", userID, sub.ID, err)
			}
			retryNeeded = true
			continue
		}
		if count <= 0 || cursor <= sub.LastNewMailEventID || len(events) == 0 {
			continue
		}
		payload, err := json.Marshal(newMailWebPushNotification(count, events[len(events)-1]))
		if err != nil {
			log.Printf("web push payload user_id=%d subscription_id=%d: %v", userID, sub.ID, err)
			continue
		}
		result, err := s.sendWebPushNotification(ctx, client, sub, payload, subject)
		if err == nil {
			advanced, err := s.store.AdvanceWebPushSubscriptionNewMailCursor(ctx, userID, sub, cursor)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("web push cursor user_id=%d subscription_id=%d: %v", userID, sub.ID, err)
				}
				retryNeeded = true
			} else if !advanced {
				// The endpoint keys rotated while this encrypted delivery was in flight.
				retryNeeded = true
			}
			continue
		}
		if webPushSubscriptionIsStale(result.StatusCode) {
			deleted, deleteErr := s.store.DeleteWebPushSubscriptionIfCurrent(ctx, userID, sub)
			if deleteErr != nil {
				log.Printf("web push delete stale subscription user_id=%d host=%s: %v", userID, webPushEndpointHost(sub.Endpoint), deleteErr)
				retryNeeded = true
			} else if !deleted {
				// A refreshed subscription reused the URL while this request was in flight.
				retryNeeded = true
			}
			continue
		}
		if result.StatusCode >= webPushInvalidStatusMin {
			log.Printf("web push delivery user_id=%d host=%s status=%d", userID, webPushEndpointHost(sub.Endpoint), result.StatusCode)
			if webPushDeliveryIsRetryable(result.StatusCode) {
				retryNeeded = true
			}
			continue
		}
		log.Printf("web push delivery user_id=%d host=%s: %v", userID, webPushEndpointHost(sub.Endpoint), err)
		retryNeeded = true
	}
	return retryNeeded
}

func webPushDeliveryIsRetryable(status int) bool {
	return status == 0 || status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func webPushSubscriptionIsStale(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone
}

func (s *Server) webPushDeliveryLock(userID int64) *sync.Mutex {
	lock, _ := s.webPushDeliveryLocks.LoadOrStore(userID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (s *Server) sendWebPushNotification(ctx context.Context, client *http.Client, sub store.WebPushSubscription, payload []byte, subject string) (webPushSendResult, error) {
	if s.webPushSend != nil {
		return s.webPushSend(ctx, client, s.masterKey, sub, payload, subject)
	}
	return sendWebPush(ctx, client, s.masterKey, sub, payload, subject)
}

func newMailWebPushNotification(count int, latest store.NewMailEvent) webPushNotification {
	sender := displayWebPushSender(latest.FromAddr)
	subject := truncateWebPushText(latest.Subject, 110)
	title := "rolltop"
	if sender != "" {
		title += " - " + sender
	}
	fallback := "1 new message synced."
	if count != 1 {
		fallback = formatWebPushCount(count) + " new messages synced."
	}
	body := fallback
	if subject != "" {
		if count == 1 {
			body = subject
		} else {
			body = formatWebPushCount(count) + " new messages synced. Latest: " + subject
		}
	}
	messageURL := "/mail"
	apiURL := "/api/mail?page=1"
	messageID := int64(0)
	if count == 1 && latest.MessageID > 0 {
		messageURL = webPushMessageURL(latest.MessageID)
		apiURL = "/api/messages/" + strconv.FormatInt(latest.MessageID, 10) + "/prefetch"
		messageID = latest.MessageID
	}
	return webPushNotification{
		Title:     title,
		Body:      body,
		Tag:       "rolltop-new-mail",
		Icon:      "/icon.svg?v=transparent-logo-v2",
		Badge:     "/icon.svg?v=transparent-logo-v2",
		URL:       messageURL,
		APIURL:    apiURL,
		MessageID: messageID,
	}
}

func webPushMessageURL(messageID int64) string {
	if messageID <= 0 {
		return "/mail"
	}
	params := url.Values{}
	params.Set("back", "/mail")
	return "/messages/" + strconv.FormatInt(messageID, 10) + "?" + params.Encode()
}

func displayWebPushSender(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	value := trimmed
	if start := strings.Index(trimmed, "<"); start > 0 && strings.HasSuffix(trimmed, ">") {
		value = strings.TrimSpace(trimmed[:start])
	}
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if !strings.Contains(value, "@") {
		return truncateWebPushText(value, 64)
	}
	parts := strings.SplitN(value, "@", 2)
	return truncateWebPushText(parts[0], 64)
}

func truncateWebPushText(value string, max int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return strings.TrimSpace(value[:max-3]) + "..."
}

func formatWebPushCount(count int) string {
	return strconv.Itoa(count)
}

func webPushEndpointHost(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "unknown"
	}
	return parsed.Host
}
