package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListPendingMailboxGenerationRebuildsPrioritizesInboxWithinEachUser(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first, err := db.CreateUser(ctx, "recovery-order-first@example.test", "First", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateUser(ctx, "recovery-order-second@example.test", "Second", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	firstHistoryAccount := createRecoveryOrderAccount(t, ctx, db, first.ID, "first-history")
	firstHistory, err := db.GetOrCreateMailbox(ctx, first.ID, firstHistoryAccount.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	firstInboxAccount := createRecoveryOrderAccount(t, ctx, db, first.ID, "first-inbox")
	firstInbox, err := db.GetOrCreateMailboxWithRole(ctx, first.ID, firstInboxAccount.ID, "Primary", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryOrderMarker(t, ctx, db, first.ID, firstHistoryAccount.ID, firstHistory.ID, 101)
	insertRecoveryOrderMarker(t, ctx, db, first.ID, firstInboxAccount.ID, firstInbox.ID, 102)

	secondAccount := createRecoveryOrderAccount(t, ctx, db, second.ID, "second")
	secondHistory, err := db.GetOrCreateMailbox(ctx, second.ID, secondAccount.ID, "History")
	if err != nil {
		t.Fatal(err)
	}
	secondInbox, err := db.GetOrCreateMailbox(ctx, second.ID, secondAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryOrderMarker(t, ctx, db, second.ID, secondAccount.ID, secondHistory.ID, 201)
	insertRecoveryOrderMarker(t, ctx, db, second.ID, secondAccount.ID, secondInbox.ID, 202)

	rebuilds, err := db.ListPendingMailboxGenerationRebuilds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []PendingMailboxGenerationRebuild{
		{UserID: first.ID, AccountID: firstInboxAccount.ID, MailboxID: firstInbox.ID, MailboxName: firstInbox.Name, TargetUIDValidity: 102},
		{UserID: first.ID, AccountID: firstHistoryAccount.ID, MailboxID: firstHistory.ID, MailboxName: firstHistory.Name, TargetUIDValidity: 101},
		{UserID: second.ID, AccountID: secondAccount.ID, MailboxID: secondInbox.ID, MailboxName: secondInbox.Name, TargetUIDValidity: 202},
		{UserID: second.ID, AccountID: secondAccount.ID, MailboxID: secondHistory.ID, MailboxName: secondHistory.Name, TargetUIDValidity: 201},
	}
	if len(rebuilds) != len(want) {
		t.Fatalf("pending rebuilds=%+v, want %d", rebuilds, len(want))
	}
	for i := range want {
		got := rebuilds[i]
		if got.UserID != want[i].UserID || got.AccountID != want[i].AccountID ||
			got.MailboxID != want[i].MailboxID || got.MailboxName != want[i].MailboxName ||
			got.TargetUIDValidity != want[i].TargetUIDValidity {
			t.Fatalf("pending rebuild %d=%+v, want %+v", i, got, want[i])
		}
	}
}

func TestHasPendingMailboxGenerationRebuildsForUserIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUser(ctx, "recovery-pending-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "recovery-pending-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := createRecoveryOrderAccount(t, ctx, db, owner.ID, "pending-owner")
	mailbox, err := db.GetOrCreateMailbox(ctx, owner.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryOrderMarker(t, ctx, db, owner.ID, account.ID, mailbox.ID, 301)

	ownerPending, err := db.HasPendingMailboxGenerationRebuildsForUser(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherPending, err := db.HasPendingMailboxGenerationRebuildsForUser(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ownerPending || otherPending {
		t.Fatalf("pending rebuilds owner=%t other=%t, want true/false", ownerPending, otherPending)
	}

	if err := db.FinalizeMailboxGenerationRebuild(ctx, owner.ID, account.ID, mailbox.ID, 301); err != nil {
		t.Fatal(err)
	}
	ownerPending, err = db.HasPendingMailboxGenerationRebuildsForUser(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ownerPending {
		t.Fatal("owner remained blocked after its rebuild marker cleared")
	}
}

func createRecoveryOrderAccount(t *testing.T, ctx context.Context, db *Store, userID int64, label string) MailAccount {
	t.Helper()
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: userID, Email: label + "@example.test", Label: label,
		Host: "imap.example.test", Port: 993, Username: label,
		EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func insertRecoveryOrderMarker(t *testing.T, ctx context.Context, db *Store,
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
