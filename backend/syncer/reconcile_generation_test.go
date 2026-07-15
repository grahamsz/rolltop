package syncer

import (
	"context"
	"testing"

	"rolltop/backend/store"
)

type legacyReconcileTestFetcher struct {
	*moveTestFetcher
	uids  []uint32
	calls int
}

func (f *legacyReconcileTestFetcher) UIDs(context.Context, store.MailAccount, string) ([]uint32, error) {
	f.calls++
	return append([]uint32(nil), f.uids...), nil
}

func TestReconcileMailboxSkipsDeletionAcrossUIDValidityEpoch(t *testing.T) {
	fixture := newMoveTestFixture(t)
	const localUIDValidity = 777
	setReconcileFixtureUIDValidity(t, fixture, localUIDValidity)
	fetcher := &reconcileJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		uidValidity:     localUIDValidity + 1,
		uidNext:         fixture.message.UID + 1,
	}
	fixture.service.Fetcher = fetcher

	if err := fixture.service.reconcileMailboxUIDs(context.Background(), fixture.userID, fixture.account, fixture.source); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); err != nil {
		t.Fatalf("generation mismatch deleted local message: %v", err)
	}
	if got := expungeFingerprintCount(t, fixture); got != 0 {
		t.Fatalf("generation mismatch recorded %d expunge fingerprints", got)
	}
	if fetcher.snapshotCalls != 1 || fetcher.legacyCalls != 0 {
		t.Fatalf("snapshot calls=%d legacy calls=%d, want 1/0", fetcher.snapshotCalls, fetcher.legacyCalls)
	}
}

func TestReconcileMailboxSnapshotPreservesPostSearchCopy(t *testing.T) {
	fixture := newMoveTestFixture(t)
	const uidValidity = 777
	setReconcileFixtureUIDValidity(t, fixture, uidValidity)
	cutoff := fixture.message.UID + 1
	var copied store.MessageRecord
	fetcher := &reconcileJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		uidValidity:     uidValidity,
		uidNext:         cutoff,
	}
	fetcher.afterSnapshot = func() {
		blob, err := fixture.store.CreateBlob(context.Background(), store.BlobRecord{
			UserID: fixture.userID, Kind: "message", Path: "users/reconcile/direct-copy.eml",
			SHA256: "direct-copy", Size: 64,
		})
		if err != nil {
			t.Fatal(err)
		}
		copied, err = fixture.store.CreateMessage(context.Background(), store.CreateMessage{
			UserID: fixture.userID, AccountID: fixture.account.ID, MailboxID: fixture.source.ID,
			BlobID: blob.ID, MessageIDHeader: "<direct-copy@example.test>",
			Subject: "Direct copy after UID search", FromAddr: "sender@example.test",
			Date: fixture.message.Date, InternalDate: fixture.message.InternalDate,
			UID: cutoff, UIDValidity: uidValidity, Size: 64, BlobPath: blob.Path,
			BodyText: "copied after the remote snapshot", IsRead: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture.service.Fetcher = fetcher

	if err := fixture.service.reconcileMailboxUIDs(context.Background(), fixture.userID, fixture.account, fixture.source); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); !store.IsNotFound(err) {
		t.Fatalf("missing pre-snapshot UID survived reconciliation: %v", err)
	}
	if copied.ID == 0 {
		t.Fatal("post-snapshot copy was not inserted")
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, copied.ID); err != nil {
		t.Fatalf("post-snapshot copy was deleted: %v", err)
	}
	var copiedTombstones int
	if err := fixture.store.DB().QueryRow(`SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`,
		fixture.userID, fixture.source.ID, cutoff).Scan(&copiedTombstones); err != nil {
		t.Fatal(err)
	}
	if copiedTombstones != 0 {
		t.Fatalf("post-snapshot copy created %d expunge tombstones", copiedTombstones)
	}
	if got := expungeFingerprintCount(t, fixture); got != 1 {
		t.Fatalf("snapshot reconciliation recorded %d tombstones, want only the older missing UID", got)
	}
}

func TestLegacyReconcileDeletesWithoutExpungeEvidence(t *testing.T) {
	fixture := newMoveTestFixture(t)
	const uidValidity = 777
	setReconcileFixtureUIDValidity(t, fixture, uidValidity)
	fetcher := &legacyReconcileTestFetcher{moveTestFetcher: fixture.fetcher}
	fixture.service.Fetcher = fetcher

	if err := fixture.service.reconcileMailboxUIDs(context.Background(), fixture.userID, fixture.account, fixture.source); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); !store.IsNotFound(err) {
		t.Fatalf("legacy reconciliation left stale local message: %v", err)
	}
	if got := expungeFingerprintCount(t, fixture); got != 0 {
		t.Fatalf("legacy reconciliation recorded %d unsafe expunge fingerprints", got)
	}
	if fetcher.calls != 1 {
		t.Fatalf("legacy UID calls=%d, want 1", fetcher.calls)
	}
}

func setReconcileFixtureUIDValidity(t *testing.T, fixture moveTestFixture, uidValidity uint32) {
	t.Helper()
	ctx := context.Background()
	if err := fixture.store.UpdateMailboxRemoteStatus(ctx, fixture.userID, fixture.source.ID,
		1, 0, fixture.message.UID+1, uidValidity); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().ExecContext(ctx, `UPDATE messages SET uid_validity = ?
		WHERE user_id = ? AND id = ?`, uidValidity, fixture.userID, fixture.message.ID); err != nil {
		t.Fatal(err)
	}
}

func expungeFingerprintCount(t *testing.T, fixture moveTestFixture) int {
	t.Helper()
	var count int
	if err := fixture.store.DB().QueryRow(`SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ?`, fixture.userID, fixture.source.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
