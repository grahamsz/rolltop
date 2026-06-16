package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestGmailMessageGetRequiresUserOwnedMessage(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first := createTestMessage(t, ctx, db, "first@example.test", "First message", 1)
	second := createTestMessage(t, ctx, db, "second@example.test", "Second message", 2)

	if _, err := gmailMessageGet(ctx, db, first.UserID, messageID(first.ID), "metadata"); err != nil {
		t.Fatalf("own message lookup failed: %v", err)
	}
	if _, err := gmailMessageGet(ctx, db, first.UserID, messageID(second.ID), "metadata"); !store.IsNotFound(err) {
		t.Fatalf("cross-user message lookup err = %v, want not found", err)
	}
}

func TestListMessagesRequiresUserOwnedMailbox(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first := createTestMessage(t, ctx, db, "first@example.test", "First message", 1)
	second := createTestMessage(t, ctx, db, "second@example.test", "Second message", 2)

	messages, _, err := listMessages(ctx, db, first.UserID, listArgs{LabelIDs: []string{gmailLabelID(first.MailboxID)}}, false)
	if err != nil {
		t.Fatalf("own mailbox list failed: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != first.ID {
		t.Fatalf("own mailbox messages = %+v, want only %d", messages, first.ID)
	}
	if _, _, err := listMessages(ctx, db, first.UserID, listArgs{LabelIDs: []string{gmailLabelID(second.MailboxID)}}, false); !store.IsNotFound(err) {
		t.Fatalf("cross-user mailbox list err = %v, want not found", err)
	}
}

func TestBearerUserIDRequiresIssuedAccessToken(t *testing.T) {
	p := &gmailMCPPlugin{access: map[string]oauthToken{}}
	req := httptest.NewRequest("POST", "/api/plugins/gmail_mcp/mcp", nil)
	req.Header.Set("Authorization", "Bearer missing")
	if _, ok := p.bearerUserID(req); ok {
		t.Fatal("missing token authenticated")
	}

	p.access[codeHash("valid-token")] = oauthToken{UserID: 42, ClientID: "test", Scope: "gmail.readonly", ExpiresAt: time.Now().Add(time.Minute)}
	req.Header.Set("Authorization", "Bearer valid-token")
	userID, ok := p.bearerUserID(req)
	if !ok || userID != 42 {
		t.Fatalf("valid token userID=%d ok=%t, want 42 true", userID, ok)
	}
}

func createTestMessage(t *testing.T, ctx context.Context, db *store.Store, email, subject string, uid uint32) store.MessageRecord {
	t.Helper()
	user, err := db.CreateUser(ctx, email, email, "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID:            user.ID,
		Email:             email,
		Host:              "imap.example.test",
		Port:              993,
		Username:          email,
		EncryptedPassword: "secret",
		UseTLS:            true,
		Mailbox:           store.DefaultMailboxPattern,
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID,
		Kind:   "message",
		Path:   filepath.Join("accounts", "1", "mailboxes", "INBOX", subject+".eml"),
		SHA256: subject,
		Size:   64,
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:          user.ID,
		AccountID:       account.ID,
		MailboxID:       mailbox.ID,
		BlobID:          blob.ID,
		MessageIDHeader: "<" + subject + "@example.test>",
		Subject:         subject,
		FromAddr:        email,
		ToAddr:          "recipient@example.test",
		Date:            time.Now(),
		InternalDate:    time.Now(),
		UID:             uid,
		Size:            64,
		BlobPath:        blob.Path,
		BodyText:        "body for " + subject,
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}
