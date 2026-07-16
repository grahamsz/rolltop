package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestInstanceLockRefusesSecondProcessForDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	first, err := acquireInstanceLock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := acquireInstanceLock(dataDir)
	if second != nil {
		_ = second.Close()
		t.Fatal("second instance lock unexpectedly succeeded")
	}
	if !errors.Is(err, errRolltopAlreadyRunning) || !strings.Contains(err.Error(), "stop the server") {
		t.Fatalf("second instance lock error = %v", err)
	}
}

func TestResetSearchRequiresExplicitOfflineConfirmation(t *testing.T) {
	err := runCommand(context.Background(), []string{"reset-search", "--user-id", "1"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--confirm-offline") {
		t.Fatalf("reset-search error = %v", err)
	}
}

func TestResetSearchQuarantinesTargetIndexAndMarksVisibleMessages(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	pluginDir := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLTOP_DATA_DIR", dataDir)
	t.Setenv("ROLLTOP_DB_PATH", filepath.Join(dataDir, "rolltop.db"))
	t.Setenv("ROLLTOP_PLUGIN_DIR", pluginDir)
	t.Setenv("ROLLTOP_MASTER_KEY", "01234567890123456789012345678901")

	db, err := store.OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "offline-reset@example.test", "Offline Reset", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "offline-reset-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerMessage := createResetSearchCommandMessage(t, ctx, db, user, 1)
	otherMessage := createResetSearchCommandMessage(t, ctx, db, other, 1)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	ownerIndex := filepath.Join(dataDir, "users", strconv.FormatInt(user.ID, 10), "bleve")
	otherIndex := filepath.Join(dataDir, "users", strconv.FormatInt(other.ID, 10), "bleve")
	for _, path := range []string{ownerIndex, otherIndex} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ownerIndex, "owner"), []byte("owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherIndex, "other"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := runCommand(ctx, []string{"reset-search", "--user-id", strconv.FormatInt(user.ID, 10), "--confirm-offline"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Quarantined user") || !strings.Contains(stdout.String(), "To restore before starting Rolltop") {
		t.Fatalf("reset output = %q", stdout.String())
	}
	if _, err := os.Stat(ownerIndex); !os.IsNotExist(err) {
		t.Fatalf("owner live index still exists: %v", err)
	}
	quarantines, err := filepath.Glob(ownerIndex + ".quarantine-*")
	if err != nil || len(quarantines) != 1 {
		t.Fatalf("owner quarantines = %v, %v", quarantines, err)
	}
	if _, err := os.Stat(otherIndex); err != nil {
		t.Fatalf("other tenant index changed: %v", err)
	}

	db, err = store.OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ownerAfter, err := db.GetMessageForUser(ctx, user.ID, ownerMessage.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherAfter, err := db.GetMessageForUser(ctx, other.ID, otherMessage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ownerAfter.AttachmentIndexedAt.IsZero() {
		t.Fatal("owner message was not marked pending")
	}
	if otherAfter.AttachmentIndexedAt.IsZero() {
		t.Fatal("other tenant message was marked pending")
	}
	if ownerAfter.Subject != ownerMessage.Subject || ownerAfter.BlobID != ownerMessage.BlobID || ownerAfter.UID != ownerMessage.UID {
		t.Fatalf("owner message content changed: before=%+v after=%+v", ownerMessage, ownerAfter)
	}
}

func TestResetSearchRejectsSymlinkedTenantBeforeOpeningUserDatabase(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	pluginDir := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLTOP_DATA_DIR", dataDir)
	t.Setenv("ROLLTOP_DB_PATH", filepath.Join(dataDir, "rolltop.db"))
	t.Setenv("ROLLTOP_PLUGIN_DIR", pluginDir)
	t.Setenv("ROLLTOP_MASTER_KEY", "01234567890123456789012345678901")

	db, err := store.OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "symlink-reset@example.test", "Symlink Reset", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	usersRoot := filepath.Join(dataDir, "users")
	outside := filepath.Join(t.TempDir(), "outside-tenant")
	if err := os.MkdirAll(usersRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(usersRoot, strconv.FormatInt(user.ID, 10))); err != nil {
		t.Fatal(err)
	}

	err = runCommand(ctx, []string{"reset-search", "--user-id", strconv.FormatInt(user.ID, 10), "--confirm-offline"},
		&bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not a regular directory") {
		t.Fatalf("symlinked tenant reset error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "rolltop.db")); !os.IsNotExist(err) {
		t.Fatalf("reset opened or created an external tenant database: %v", err)
	}
}

func createResetSearchCommandMessage(t *testing.T, ctx context.Context, db *store.Store, user store.User, uid uint32) store.MessageRecord {
	t.Helper()
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: user.Email + ".eml", SHA256: user.Email, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: "<" + user.Email + ">", Subject: "Preserved message", FromAddr: "sender@example.test",
		ToAddr: user.Email, Date: time.Now(), InternalDate: time.Now(), UID: uid, Size: 10,
		BlobPath: blob.Path, BodyText: "preserved body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageAttachmentIndexed(ctx, user.ID, message.ID, false); err != nil {
		t.Fatal(err)
	}
	message, err = db.GetMessageForUser(ctx, user.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	return message
}
