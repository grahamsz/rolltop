package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"rolltop/backend/store"
)

func TestAPIAccountGETUsesCachedIdentitiesWithoutReconciliation(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	db, err := store.OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := db.CreateUser(ctx, "settings@example.test", "Settings User", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := db.CreateMailIdentityForUser(ctx, user.ID, store.MailIdentity{
		Email:       user.Email,
		DisplayName: user.Name,
		IsPrimary:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, user.ID, store.Contact{
		DisplayName: "Unreconciled Alias",
		IsMe:        true,
		Emails: []store.ContactEmail{{
			Email:     "alias@example.test",
			IsPrimary: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	before, err := db.ListCachedMailIdentitiesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].ID != cached.ID {
		t.Fatalf("cached identities before GET = %+v, want only identity %d", before, cached.ID)
	}

	server := &Server{store: db}
	req := httptest.NewRequest(http.MethodGet, "/api/account", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.apiAccount(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/account status = %d body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Identities []apiMailIdentity `json:"identities"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Identities) != 1 || payload.Identities[0].ID != cached.ID {
		t.Fatalf("GET /api/account identities = %+v, want only cached identity %d", payload.Identities, cached.ID)
	}

	after, err := db.ListCachedMailIdentitiesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != cached.ID {
		t.Fatalf("cached identities after GET = %+v, want database unchanged", after)
	}
}
