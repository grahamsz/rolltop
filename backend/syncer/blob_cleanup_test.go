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

func TestGenericBlobCleanupRemovesVirtualRemoteMetadataWithoutDeletingPath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "blob-cleanup-virtual@example.test", "Blob Cleanup Virtual", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	virtualPath := remoteMessagePath(user.ID, 23, "INBOX", 4009, "0123456789abcdef0123456789abcdef")
	virtualAbs := filepath.Join(root, filepath.FromSlash(virtualPath))
	if err := os.MkdirAll(filepath.Dir(virtualAbs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(virtualAbs, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: virtualPath, SHA256: "virtual-sha", Size: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	service := &Service{Store: db, Blobs: blob.New(root)}
	deleted, err := service.deleteUnreferencedBlob(ctx, user.ID, blobRecord.ID, virtualPath)
	if err != nil || !deleted {
		t.Fatalf("virtual cleanup deleted=%v err=%v", deleted, err)
	}
	if body, err := os.ReadFile(virtualAbs); err != nil || string(body) != "sentinel" {
		t.Fatalf("virtual cleanup touched filesystem path body=%q err=%v", body, err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blobRecord.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("virtual cleanup retained blob metadata: %v", err)
	}
	entries, err := db.ListBlobCleanupQueueForUser(ctx, user.ID, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("virtual cleanup queue=%+v err=%v", entries, err)
	}
}

func TestGenericBlobCleanupRejectsUnownedOrMalformedVirtualRemotePaths(t *testing.T) {
	tests := []struct {
		name string
		path func(ownerID, otherID int64) string
	}{
		{
			name: "cross user",
			path: func(_ int64, otherID int64) string {
				return remoteMessagePath(otherID, 23, "INBOX", 4009, "0123456789abcdef0123456789abcdef")
			},
		},
		{
			name: "traversal segment",
			path: func(ownerID, _ int64) string {
				return fmt.Sprintf("remote/users/%d/accounts/23/mailboxes/../uid-4009-0123456789abcdef.eml", ownerID)
			},
		},
		{
			name: "missing message leaf",
			path: func(ownerID, _ int64) string {
				return fmt.Sprintf("remote/users/%d/accounts/23/mailboxes/INBOX", ownerID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			db, err := store.Open(filepath.Join(root, "rolltop.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			owner, err := db.CreateUser(ctx, "blob-cleanup-owner@example.test", "Blob Cleanup Owner", "hash", false)
			if err != nil {
				t.Fatal(err)
			}
			other, err := db.CreateUser(ctx, "blob-cleanup-other@example.test", "Blob Cleanup Other", "hash", false)
			if err != nil {
				t.Fatal(err)
			}
			virtualPath := tt.path(owner.ID, other.ID)
			blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
				UserID: owner.ID, Kind: "message-remote", Path: virtualPath, SHA256: "invalid-virtual-sha", Size: 0,
			})
			if err != nil {
				t.Fatal(err)
			}

			service := &Service{Store: db, Blobs: blob.New(root)}
			deleted, err := service.deleteUnreferencedBlob(ctx, owner.ID, blobRecord.ID, virtualPath)
			if err == nil || deleted {
				t.Fatalf("invalid virtual cleanup deleted=%v err=%v, want false/error", deleted, err)
			}
			if _, err := db.GetBlobForUser(ctx, owner.ID, blobRecord.ID); err != nil {
				t.Fatalf("invalid virtual cleanup removed blob metadata: %v", err)
			}
			entries, err := db.ListBlobCleanupQueueForUser(ctx, owner.ID, 10)
			if err != nil || len(entries) != 1 || entries[0].BlobID != blobRecord.ID {
				t.Fatalf("invalid virtual cleanup queue=%+v err=%v", entries, err)
			}
		})
	}
}
