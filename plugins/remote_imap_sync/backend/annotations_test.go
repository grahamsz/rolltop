package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestMessageAnnotationsExposeTransferredJournalTimestamp(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	routine := createAnnotationRoutine(t, ctx, fixture, fixture.owner.ID, fixture.inputForOwner("owner-password"))
	copied := createAnnotationMessage(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerAccount.ID, fixture.ownerMailbox.ID, 100)
	untracked := createAnnotationMessage(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerAccount.ID, fixture.ownerMailbox.ID, 101)
	skipped := createAnnotationMessage(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerAccount.ID, fixture.ownerMailbox.ID, 102)
	syncedAt := time.Date(2026, time.July, 14, 18, 42, 31, 0, time.UTC)
	destinationUIDValidity := uint32(501)
	setAnnotationMailboxUIDValidity(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerMailbox.ID, destinationUIDValidity)
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 10, "fingerprint-copied", "marker-copied", copied.UID, "transferred", syncedAt, destinationUIDValidity, annotationMessageSHA(fixture.owner.ID, copied.UID)); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 11, "fingerprint-skipped", "marker-skipped", skipped.UID, "skipped", syncedAt.Add(time.Minute), destinationUIDValidity, annotationMessageSHA(fixture.owner.ID, skipped.UID)); err != nil {
		t.Fatal(err)
	}
	var journalCopiedAt, provenanceSyncedAt int64
	var provenanceSHA string
	if err := fixture.db.QueryRowContext(ctx, `SELECT copied_at FROM plugin_remote_imap_sync_messages
		WHERE user_id = ? AND routine_id = ? AND source_uidvalidity = ? AND source_uid = ?`,
		fixture.owner.ID, routine.ID, 77, 10).Scan(&journalCopiedAt); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRowContext(ctx, `SELECT synced_at, destination_sha256 FROM plugin_remote_imap_sync_provenance
		WHERE user_id = ? AND destination_account_id = ? AND destination_mailbox_id = ?
		AND destination_uidvalidity = ? AND destination_uid = ?`, fixture.owner.ID, fixture.ownerAccount.ID,
		fixture.ownerMailbox.ID, destinationUIDValidity, copied.UID).Scan(&provenanceSyncedAt, &provenanceSHA); err != nil {
		t.Fatal(err)
	}
	if journalCopiedAt != syncedAt.Unix() || provenanceSyncedAt != syncedAt.Unix() {
		t.Fatalf("persisted timestamps = journal %d provenance %d, want %d", journalCopiedAt, provenanceSyncedAt, syncedAt.Unix())
	}
	if want := annotationMessageSHA(fixture.owner.ID, copied.UID); provenanceSHA != want {
		t.Fatalf("provenance SHA-256 = %q, want %q", provenanceSHA, want)
	}

	backend := &remoteIMAPSyncBackend{}
	got, err := backend.MessageAnnotations(ctx, routeAPIHost{st: fixture.store}, fixture.owner.ID,
		[]int64{copied.ID, untracked.ID, skipped.ID, copied.ID, 0, -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[copied.ID]) != 1 {
		t.Fatalf("annotations = %#v, want only transferred message %d", got, copied.ID)
	}
	annotation := got[copied.ID][0]
	if annotation.PluginID != pluginID || annotation.Kind != "remote-imap-sync" || annotation.Label != "Synced by Rolltop" {
		t.Fatalf("annotation identity = %+v", annotation)
	}
	if got := annotation.Metadata["synced_at"]; got != "2026-07-14T18:42:31Z" {
		t.Fatalf("synced_at = %q", got)
	}
	if len(annotation.Metadata) != 1 {
		t.Fatalf("annotation metadata = %#v, want only synced_at", annotation.Metadata)
	}
	if _, ok := got[untracked.ID]; ok {
		t.Fatal("an untracked destination message received a sync annotation")
	}
	if _, ok := got[skipped.ID]; ok {
		t.Fatal("a skipped journal row received a sync annotation")
	}
}

func TestMessageAnnotationsAreTenantScoped(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	ownerRoutine := createAnnotationRoutine(t, ctx, fixture, fixture.owner.ID, fixture.inputForOwner("owner-password"))
	otherInput := fixture.inputForOwner("other-password")
	otherInput.Name = "Other Gmail inbox"
	otherInput.Source.Username = "other@gmail.test"
	otherInput.Destination = destinationInput{AccountID: fixture.otherAccount.ID, MailboxID: fixture.otherMailbox.ID}
	otherRoutine := createAnnotationRoutine(t, ctx, fixture, fixture.other.ID, otherInput)

	ownerMessage := createAnnotationMessage(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerAccount.ID, fixture.ownerMailbox.ID, 210)
	otherMessage := createAnnotationMessage(t, ctx, fixture.store, fixture.other.ID, fixture.otherAccount.ID, fixture.otherMailbox.ID, 220)
	setAnnotationMailboxUIDValidity(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerMailbox.ID, 601)
	setAnnotationMailboxUIDValidity(t, ctx, fixture.store, fixture.other.ID, fixture.otherMailbox.ID, 602)
	if err := recordHandledMessageAt(ctx, fixture.db, ownerRoutine, 77, 20, "fingerprint-owner", "marker-owner", ownerMessage.UID, "transferred", time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC), 601, annotationMessageSHA(fixture.owner.ID, ownerMessage.UID)); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessageAt(ctx, fixture.db, otherRoutine, 77, 30, "fingerprint-other", "marker-other", otherMessage.UID, "transferred", time.Date(2026, 7, 14, 4, 5, 6, 0, time.UTC), 602, annotationMessageSHA(fixture.other.ID, otherMessage.UID)); err != nil {
		t.Fatal(err)
	}

	backend := &remoteIMAPSyncBackend{}
	host := routeAPIHost{st: fixture.store}
	ownerAnnotations, err := backend.MessageAnnotations(ctx, host, fixture.owner.ID, []int64{ownerMessage.ID, otherMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerAnnotations) != 1 || len(ownerAnnotations[ownerMessage.ID]) != 1 {
		t.Fatalf("owner annotations = %#v", ownerAnnotations)
	}
	if _, leaked := ownerAnnotations[otherMessage.ID]; leaked {
		t.Fatal("owner annotation request exposed another user's transfer journal")
	}

	otherAnnotations, err := backend.MessageAnnotations(ctx, host, fixture.other.ID, []int64{ownerMessage.ID, otherMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherAnnotations) != 1 || len(otherAnnotations[otherMessage.ID]) != 1 {
		t.Fatalf("other-user annotations = %#v", otherAnnotations)
	}
	if _, leaked := otherAnnotations[ownerMessage.ID]; leaked {
		t.Fatal("other-user annotation request exposed the owner's transfer journal")
	}
}

func TestMessageAnnotationProvenanceSurvivesRoutineResetAndDeletion(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	routine := createAnnotationRoutine(t, ctx, fixture, fixture.owner.ID, fixture.inputForOwner("owner-password"))
	message := createAnnotationMessage(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerAccount.ID, fixture.ownerMailbox.ID, 310)
	later := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)
	earlier := later.Add(-2 * time.Hour)
	destinationUIDValidity := uint32(701)
	setAnnotationMailboxUIDValidity(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerMailbox.ID, destinationUIDValidity)
	messageSHA := annotationMessageSHA(fixture.owner.ID, message.UID)
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 40, "fingerprint-later", "marker-later", message.UID, "transferred", later, destinationUIDValidity, messageSHA); err != nil {
		t.Fatal(err)
	}
	differentSHA := annotationSHA("different destination bytes")
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 41, "fingerprint-conflict", "marker-conflict", message.UID, "transferred", earlier.Add(-time.Hour), destinationUIDValidity, differentSHA); err != nil {
		t.Fatal(err)
	}
	assertProvenanceIdentity(t, ctx, fixture, message.UID, destinationUIDValidity, later, messageSHA)
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 42, "fingerprint-earlier", "marker-earlier", message.UID, "transferred", earlier, destinationUIDValidity, messageSHA); err != nil {
		t.Fatal(err)
	}
	assertProvenanceIdentity(t, ctx, fixture, message.UID, destinationUIDValidity, earlier, messageSHA)
	assertAnnotationSyncedAt(t, ctx, fixture, message.ID, earlier)

	tx, err := fixture.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := resetRoutineProgress(ctx, tx, fixture.owner.ID, routine.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertAnnotationSyncedAt(t, ctx, fixture, message.ID, earlier)

	if err := deleteRoutine(ctx, fixture.db, fixture.owner.ID, routine.ID); err != nil {
		t.Fatal(err)
	}
	assertAnnotationSyncedAt(t, ctx, fixture, message.ID, earlier)

	var provenanceRows int
	if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_remote_imap_sync_provenance
		WHERE user_id = ? AND destination_account_id = ? AND destination_mailbox_id = ?
		AND destination_uidvalidity = ? AND destination_uid = ?`,
		fixture.owner.ID, fixture.ownerAccount.ID, fixture.ownerMailbox.ID, destinationUIDValidity, message.UID).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 1 {
		t.Fatalf("provenance rows after routine deletion = %d, want 1", provenanceRows)
	}
}

func TestMessageAnnotationsRequireDestinationEpochAndExactBlobSHA(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	routine := createAnnotationRoutine(t, ctx, fixture, fixture.owner.ID, fixture.inputForOwner("owner-password"))
	message := createAnnotationMessage(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerAccount.ID, fixture.ownerMailbox.ID, 410)
	syncedAt := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	newMessageSHA := annotationSHA("new message bytes at reused uid")
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 50, "fingerprint-new-epoch", "marker-new-epoch", message.UID, "transferred", syncedAt, 802, newMessageSHA); err != nil {
		t.Fatal(err)
	}

	setAnnotationMailboxUIDValidity(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerMailbox.ID, 802)
	assertNoMessageAnnotations(t, ctx, fixture, message.ID, "stale cached row with a reused UID and different bytes")

	if _, err := fixture.db.ExecContext(ctx, `UPDATE blobs SET sha256 = ? WHERE user_id = ? AND id = ?`,
		newMessageSHA, fixture.owner.ID, message.BlobID); err != nil {
		t.Fatal(err)
	}
	assertAnnotationSyncedAt(t, ctx, fixture, message.ID, syncedAt)

	setAnnotationMailboxUIDValidity(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerMailbox.ID, 803)
	assertNoMessageAnnotations(t, ctx, fixture, message.ID, "unknown UIDVALIDITY epoch")
}

func TestProvenanceMigrationDoesNotGuessLegacyDestinationUIDValidity(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	routine := createAnnotationRoutine(t, ctx, fixture, fixture.owner.ID, fixture.inputForOwner("owner-password"))
	message := createAnnotationMessage(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerAccount.ID, fixture.ownerMailbox.ID, 510)
	setAnnotationMailboxUIDValidity(t, ctx, fixture.store, fixture.owner.ID, fixture.ownerMailbox.ID, 901)
	insertAnnotationJournalRow(t, ctx, fixture.db, routine, 60, message.UID, "transferred", time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC))
	applyAnnotationProvenanceMigration(t, ctx, fixture.db)

	var provenanceRows int
	if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_remote_imap_sync_provenance WHERE user_id = ?`, fixture.owner.ID).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 0 {
		t.Fatalf("legacy journal backfill created %d provenance rows without destination UIDVALIDITY", provenanceRows)
	}
	annotations, err := (&remoteIMAPSyncBackend{}).MessageAnnotations(ctx, routeAPIHost{st: fixture.store}, fixture.owner.ID, []int64{message.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(annotations) != 0 {
		t.Fatalf("legacy journal row produced annotations without safe provenance: %#v", annotations)
	}
}

func TestRecordHandledMessageRequiresCompleteDestinationIdentityForProvenance(t *testing.T) {
	fixture := newBackendFixture(t)
	ctx := context.Background()
	routine := createAnnotationRoutine(t, ctx, fixture, fixture.owner.ID, fixture.inputForOwner("owner-password"))
	syncedAt := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	validSHA := annotationSHA("valid destination bytes")
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 70, "fingerprint-no-epoch", "marker-no-epoch", 610, "transferred", syncedAt, 0, validSHA); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 71, "fingerprint-no-uid", "marker-no-uid", 0, "transferred", syncedAt, 1001, validSHA); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 72, "fingerprint-pre-epoch-time", "marker-pre-epoch-time", 611, "transferred", time.Unix(-1, 0), 1001, validSHA); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 73, "fingerprint-missing-hash", "marker-missing-hash", 612, "transferred", syncedAt, 1001, ""); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 74, "fingerprint-uppercase-hash", "marker-uppercase-hash", 613, "transferred", syncedAt, 1001, "A"+validSHA[1:]); err != nil {
		t.Fatal(err)
	}
	if err := recordHandledMessageAt(ctx, fixture.db, routine, 77, 75, "fingerprint-invalid-hash", "marker-invalid-hash", 614, "transferred", syncedAt, 1001, "g"+validSHA[1:]); err != nil {
		t.Fatal(err)
	}
	var provenanceRows int
	if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_remote_imap_sync_provenance WHERE user_id = ?`, fixture.owner.ID).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 0 {
		t.Fatalf("incomplete destination identity created %d provenance rows", provenanceRows)
	}
}

func createAnnotationRoutine(t *testing.T, ctx context.Context, fixture backendFixture, userID int64, input routineInput) routine {
	t.Helper()
	backend := &remoteIMAPSyncBackend{}
	item, err := backend.prepareRoutine(ctx, testAPIHost{}, fixture.store, fixture.db, userID, 0, input)
	if err != nil {
		t.Fatal(err)
	}
	item, err = persistRoutine(ctx, fixture.db, item)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func createAnnotationMessage(t *testing.T, ctx context.Context, st *store.Store, userID, accountID, mailboxID int64, uid uint32) store.MessageRecord {
	t.Helper()
	path := fmt.Sprintf("users/%d/annotation-test/uid-%d.eml", userID, uid)
	blob, err := st.CreateBlob(ctx, store.BlobRecord{
		UserID: userID, Kind: "message", Path: path,
		SHA256: annotationMessageSHA(userID, uid), Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := st.CreateMessage(ctx, store.CreateMessage{
		UserID: userID, AccountID: accountID, MailboxID: mailboxID, BlobID: blob.ID,
		UID: uid, BlobPath: path, Subject: fmt.Sprintf("Message %d", uid),
		Date: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func insertAnnotationJournalRow(t *testing.T, ctx context.Context, db *sql.DB, item routine, sourceUID uint32, destinationUID uint32, status string, copiedAt time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_remote_imap_sync_messages
		(user_id, routine_id, source_uidvalidity, source_uid, source_fingerprint, marker,
		 destination_uid, status, copied_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.UserID, item.ID, 77, sourceUID,
		fmt.Sprintf("fingerprint-%d-%d", item.ID, sourceUID),
		fmt.Sprintf("marker-%d-%d", item.ID, sourceUID), destinationUID, status, copiedAt.UTC().Unix())
	if err != nil {
		t.Fatal(err)
	}
}

func applyAnnotationProvenanceMigration(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	migrationFile := filepath.Join("..", "migrations", "user", "002_create_remote_imap_sync_provenance.sql")
	migration, err := os.ReadFile(migrationFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply %s: %v", filepath.Base(migrationFile), err)
	}
}

func setAnnotationMailboxUIDValidity(t *testing.T, ctx context.Context, st *store.Store, userID, mailboxID int64, uidValidity uint32) {
	t.Helper()
	if err := st.UpdateMailboxRemoteStatus(ctx, userID, mailboxID, 0, 0, 0, uidValidity); err != nil {
		t.Fatal(err)
	}
}

func assertAnnotationSyncedAt(t *testing.T, ctx context.Context, fixture backendFixture, messageID int64, want time.Time) {
	t.Helper()
	annotations, err := (&remoteIMAPSyncBackend{}).MessageAnnotations(ctx, routeAPIHost{st: fixture.store}, fixture.owner.ID, []int64{messageID})
	if err != nil {
		t.Fatal(err)
	}
	if len(annotations[messageID]) != 1 {
		t.Fatalf("annotations for message %d = %#v", messageID, annotations[messageID])
	}
	if got := annotations[messageID][0].Metadata["synced_at"]; got != want.UTC().Format(time.RFC3339) {
		t.Fatalf("synced_at = %q, want %q", got, want.UTC().Format(time.RFC3339))
	}
}

func assertNoMessageAnnotations(t *testing.T, ctx context.Context, fixture backendFixture, messageID int64, reason string) {
	t.Helper()
	annotations, err := (&remoteIMAPSyncBackend{}).MessageAnnotations(ctx, routeAPIHost{st: fixture.store}, fixture.owner.ID, []int64{messageID})
	if err != nil {
		t.Fatal(err)
	}
	if len(annotations) != 0 {
		t.Fatalf("annotations for %s = %#v, want none", reason, annotations)
	}
}

func assertProvenanceIdentity(t *testing.T, ctx context.Context, fixture backendFixture, destinationUID, destinationUIDValidity uint32, wantTime time.Time, wantSHA string) {
	t.Helper()
	var syncedAt int64
	var destinationSHA string
	if err := fixture.db.QueryRowContext(ctx, `SELECT synced_at, destination_sha256
		FROM plugin_remote_imap_sync_provenance
		WHERE user_id = ? AND destination_account_id = ? AND destination_mailbox_id = ?
		AND destination_uidvalidity = ? AND destination_uid = ?`, fixture.owner.ID,
		fixture.ownerAccount.ID, fixture.ownerMailbox.ID, destinationUIDValidity, destinationUID).Scan(&syncedAt, &destinationSHA); err != nil {
		t.Fatal(err)
	}
	if syncedAt != wantTime.UTC().Unix() || destinationSHA != wantSHA {
		t.Fatalf("provenance identity = time %d SHA %q, want time %d SHA %q", syncedAt, destinationSHA, wantTime.UTC().Unix(), wantSHA)
	}
}

func annotationMessageSHA(userID int64, uid uint32) string {
	return annotationSHA(fmt.Sprintf("annotation-%d-%d", userID, uid))
}

func annotationSHA(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
