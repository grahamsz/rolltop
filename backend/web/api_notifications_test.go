package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestNewMailNotificationsBaselineAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "notify-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "notify-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerFirst := createNotificationTestMessage(t, ctx, db, owner, 101, "Alice <alice@example.test>", "First")
	ownerSecond := createNotificationTestMessage(t, ctx, db, owner, 102, "Bob <bob@example.test>", "Second")
	otherMessage := createNotificationTestMessage(t, ctx, db, other, 201, "Secret <secret@example.test>", "Other tenant")
	for _, msg := range []store.MessageRecord{ownerFirst, ownerSecond, otherMessage} {
		if _, _, err := db.RecordNewMailEvent(ctx, msg.UserID, msg); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{store: db}

	baseline := newMailNotificationRequest(t, server, owner, "/api/notifications/new-mail")
	if baseline.UserID != owner.ID || baseline.Cursor == 0 || baseline.Count != 0 || len(baseline.Messages) != 0 {
		t.Fatalf("baseline = %+v, want a silent owner-scoped cursor", baseline)
	}

	ownerFeed := newMailNotificationRequest(t, server, owner, "/api/notifications/new-mail?after=0&user_id="+strconvInt64(other.ID))
	if ownerFeed.UserID != owner.ID || ownerFeed.Count != 2 || len(ownerFeed.Messages) != 2 {
		t.Fatalf("owner feed = %+v", ownerFeed)
	}
	for _, message := range ownerFeed.Messages {
		if message.MessageID == otherMessage.ID || message.Subject == "Other tenant" {
			t.Fatalf("owner feed exposed another tenant: %+v", ownerFeed)
		}
	}

	otherFeed := newMailNotificationRequest(t, server, other, "/api/notifications/new-mail?after=0")
	if otherFeed.UserID != other.ID || otherFeed.Count != 1 || len(otherFeed.Messages) != 1 || otherFeed.Messages[0].MessageID != otherMessage.ID {
		t.Fatalf("other feed = %+v", otherFeed)
	}
}

func createNotificationTestMessage(t *testing.T, ctx context.Context, db *store.Store, user store.User, uid uint32, from, subject string) store.MessageRecord {
	t.Helper()
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message", Path: fmt.Sprintf("users/%d/notifications-%d.eml", user.ID, uid),
		SHA256: fmt.Sprintf("notifications-%d-%d", user.ID, uid), Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		FromAddr: from, Subject: subject, Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
		UID: uid, BlobPath: blob.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func newMailNotificationRequest(t *testing.T, server *Server, user store.User, target string) apiNewMailNotificationsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	res := httptest.NewRecorder()
	server.apiNewMailNotifications(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", target, res.Code, res.Body.String())
	}
	var payload apiNewMailNotificationsResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func strconvInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
