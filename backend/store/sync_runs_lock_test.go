package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFinishSyncRunRetriesTransientWriterLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user, account, _, _ := testMailbox(t, ctx, st)
	run, err := st.CreateSyncRun(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	st.db.SetMaxOpenConns(1)
	if _, err := st.db.Exec(`PRAGMA busy_timeout = 1`); err != nil {
		t.Fatal(err)
	}
	blocker, err := sql.Open("sqlite3", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	tx, err := blocker.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	progress := SyncProgress{CurrentMailbox: "INBOX", MessagesSeen: 4, MessagesStored: 4, CurrentUID: 10}
	if err := st.finishSyncRunOnce(ctx, user.ID, run.ID, "ok", progress, ""); !isSQLiteBusyError(err) {
		t.Fatalf("expected competing writer to block completion, got %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- st.FinishSyncRun(ctx, user.ID, run.ID, "ok", progress, "") }()
	select {
	case err := <-done:
		t.Fatalf("completion abandoned a transient writer lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not resume after the writer released its lock")
	}
	finished, err := st.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "ok" || finished.FinishedAt.IsZero() || finished.MessagesStored != 4 || finished.CurrentUID != 10 {
		t.Fatalf("final status/progress not saved: %+v", finished)
	}
}

func TestFinishSyncRunBoundsBackgroundConnectionWait(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user, account, _, _ := testMailbox(t, ctx, st)
	run, err := st.CreateSyncRun(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	st.db.SetMaxOpenConns(1)
	conn, err := st.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	started := time.Now()
	err = st.finishSyncRunWithTimeout(ctx, user.ID, run.ID, "interrupted", SyncProgress{}, "Cancelled", 30*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool wait error = %v, want deadline exceeded", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("cleanup retained its worker reservation while the pool was exhausted")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishSyncRun(ctx, user.ID, run.ID, "interrupted", SyncProgress{}, "Cancelled"); err != nil {
		t.Fatal(err)
	}
}

func TestFinishSyncRunKeepsTenantAndCancellationBoundaries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := OpenServer(filepath.Join(root, "rolltop.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var users []User
	var runs []SyncRun
	for _, email := range []string{"one@example.test", "two@example.test"} {
		user, err := st.CreateUser(ctx, email, "User", "hash", false)
		if err != nil {
			t.Fatal(err)
		}
		account, err := st.CreateMailAccount(ctx, MailAccount{UserID: user.ID, Email: email, Host: "imap.example.test", Port: 993, Username: email, EncryptedPassword: "encrypted", UseTLS: true})
		if err != nil {
			t.Fatal(err)
		}
		run, err := st.CreateSyncRun(ctx, user.ID, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		users, runs = append(users, user), append(runs, run)
	}
	if runs[0].ID != runs[1].ID {
		t.Fatal("fixture needs colliding per-tenant run IDs")
	}
	if err := st.InterruptSyncRunForUser(ctx, users[0].ID, runs[0].ID, "Cancelled by user."); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishSyncRun(ctx, users[0].ID, runs[0].ID, "ok", SyncProgress{}, ""); err != nil {
		t.Fatal(err)
	}
	first, err := st.GetSyncRunForUser(ctx, users[0].ID, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.GetSyncRunForUser(ctx, users[1].ID, runs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "interrupted" || first.Error != "Cancelled by user." || second.Status != "running" {
		t.Fatal("completion crossed tenant or terminal cancellation boundaries")
	}
}
