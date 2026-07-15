package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"rolltop/backend/blob"
	"rolltop/backend/store"
)

func TestGenericBlobCleanupFilesystemFailureSurvivesRestartAndRunnerRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "rolltop.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "blob-cleanup-recovery@example.test", "Blob Cleanup Recovery", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("users", fmt.Sprintf("%d", user.ID), "blobs", "failure.eml")
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abs, "blocks-remove"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: rel, SHA256: "failure-sha", Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db, Blobs: blob.New(root)}
	deleted, err := service.deleteUnreferencedBlob(ctx, user.ID, blobRecord.ID, rel)
	if err == nil || deleted {
		t.Fatalf("filesystem failure deleted=%v err=%v, want queued failure", deleted, err)
	}
	entries, err := db.ListBlobCleanupQueueForUser(ctx, user.ID, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed filesystem cleanup queue=%+v err=%v", entries, err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blobRecord.ID); err != nil {
		t.Fatalf("failed filesystem cleanup removed metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(abs); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service = &Service{Store: db, Blobs: blob.New(root)}
	runner := NewRunner(service)
	if err := runner.RecoverPendingBlobCleanups(); err != nil {
		t.Fatal(err)
	}
	entries, err = db.ListBlobCleanupQueueForUser(ctx, user.ID, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("startup recovery queue=%+v err=%v", entries, err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blobRecord.ID); err == nil {
		t.Fatal("startup recovery retained blob metadata")
	}
}

func TestGenericBlobCleanupDeletesFileAndMetadataNormally(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "blob-cleanup-normal@example.test", "Blob Cleanup Normal", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("users", fmt.Sprintf("%d", user.ID), "blobs", "normal.eml")
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: rel, SHA256: "normal-sha", Size: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db, Blobs: blob.New(root)}
	deleted, err := service.deleteUnreferencedBlob(ctx, user.ID, blobRecord.ID, rel)
	if err != nil || !deleted {
		t.Fatalf("normal cleanup deleted=%v err=%v", deleted, err)
	}
	if _, err := os.Stat(abs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("normal cleanup file stat error=%v, want not exist", err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blobRecord.ID); err == nil {
		t.Fatal("normal cleanup retained blob metadata")
	}
}

func TestGenericBlobCleanupWithoutBlobStoreDefersSafely(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "blob-cleanup-deferred@example.test", "Blob Cleanup Deferred", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: "users/legacy-test/message.eml", SHA256: "deferred-sha", Size: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db}
	deleted, err := service.deleteUnreferencedBlob(ctx, user.ID, blobRecord.ID, blobRecord.Path)
	if err != nil || deleted {
		t.Fatalf("deferred cleanup deleted=%v err=%v, want false/nil", deleted, err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blobRecord.ID); err != nil {
		t.Fatalf("deferred cleanup removed blob metadata: %v", err)
	}
	entries, err := db.ListBlobCleanupQueueForUser(ctx, user.ID, 10)
	if err != nil || len(entries) != 1 || entries[0].BlobID != blobRecord.ID {
		t.Fatalf("deferred cleanup queue=%+v err=%v", entries, err)
	}
}
