package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestSnoozeAPIAndReminderFeedAreUserScoped(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "snooze-api-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "snooze-api-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerMessage := createNotificationTestMessage(t, ctx, db, owner, 1101, "Alice <alice@example.test>", "Owner reminder")
	otherMessage := createNotificationTestMessage(t, ctx, db, other, 1201, "Secret <secret@example.test>", "Other reminder")
	server := &Server{
		store: db, masterKey: bytes.Repeat([]byte{4}, 32), mailListCache: newMailListCache(),
		events: newEventHub(), snoozeSchedulerWake: make(chan struct{}, 1),
	}

	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	body, _ := json.Marshal(map[string]any{"until": until.Format(time.RFC3339)})
	request := authenticatedSnoozeRequest(t, server, owner, http.MethodPut, "/api/messages/"+strconvInt64(ownerMessage.ID)+"/snooze", body)
	response := httptest.NewRecorder()
	server.apiMessageSnooze(response, request, ownerMessage.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT snooze status=%d body=%s", response.Code, response.Body.String())
	}
	var mutation struct {
		Snoozed bool      `json:"snoozed"`
		Snooze  apiSnooze `json:"snooze"`
	}
	if err := json.NewDecoder(response.Body).Decode(&mutation); err != nil {
		t.Fatal(err)
	}
	if !mutation.Snoozed || mutation.Snooze.MessageID != ownerMessage.ID || mutation.Snooze.SnoozedUntil == "" {
		t.Fatalf("snooze mutation = %+v", mutation)
	}

	ownerList := authenticatedSnoozeRequest(t, server, owner, http.MethodGet, "/api/snoozes?user_id="+strconvInt64(other.ID), nil)
	ownerListResponse := httptest.NewRecorder()
	server.apiSnoozes(ownerListResponse, ownerList)
	if ownerListResponse.Code != http.StatusOK {
		t.Fatalf("owner snoozes status=%d body=%s", ownerListResponse.Code, ownerListResponse.Body.String())
	}
	var ownerPayload struct {
		Conversations []apiConversation `json:"conversations"`
		Snoozes       []apiSnooze       `json:"snoozes"`
	}
	if err := json.NewDecoder(ownerListResponse.Body).Decode(&ownerPayload); err != nil {
		t.Fatal(err)
	}
	if len(ownerPayload.Conversations) != 1 || ownerPayload.Conversations[0].Message.ID != ownerMessage.ID || len(ownerPayload.Snoozes) != 1 {
		t.Fatalf("owner snoozes = %+v", ownerPayload)
	}
	otherList := authenticatedSnoozeRequest(t, server, other, http.MethodGet, "/api/snoozes", nil)
	otherListResponse := httptest.NewRecorder()
	server.apiSnoozes(otherListResponse, otherList)
	if otherListResponse.Code != http.StatusOK || strings.Contains(otherListResponse.Body.String(), "Owner reminder") {
		t.Fatalf("other snoozes status=%d body=%s", otherListResponse.Code, otherListResponse.Body.String())
	}

	if _, err := db.SnoozeMessage(ctx, other.ID, otherMessage.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ownerDB, err := db.UserDB(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherDB, err := db.UserDB(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerDB.ExecContext(ctx, `UPDATE message_snoozes SET snoozed_until = ?, reminded_at = 0 WHERE user_id = ?`, time.Now().Add(-time.Minute).Unix(), owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := otherDB.ExecContext(ctx, `UPDATE message_snoozes SET snoozed_until = ?, reminded_at = 0 WHERE user_id = ?`, time.Now().Add(-time.Minute).Unix(), other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.processDueSnoozes(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	ownerFeed := snoozeReminderRequest(t, server, owner, "/api/notifications/reminders?after=0&user_id="+strconvInt64(other.ID))
	if ownerFeed.UserID != owner.ID || ownerFeed.Count != 1 || len(ownerFeed.Reminders) != 1 || ownerFeed.Reminders[0].MessageID != ownerMessage.ID {
		t.Fatalf("owner reminder feed = %+v", ownerFeed)
	}
	otherFeed := snoozeReminderRequest(t, server, other, "/api/notifications/reminders?after=0")
	if otherFeed.UserID != other.ID || otherFeed.Count != 1 || len(otherFeed.Reminders) != 1 || otherFeed.Reminders[0].MessageID != otherMessage.ID {
		t.Fatalf("other reminder feed = %+v", otherFeed)
	}
}

func authenticatedSnoozeRequest(t *testing.T, server *Server, user store.User, method, target string, body []byte) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, currentUser{User: user}))
	if method != http.MethodGet {
		const session = "snooze-test-session"
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		request.Header.Set("X-CSRF-Token", server.csrfForBase(session))
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func snoozeReminderRequest(t *testing.T, server *Server, user store.User, target string) apiSnoozeReminderNotificationsResponse {
	t.Helper()
	request := authenticatedSnoozeRequest(t, server, user, http.MethodGet, target, nil)
	response := httptest.NewRecorder()
	server.apiSnoozeReminderNotifications(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", target, response.Code, response.Body.String())
	}
	var payload apiSnoozeReminderNotificationsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
