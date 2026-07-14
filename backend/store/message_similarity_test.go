package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMessageSimilarityCandidatesUseInternalDateWithCreationFallback(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox, blob := testMailbox(t, ctx, db)
	now := time.Now().UTC().Truncate(time.Second)

	withInternalDate, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		UID: 1, BlobPath: blob.Path, ThreadKey: "internal", FromAddr: "sender@example.test",
		Date: now.Add(365 * 24 * time.Hour), InternalDate: now.Add(-24 * time.Hour), IsRead: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	withCreationFallback, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		UID: 2, BlobPath: blob.Path, ThreadKey: "fallback", FromAddr: "sender@example.test",
		Date: now.Add(-365 * 24 * time.Hour), IsRead: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleInternalDate, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		UID: 3, BlobPath: blob.Path, ThreadKey: "stale", FromAddr: "sender@example.test",
		Date: now, InternalDate: now.Add(-100 * 24 * time.Hour), IsRead: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	hydrated, err := db.MessageSimilarityCandidatesForUser(ctx, user.ID, []int64{
		withInternalDate.ID, withCreationFallback.ID, staleInternalDate.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hydrated) != 3 {
		t.Fatalf("hydrated candidates = %d, want 3", len(hydrated))
	}
	if !hydrated[0].Date.Equal(withInternalDate.InternalDate) {
		t.Fatalf("internal-date hydration = %v, want %v", hydrated[0].Date, withInternalDate.InternalDate)
	}
	if !hydrated[1].Date.Equal(withCreationFallback.CreatedAt) {
		t.Fatalf("fallback hydration = %v, want creation time %v", hydrated[1].Date, withCreationFallback.CreatedAt)
	}
	if !hydrated[2].Date.Equal(staleInternalDate.InternalDate) {
		t.Fatalf("stale hydration = %v, want internal date %v", hydrated[2].Date, staleInternalDate.InternalDate)
	}

	recent, err := db.RecentReadMessageSimilarityCandidatesForUser(ctx, user.ID, now.Add(-7*24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int64]MessageSimilarityCandidate, len(recent))
	for _, candidate := range recent {
		seen[candidate.ID] = candidate
	}
	if len(seen) != 2 {
		t.Fatalf("recent candidates = %+v, want internal and fallback rows", recent)
	}
	if _, ok := seen[withInternalDate.ID]; !ok {
		t.Fatalf("recent candidates omitted internal-date row %d", withInternalDate.ID)
	}
	if candidate, ok := seen[withCreationFallback.ID]; !ok || !candidate.Date.Equal(withCreationFallback.CreatedAt) {
		t.Fatalf("recent candidates fallback = %+v, want creation time", candidate)
	}
	if _, ok := seen[staleInternalDate.ID]; ok {
		t.Fatalf("sender Date header made stale INTERNALDATE candidate recent: %+v", seen[staleInternalDate.ID])
	}
}
