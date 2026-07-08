// File overview: New-mail Web Push notification scheduling and delivery.

package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rolltop/backend/store"
)

type webPushProgress struct {
	RunID       int64
	NewMessages int
}

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

const webPushFreshRunWindow = 2 * time.Minute

func (s *Server) notifyNewMailWebPushAsync(userID int64) {
	if s == nil || s.store == nil || userID <= 0 {
		return
	}
	go s.notifyNewMailWebPush(context.Background(), userID)
}

func (s *Server) notifyNewMailWebPush(ctx context.Context, userID int64) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runs, err := s.store.ListSyncRunsForUser(ctx, userID, 1)
	if err != nil || len(runs) == 0 {
		if err != nil && ctx.Err() == nil {
			log.Printf("web push sync run user_id=%d: %v", userID, err)
		}
		return
	}
	run := runs[0]
	count := run.NewMessages
	if count <= 0 || strings.TrimSpace(run.LatestNewFrom) == "rolltop:maintenance" {
		return
	}
	if !run.UpdatedAt.IsZero() && !s.startedAt.IsZero() && run.UpdatedAt.Before(s.startedAt) {
		s.rememberWebPushProgress(userID, run.ID, count)
		return
	}
	if !run.UpdatedAt.IsZero() && time.Since(run.UpdatedAt) > webPushFreshRunWindow {
		s.rememberWebPushProgress(userID, run.ID, count)
		return
	}
	delta := s.claimWebPushDelta(userID, run.ID, count)
	if delta <= 0 {
		return
	}
	if err := s.sendNewMailWebPush(ctx, userID, delta, run); err != nil && ctx.Err() == nil {
		log.Printf("web push notify user_id=%d: %v", userID, err)
	}
}

func (s *Server) claimWebPushDelta(userID, runID int64, count int) int {
	s.webPushMu.Lock()
	defer s.webPushMu.Unlock()
	if s.webPushSent == nil {
		s.webPushSent = map[int64]webPushProgress{}
	}
	previous := s.webPushSent[userID]
	delta := count
	if previous.RunID == runID {
		delta = count - previous.NewMessages
	}
	if delta <= 0 {
		return 0
	}
	s.webPushSent[userID] = webPushProgress{RunID: runID, NewMessages: count}
	return delta
}

func (s *Server) rememberWebPushProgress(userID, runID int64, count int) {
	s.webPushMu.Lock()
	defer s.webPushMu.Unlock()
	if s.webPushSent == nil {
		s.webPushSent = map[int64]webPushProgress{}
	}
	current := s.webPushSent[userID]
	if current.RunID == runID && current.NewMessages >= count {
		return
	}
	s.webPushSent[userID] = webPushProgress{RunID: runID, NewMessages: count}
}

func (s *Server) sendNewMailWebPush(ctx context.Context, userID int64, count int, run store.SyncRun) error {
	subs, err := s.store.ListWebPushSubscriptions(ctx, userID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	payload, err := json.Marshal(newMailWebPushNotification(count, run))
	if err != nil {
		return err
	}
	user, err := s.store.GetUserByID(ctx, userID)
	subject := ""
	if err == nil {
		subject = user.Email
	}
	client := &http.Client{Timeout: webPushHTTPTimeout}
	for _, sub := range subs {
		result, err := sendWebPush(ctx, client, s.masterKey, sub, payload, subject)
		if err == nil {
			continue
		}
		if result.StatusCode == http.StatusGone || result.StatusCode == http.StatusNotFound {
			if deleteErr := s.store.DeleteWebPushSubscription(ctx, userID, sub.Endpoint); deleteErr != nil {
				log.Printf("web push delete stale subscription user_id=%d host=%s: %v", userID, webPushEndpointHost(sub.Endpoint), deleteErr)
			}
			continue
		}
		if result.StatusCode >= webPushInvalidStatusMin {
			log.Printf("web push delivery user_id=%d host=%s status=%d", userID, webPushEndpointHost(sub.Endpoint), result.StatusCode)
			continue
		}
		log.Printf("web push delivery user_id=%d host=%s: %v", userID, webPushEndpointHost(sub.Endpoint), err)
	}
	return nil
}

func newMailWebPushNotification(count int, run store.SyncRun) webPushNotification {
	sender := displayWebPushSender(run.LatestNewFrom)
	subject := truncateWebPushText(run.LatestNewSubject, 110)
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
	apiURL := ""
	if run.LatestNewMessageID > 0 {
		messageURL = webPushMessageURL(run.LatestNewMessageID)
		apiURL = "/api/messages/" + strconv.FormatInt(run.LatestNewMessageID, 10)
	}
	return webPushNotification{
		Title:     title,
		Body:      body,
		Tag:       "rolltop-new-mail",
		Icon:      "/icon.svg?v=transparent-logo-v2",
		Badge:     "/icon.svg?v=transparent-logo-v2",
		URL:       messageURL,
		APIURL:    apiURL,
		MessageID: run.LatestNewMessageID,
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
