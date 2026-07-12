package web

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestSnoozeReminderWebPushUsesIndependentCursor(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "snooze-notifier@example.test", "Notifier", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	message := createNotificationTestMessage(t, ctx, db, user, 1301, "Alice <alice@example.test>", "Follow up")
	sub := saveNotifierTestSubscription(t, ctx, db, user.ID, "https://push.example.test/snooze-notifier")
	if _, err := db.SnoozeMessage(ctx, user.ID, message.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `UPDATE message_snoozes SET snoozed_until = ? WHERE user_id = ?`, time.Now().Add(-time.Minute).Unix(), user.ID); err != nil {
		t.Fatal(err)
	}
	events, err := db.RecordDueSnoozeReminderEvents(ctx, user.ID, time.Now(), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v err=%v", events, err)
	}
	var delivered webPushNotification
	server := &Server{
		store: db,
		webPushSend: func(_ context.Context, _ *http.Client, _ []byte, got store.WebPushSubscription, payload []byte, _ string) (webPushSendResult, error) {
			if got.ID != sub.ID {
				t.Fatalf("delivered subscription = %+v, want id %d", got, sub.ID)
			}
			if err := json.Unmarshal(payload, &delivered); err != nil {
				t.Fatal(err)
			}
			return webPushSendResult{StatusCode: http.StatusCreated}, nil
		},
	}
	if retry := server.notifySnoozeReminderWebPush(ctx, user.ID); retry {
		t.Fatal("successful reminder push requested retry")
	}
	if delivered.MessageID != message.ID || delivered.URL != webPushMessageURL(message.ID) || delivered.APIURL == "" {
		t.Fatalf("delivered reminder = %+v", delivered)
	}
	subs, err := db.ListWebPushSubscriptions(ctx, user.ID)
	if err != nil || len(subs) != 1 {
		t.Fatalf("subscriptions = %+v err=%v", subs, err)
	}
	if subs[0].LastSnoozeReminderEventID != events[0].ID || subs[0].LastNewMailEventID != sub.LastNewMailEventID {
		t.Fatalf("independent cursors after delivery = %+v", subs[0])
	}
}
