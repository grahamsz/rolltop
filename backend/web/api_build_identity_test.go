package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/buildinfo"
	"rolltop/backend/store"
)

func TestBuildCommitIncludedInAuthenticatedChromePayloads(t *testing.T) {
	oldCommit := buildinfo.Commit
	buildinfo.Commit = "0123456789abcdef0123456789abcdef01234567"
	t.Cleanup(func() { buildinfo.Commit = oldCommit })

	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(context.Background(), "build@example.test", "Build", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, startedAt: time.Now(), mailListCache: newMailListCache()}
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))

	bootstrap, err := server.bootstrapPayload(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := bootstrap["build_commit"]; got != buildinfo.Commit {
		t.Fatalf("bootstrap build_commit = %#v, want %q", got, buildinfo.Commit)
	}

	chrome, err := server.syncEventPayload(req.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := chrome["build_commit"]; got != buildinfo.Commit {
		t.Fatalf("chrome build_commit = %#v, want %q", got, buildinfo.Commit)
	}
}
