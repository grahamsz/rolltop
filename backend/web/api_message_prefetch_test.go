package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/store"
)

func TestMessagePrefetchWarmsBodyWithoutChangingReadState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "prefetch@example.test", "Prefetch", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "encrypted", Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Join([]string{
		"From: Alice <alice@example.test>",
		"To: " + user.Email,
		"Subject: Warm without opening",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><p><strong>Fully formatted</strong> notification body.</p></body></html>",
	}, "\r\n")
	blobStore := blob.New(dir)
	saved, err := blobStore.SaveRawMessage(user.ID, account.ID, mailbox.Name, 7, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message", Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blobRecord.ID,
		Subject: "Warm without opening", FromAddr: "Alice <alice@example.test>", ToAddr: user.Email,
		Date: time.Now().UTC(), InternalDate: time.Now().UTC(), UID: 7, Size: saved.Size,
		BlobPath: saved.Path, BodyText: "Fully formatted notification body.", IsRead: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, blobs: blobStore}
	other, err := db.CreateUser(ctx, "other-prefetch@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherReq := httptest.NewRequest(http.MethodGet, "/api/messages/"+strconvInt64(message.ID)+"/prefetch", nil)
	otherReq = otherReq.WithContext(context.WithValue(otherReq.Context(), userContextKey, currentUser{User: other}))
	otherRes := httptest.NewRecorder()
	server.apiMessagePrefetch(otherRes, otherReq, message.ID)
	if otherRes.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant prefetch status = %d body=%s", otherRes.Code, otherRes.Body.String())
	}
	untouched, err := db.GetMessageForUser(ctx, user.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.BodyHTML != "" || untouched.IsRead || untouched.ReadSyncPending {
		t.Fatalf("cross-tenant prefetch mutated owner message: %+v", untouched)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/messages/"+strconvInt64(message.ID)+"/prefetch", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	res := httptest.NewRecorder()

	server.apiMessagePrefetch(res, req, message.ID)

	if res.Code != http.StatusOK {
		t.Fatalf("prefetch status = %d body=%s", res.Code, res.Body.String())
	}
	warmed, err := db.GetMessageForUser(ctx, user.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if warmed.IsRead || warmed.ReadSyncPending {
		t.Fatalf("prefetch changed read state: is_read=%t pending=%t", warmed.IsRead, warmed.ReadSyncPending)
	}
	if !strings.Contains(warmed.BodyHTML, "<strong>Fully formatted</strong>") {
		t.Fatalf("prefetch did not persist formatted body: %q", warmed.BodyHTML)
	}
}
