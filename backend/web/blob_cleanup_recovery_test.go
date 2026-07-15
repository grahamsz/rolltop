package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"rolltop/backend/blob"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestServerStartupRecoversPendingBlobCleanup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "startup-blob-cleanup@example.test", "Startup Blob Cleanup", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("users", fmt.Sprintf("%d", user.ID), "blobs", "startup.eml")
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("message"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: rel, SHA256: "startup-sha", Size: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, queued, err := db.QueueBlobCleanupIfUnreferenced(ctx, user.ID, blobRecord.ID); err != nil || !queued {
		t.Fatalf("queue cleanup queued=%v err=%v", queued, err)
	}

	service := &syncer.Service{Store: db, Blobs: blob.New(root)}
	server, err := New(Options{Store: db, Syncer: service, PluginDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := os.Stat(abs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup cleanup file stat error=%v, want not exist", err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blobRecord.ID); err == nil {
		t.Fatal("startup cleanup retained blob metadata")
	}
	entries, err := db.ListBlobCleanupQueueForUser(ctx, user.ID, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("startup cleanup queue=%+v err=%v", entries, err)
	}
}
