package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"rolltop/backend/store"
)

func TestBulkReadMessagesIsUserScoped(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "read-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "read-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerMessage := createNotificationTestMessage(t, ctx, db, owner, 301, "Sender", "Owner message")
	otherMessage := createNotificationTestMessage(t, ctx, db, other, 401, "Sender", "Other message")
	server := &Server{store: db, masterKey: []byte("12345678901234567890123456789012")}

	payload, err := json.Marshal(map[string]any{
		"ids":  []int64{ownerMessage.ID, ownerMessage.ID, otherMessage.ID},
		"read": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/messages/bulk-read", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: owner}))
	csrfBase := "bulk-read-csrf"
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrfBase})
	req.Header.Set("X-CSRF-Token", server.csrfForBase(csrfBase))
	res := httptest.NewRecorder()
	server.apiBulkReadMessages(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var response struct {
		Updated int `json:"updated"`
	}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Updated != 1 {
		t.Fatalf("updated = %d, want 1 owned message", response.Updated)
	}
	updatedOwner, err := db.GetMessageForUser(ctx, owner.ID, ownerMessage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updatedOwner.IsRead || !updatedOwner.ReadSyncPending {
		t.Fatalf("owner flags = read:%t pending:%t", updatedOwner.IsRead, updatedOwner.ReadSyncPending)
	}
	unchangedOther, err := db.GetMessageForUser(ctx, other.ID, otherMessage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedOther.IsRead || unchangedOther.ReadSyncPending {
		t.Fatalf("other tenant flags changed = read:%t pending:%t", unchangedOther.IsRead, unchangedOther.ReadSyncPending)
	}
}
