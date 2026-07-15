package syncer

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestRequestedNeverMailboxBypassRequiresExactRebuildMarker(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-never@example.test", "Rebuild Never", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	firstAccount := recoveryTestAccount(t, ctx, db, user.ID, "first")
	secondAccount := recoveryTestAccount(t, ctx, db, user.ID, "second")
	firstMailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, firstAccount.ID, "INBOX")
	recoveryTestNeverMailbox(t, ctx, db, user.ID, secondAccount.ID, "INBOX")
	service := &Service{Store: db}

	mailboxes, err := service.requestedMailboxes(ctx, firstAccount, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 0 {
		t.Fatalf("never mailbox without marker was requested: %v", mailboxes)
	}
	insertRecoveryTestMarker(t, ctx, db, user.ID, firstAccount.ID, firstMailbox.ID, 42)
	mailboxes, err = service.requestedMailboxes(ctx, firstAccount, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 1 || mailboxes[0] != "INBOX" {
		t.Fatalf("marked never mailbox request=%v, want INBOX", mailboxes)
	}
	mailboxes, err = service.requestedMailboxes(ctx, secondAccount, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 0 {
		t.Fatalf("marker crossed into same-named mailbox on another account: %v", mailboxes)
	}

	other, err := db.CreateUser(ctx, "rebuild-never-other@example.test", "Other Rebuild", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount := recoveryTestAccount(t, ctx, db, other.ID, "other")
	recoveryTestNeverMailbox(t, ctx, db, other.ID, otherAccount.ID, "INBOX")
	mailboxes, err = service.requestedMailboxes(ctx, otherAccount, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 0 {
		t.Fatalf("marker crossed tenant boundary: %v", mailboxes)
	}
}

func TestMailboxGenerationRecoveryRetriesUntilMarkerClears(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-retry@example.test", "Rebuild Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "retry")
	mailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, mailbox.ID, 77)
	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.rebuildRecoveryInterval = 5 * time.Millisecond
	queued := make(chan store.PendingMailboxGenerationRebuild, 4)
	result := make(chan error, 1)
	var calls atomic.Int64
	runner.queueRebuildMailbox = func(rebuild store.PendingMailboxGenerationRebuild) {
		call := calls.Add(1)
		queued <- rebuild
		if call == 2 {
			result <- db.FinalizeMailboxGenerationRebuild(ctx, rebuild.UserID, rebuild.AccountID,
				rebuild.MailboxID, rebuild.TargetUIDValidity)
		}
	}
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	first := <-queued
	if first.UserID != user.ID || first.AccountID != account.ID || first.MailboxID != mailbox.ID {
		t.Fatalf("initial recovery queue=%+v, want exact marker", first)
	}
	select {
	case second := <-queued:
		if second.UserID != user.ID || second.AccountID != account.ID || second.MailboxID != mailbox.ID {
			t.Fatalf("retry recovery queue=%+v, want exact marker", second)
		}
	case <-time.After(time.Second):
		t.Fatal("failed recovery pass was not retried")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		pending, err := db.MailboxGenerationRebuildExists(ctx, user.ID, account.ID, mailbox.ID)
		if err != nil {
			t.Fatal(err)
		}
		runner.mu.Lock()
		running := runner.rebuildRecoveryRunning
		runner.mu.Unlock()
		if !pending && !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovery did not stop after marker cleared: pending=%v running=%v", pending, running)
		}
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("recovery queue calls=%d, want failed pass plus one retry", got)
	}
}

func TestMailboxGenerationRecoveryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-cancel@example.test", "Rebuild Cancel", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "cancel")
	mailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, mailbox.ID, 88)
	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.rebuildRecoveryInterval = time.Hour
	var calls atomic.Int64
	runner.queueRebuildMailbox = func(store.PendingMailboxGenerationRebuild) {
		calls.Add(1)
	}
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("initial recovery calls=%d, want one", calls.Load())
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		running := runner.rebuildRecoveryRunning
		runner.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild recovery loop ignored cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("recovery queued after cancellation: calls=%d", calls.Load())
	}
}

func recoveryTestAccount(t *testing.T, ctx context.Context, db *store.Store, userID int64, suffix string) store.MailAccount {
	t.Helper()
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: userID, Email: suffix + "@example.test", Host: "imap.example.test", Port: 993,
		Username: suffix, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func recoveryTestNeverMailbox(t *testing.T, ctx context.Context, db *store.Store, userID, accountID int64, name string) store.Mailbox {
	t.Helper()
	mailbox, err := db.GetOrCreateMailbox(ctx, userID, accountID, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSyncMode(ctx, userID, mailbox.ID, "never"); err != nil {
		t.Fatal(err)
	}
	mailbox.SyncMode = "never"
	return mailbox
}

func insertRecoveryTestMarker(t *testing.T, ctx context.Context, db *store.Store,
	userID, accountID, mailboxID int64, targetUIDValidity uint32,
) {
	t.Helper()
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := userDB.ExecContext(ctx, `INSERT INTO mailbox_generation_rebuilds
		(user_id, account_id, mailbox_id, target_uid_validity, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, userID, accountID, mailboxID, targetUIDValidity, now, now); err != nil {
		t.Fatal(err)
	}
}
