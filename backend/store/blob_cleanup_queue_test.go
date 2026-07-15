package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestBlobCleanupQueueCompletesNormalDeletion(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "blob-cleanup@example.test", "Blob Cleanup", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	blob := createCleanupTestBlob(t, ctx, db, user.ID, "users/1/normal.eml", "normal-sha", 12)
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `INSERT INTO remote_image_cache
		(user_id, url_hash, url, blob_id, blob_path, status, created_at, updated_at)
		VALUES (?, 'normal', 'https://example.test/normal', ?, ?, 'ready', 1, 1)`,
		user.ID, blob.ID, blob.Path); err != nil {
		t.Fatal(err)
	}
	if _, queued, err := db.QueueBlobCleanupIfUnreferenced(ctx, user.ID, blob.ID); err != nil || queued {
		t.Fatalf("referenced blob queue queued=%v err=%v, want false", queued, err)
	}
	if _, err := userDB.ExecContext(ctx, `DELETE FROM remote_image_cache
		WHERE user_id = ? AND blob_id = ?`, user.ID, blob.ID); err != nil {
		t.Fatal(err)
	}
	entry, queued, err := db.QueueBlobCleanupIfUnreferenced(ctx, user.ID, blob.ID)
	if err != nil || !queued {
		t.Fatalf("queue cleanup queued=%v err=%v", queued, err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blob.ID); err != nil {
		t.Fatalf("queue deleted blob metadata early: %v", err)
	}
	var deletedPath string
	if err := db.CompleteBlobCleanup(ctx, user.ID, entry.ID, func(path string) error {
		deletedPath = path
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if deletedPath != blob.Path {
		t.Fatalf("deleted path=%q, want queued blob path", deletedPath)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blob.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("completed blob metadata lookup error=%v, want no rows", err)
	}
	entries, err := db.ListBlobCleanupQueueForUser(ctx, user.ID, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("completed cleanup queue=%+v err=%v", entries, err)
	}
}

func TestBlobCleanupQueueFailureAndCrashSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "blob-cleanup-retry@example.test", "Blob Cleanup Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	blob := createCleanupTestBlob(t, ctx, db, user.ID, "users/1/retry.eml", "retry-sha", 13)
	entry, queued, err := db.QueueBlobCleanupIfUnreferenced(ctx, user.ID, blob.ID)
	if err != nil || !queued {
		t.Fatalf("queue cleanup queued=%v err=%v", queued, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected filesystem failure")
	if err := db.CompleteBlobCleanup(ctx, user.ID, entry.ID, func(string) error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("completion error=%v, want injected failure", err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blob.ID); err != nil {
		t.Fatalf("filesystem failure removed blob metadata: %v", err)
	}
	entries, err := db.ListBlobCleanupQueueForUser(ctx, user.ID, 10)
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("filesystem failure queue=%+v err=%v", entries, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err = db.ListBlobCleanupQueueForUser(ctx, user.ID, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("reopened cleanup queue=%+v err=%v", entries, err)
	}
	if err := db.CompleteBlobCleanup(ctx, user.ID, entries[0].ID, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, blob.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retried blob metadata lookup error=%v, want no rows", err)
	}
}

func TestBlobCleanupQueueProtectsBlobAndPathReuse(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "blob-cleanup-reuse@example.test", "Blob Cleanup Reuse", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	original := createCleanupTestBlob(t, ctx, db, user.ID, "users/1/reused.eml", "old-sha", 10)
	entry, queued, err := db.QueueBlobCleanupIfUnreferenced(ctx, user.ID, original.ID)
	if err != nil || !queued {
		t.Fatalf("queue cleanup queued=%v err=%v", queued, err)
	}
	reused, err := db.CreateBlob(ctx, BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: original.Path, SHA256: "new-sha", Size: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != original.ID {
		t.Fatalf("path upsert blob id=%d, want original %d", reused.ID, original.ID)
	}
	callbackCalls := 0
	if err := db.CompleteBlobCleanup(ctx, user.ID, entry.ID, func(string) error {
		callbackCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if callbackCalls != 0 {
		t.Fatalf("reused blob version invoked deletion callback %d times", callbackCalls)
	}
	current, err := db.GetBlobForUser(ctx, user.ID, original.ID)
	if err != nil || current.SHA256 != "new-sha" || current.Size != 20 {
		t.Fatalf("reused blob=%+v err=%v", current, err)
	}

	entry, queued, err = db.QueueBlobCleanupIfUnreferenced(ctx, user.ID, current.ID)
	if err != nil || !queued {
		t.Fatalf("requeue cleanup queued=%v err=%v", queued, err)
	}
	if err := db.DeleteBlobForUser(ctx, user.ID, current.ID); err != nil {
		t.Fatal(err)
	}
	newOwner := createCleanupTestBlob(t, ctx, db, user.ID, current.Path, "owner-sha", 30)
	if newOwner.ID == current.ID {
		t.Fatal("path reuse unexpectedly reused deleted blob id")
	}
	if err := db.CompleteBlobCleanup(ctx, user.ID, entry.ID, func(string) error {
		callbackCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if callbackCalls != 0 {
		t.Fatalf("new path owner invoked deletion callback %d times", callbackCalls)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, newOwner.ID); err != nil {
		t.Fatalf("new path owner was deleted: %v", err)
	}
}

func TestBlobCleanupQueueIsTenantIsolatedAndTransactionFailureRetainsState(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "blob-cleanup-owner@example.test", "Blob Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "blob-cleanup-other@example.test", "Blob Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	blob := createCleanupTestBlob(t, ctx, db, owner.ID, "users/1/owner.eml", "owner-sha", 14)
	entry, queued, err := db.QueueBlobCleanupIfUnreferenced(ctx, owner.ID, blob.ID)
	if err != nil || !queued {
		t.Fatalf("queue cleanup queued=%v err=%v", queued, err)
	}
	if err := db.CompleteBlobCleanup(ctx, other.ID, entry.ID, func(string) error {
		t.Fatal("cross-tenant cleanup invoked callback")
		return nil
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant completion error=%v, want not found", err)
	}
	otherEntries, err := db.ListBlobCleanupQueueForUser(ctx, other.ID, 10)
	if err != nil || len(otherEntries) != 0 {
		t.Fatalf("other tenant queue=%+v err=%v", otherEntries, err)
	}
	userDB, err := db.UserDB(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `CREATE TRIGGER fail_blob_cleanup_queue_delete
		BEFORE DELETE ON blob_cleanup_queue
		BEGIN SELECT RAISE(ABORT, 'injected cleanup transaction failure'); END`); err != nil {
		t.Fatal(err)
	}
	callbackCalls := 0
	err = db.CompleteBlobCleanup(ctx, owner.ID, entry.ID, func(string) error {
		callbackCalls++
		return nil
	})
	if err == nil {
		t.Fatal("transaction failure completed cleanup")
	}
	if callbackCalls != 1 {
		t.Fatalf("transaction failure callback calls=%d, want one", callbackCalls)
	}
	if _, err := db.GetBlobForUser(ctx, owner.ID, blob.ID); err != nil {
		t.Fatalf("transaction failure removed blob metadata: %v", err)
	}
	ownerEntries, err := db.ListBlobCleanupQueueForUser(ctx, owner.ID, 10)
	if err != nil || len(ownerEntries) != 1 {
		t.Fatalf("transaction failure queue=%+v err=%v", ownerEntries, err)
	}
	if _, err := userDB.ExecContext(ctx, `DROP TRIGGER fail_blob_cleanup_queue_delete`); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteBlobCleanup(ctx, owner.ID, entry.ID, func(string) error {
		callbackCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if callbackCalls != 2 {
		t.Fatalf("retried callback calls=%d, want two idempotent attempts", callbackCalls)
	}
}

func createCleanupTestBlob(t *testing.T, ctx context.Context, db *Store, userID int64, path, sha string, size int64) BlobRecord {
	t.Helper()
	blob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: userID, Kind: "message-remote", Path: path, SHA256: sha, Size: size,
	})
	if err != nil {
		t.Fatal(err)
	}
	return blob
}
