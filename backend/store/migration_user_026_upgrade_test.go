package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

type legacyV25UIDValidityFixture struct {
	UserID                   int64
	Email                    string
	AccountID                int64
	KnownMailboxID           int64
	PendingMailboxID         int64
	LegacyMessageID          int64
	ProvenMessageID          int64
	PendingMessageID         int64
	KnownUIDValidity         int64
	ProvenUIDValidity        int64
	PendingTargetUIDValidity int64
}

func newLegacyV25UIDValidityFixture(userID int64, email string, knownUIDValidity int64) legacyV25UIDValidityFixture {
	base := userID * 100
	return legacyV25UIDValidityFixture{
		UserID:                   userID,
		Email:                    email,
		AccountID:                base + 1,
		KnownMailboxID:           base + 2,
		PendingMailboxID:         base + 3,
		LegacyMessageID:          base + 7,
		ProvenMessageID:          base + 8,
		PendingMessageID:         base + 9,
		KnownUIDValidity:         knownUIDValidity,
		ProvenUIDValidity:        knownUIDValidity + 1_000,
		PendingTargetUIDValidity: knownUIDValidity + 1,
	}
}

func TestUserSchemaUpgradeFromV25SplitUserStorePreservesUnprovenGenerations(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	systemPath := filepath.Join(dataDir, databaseFilename)

	setup, err := OpenServer(systemPath, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	firstUser, err := setup.CreateUser(ctx, "first-v25@upgrade.example.test", "First V25", "first-hash", false)
	if err != nil {
		t.Fatal(err)
	}
	secondUser, err := setup.CreateUser(ctx, "second-v25@upgrade.example.test", "Second V25", "second-hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}

	first := newLegacyV25UIDValidityFixture(firstUser.ID, firstUser.Email, 711)
	second := newLegacyV25UIDValidityFixture(secondUser.ID, secondUser.Email, 822)
	createLegacyV25UIDValidityDatabase(t, userDatabasePath(dataDir, first.UserID), first)
	createLegacyV25UIDValidityDatabase(t, userDatabasePath(dataDir, second.UserID), second)

	server, err := OpenServer(systemPath, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	firstStore, err := server.UserStore(ctx, first.UserID)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyV25UIDValidityUpgraded(t, ctx, firstStore.DB(), first)
	assertUser026MigrationChecksum(t, ctx, firstStore.DB())
	assertSplitDatabaseContainsOnlyUser(t, ctx, firstStore.DB(), first.UserID, second.UserID)
	if _, err := firstStore.GetMessageForUser(ctx, second.UserID, first.LegacyMessageID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second tenant reading first tenant message error = %v, want not found", err)
	}

	// UserStore migrates one tenant file at a time. Opening the first tenant must
	// not rewrite or mark the second tenant's still-closed v025 database.
	assertLegacyV25DatabaseStillUnmigrated(t, ctx, userDatabasePath(dataDir, second.UserID), second)

	secondStore, err := server.UserStore(ctx, second.UserID)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyV25UIDValidityUpgraded(t, ctx, secondStore.DB(), second)
	assertUser026MigrationChecksum(t, ctx, secondStore.DB())
	assertSplitDatabaseContainsOnlyUser(t, ctx, secondStore.DB(), second.UserID, first.UserID)
	if _, err := secondStore.GetMessageForUser(ctx, first.UserID, second.LegacyMessageID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first tenant reading second tenant message error = %v, want not found", err)
	}

	var mailTable string
	if err := server.DB().QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'messages'`).Scan(&mailTable); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("system database messages lookup error = %v, want no table", err)
	}
}

func createLegacyV25UIDValidityDatabase(t *testing.T, path string, fixture legacyV25UIDValidityFixture) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	legacyStore := &Store{db: db, schema: schemaUser}
	ctx := context.Background()
	sets := userMigrationSetsBeforeV26(t)
	for _, set := range sets {
		if err := legacyStore.applyMigrationSet(ctx, set, nil); err != nil {
			_ = db.Close()
			t.Fatalf("apply legacy migration %s: %v", set.Version, err)
		}
	}
	assertCurrentMigrationChecksums(t, ctx, db, sets)
	seedLegacyV25UIDValidityFixture(t, ctx, db, fixture)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func userMigrationSetsBeforeV26(t *testing.T) []migrationSet {
	t.Helper()
	sets := currentUserMigrationSetsForUpgradeTest()
	for i, set := range sets {
		if set.Version != UserSchemaVersion026 {
			continue
		}
		if i == 0 {
			t.Fatalf("%s has no predecessor", UserSchemaVersion026)
		}
		if sets[i-1].Version != UserSchemaVersion025 {
			t.Fatalf("user-026 predecessor = %q, want %q", sets[i-1].Version, UserSchemaVersion025)
		}
		return append([]migrationSet(nil), sets[:i]...)
	}
	t.Fatalf("%s missing from current user migrations", UserSchemaVersion026)
	return nil
}

func seedLegacyV25UIDValidityFixture(t *testing.T, ctx context.Context, db *sql.DB, fixture legacyV25UIDValidityFixture) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(err error) {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users
		(id, email, name, password_hash, is_admin, created_at, updated_at)
		VALUES (?, ?, 'Legacy V25', 'legacy-hash', 0, 100, 101)`, fixture.UserID, fixture.Email); err != nil {
		fail(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mail_accounts
		(id, user_id, email, host, port, username, encrypted_password, use_tls, mailbox, created_at, updated_at)
		VALUES (?, ?, ?, 'imap.upgrade.example.test', 993, ?, 'encrypted-legacy-password', 1, '*', 110, 111)`,
		fixture.AccountID, fixture.UserID, fixture.Email, fixture.Email); err != nil {
		fail(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mailboxes
		(id, user_id, account_id, name, role, uidvalidity, last_uid, remote_uid_next, created_at, updated_at)
		VALUES (?, ?, ?, 'INBOX', 'inbox', ?, 12, 13, 120, 121),
		       (?, ?, ?, 'Archive', 'archive', ?, 13, 14, 122, 123)`,
		fixture.KnownMailboxID, fixture.UserID, fixture.AccountID, fixture.KnownUIDValidity,
		fixture.PendingMailboxID, fixture.UserID, fixture.AccountID, fixture.PendingTargetUIDValidity); err != nil {
		fail(err)
	}
	for i, messageID := range []int64{fixture.LegacyMessageID, fixture.ProvenMessageID, fixture.PendingMessageID} {
		blobID := fixture.UserID*100 + int64(4+i)
		blobPath := fmt.Sprintf("users/%d/blobs/v25-message-%d.eml", fixture.UserID, messageID)
		if _, err := tx.ExecContext(ctx, `INSERT INTO blobs (id, user_id, kind, path, sha256, size, created_at)
			VALUES (?, ?, 'message', ?, ?, 10, 130)`, blobID, fixture.UserID, blobPath, fmt.Sprintf("sha-%d", messageID)); err != nil {
			fail(err)
		}
		mailboxID := fixture.KnownMailboxID
		uidValidity := int64(0)
		if messageID == fixture.ProvenMessageID {
			uidValidity = fixture.ProvenUIDValidity
		}
		if messageID == fixture.PendingMessageID {
			mailboxID = fixture.PendingMailboxID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages
			(id, user_id, account_id, mailbox_id, blob_id, subject, uid, blob_path, uid_validity, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 140, 141)`, messageID, fixture.UserID, fixture.AccountID,
			mailboxID, blobID, fmt.Sprintf("Legacy message %d", messageID), int64(i+11), blobPath, uidValidity); err != nil {
			fail(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mailbox_generation_rebuilds
		(user_id, account_id, mailbox_id, target_uid_validity, created_at, updated_at)
		VALUES (?, ?, ?, ?, 150, 151)`, fixture.UserID, fixture.AccountID, fixture.PendingMailboxID, fixture.PendingTargetUIDValidity); err != nil {
		fail(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertLegacyV25UIDValidityUpgraded(t *testing.T, ctx context.Context, db *sql.DB, fixture legacyV25UIDValidityFixture) {
	t.Helper()
	assertMessageUIDValidity(t, ctx, db, fixture.UserID, fixture.LegacyMessageID, 0)
	assertMessageUIDValidity(t, ctx, db, fixture.UserID, fixture.ProvenMessageID, fixture.ProvenUIDValidity)
	assertMessageUIDValidity(t, ctx, db, fixture.UserID, fixture.PendingMessageID, 0)
	var arrivalUIDFloor int64
	if err := db.QueryRowContext(ctx, `SELECT arrival_uid_floor FROM mailbox_generation_rebuilds
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`, fixture.UserID,
		fixture.AccountID, fixture.PendingMailboxID).Scan(&arrivalUIDFloor); err != nil {
		t.Fatal(err)
	}
	if arrivalUIDFloor != 0 {
		t.Fatalf("upgraded pending marker arrival floor=%d, want 0", arrivalUIDFloor)
	}
}

func assertLegacyV25DatabaseStillUnmigrated(t *testing.T, ctx context.Context, path string, fixture legacyV25UIDValidityFixture) {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE scope = 'user' AND version = ?`, UserSchemaVersion026).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("closed tenant has %d %s migration records, want 0", applied, UserSchemaVersion026)
	}
	assertMessageUIDValidity(t, ctx, db, fixture.UserID, fixture.LegacyMessageID, 0)
}

func assertUser026MigrationChecksum(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var checksum string
	if err := db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE scope = 'user' AND version = ?`, UserSchemaVersion026).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if want := migrationChecksum(userMailboxGenerationArrivalFloorMigrationSet()); checksum != want {
		t.Fatalf("%s checksum = %q, want %q", UserSchemaVersion026, checksum, want)
	}
}

func assertMessageUIDValidity(t *testing.T, ctx context.Context, db *sql.DB, userID, messageID, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRowContext(ctx, `SELECT uid_validity FROM messages WHERE user_id = ? AND id = ?`, userID, messageID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("message %d uid_validity = %d, want %d", messageID, got, want)
	}
}
