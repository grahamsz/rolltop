package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateMessageWaitsForConcurrentWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, account, mailbox, blob := testMailbox(t, ctx, db)
	const uidValidity = uint32(7001)
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 0, 0, 2, uidValidity); err != nil {
		t.Fatal(err)
	}

	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := userDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `UPDATE mailboxes SET updated_at = updated_at + 1 WHERE user_id = ? AND id = ?`, user.ID, mailbox.ID); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := db.CreateMessage(ctx, CreateMessage{
			UserID:          user.ID,
			AccountID:       account.ID,
			MailboxID:       mailbox.ID,
			BlobID:          blob.ID,
			MessageIDHeader: "<concurrent-writer@example.test>",
			Subject:         "Concurrent writer",
			FromAddr:        "sender@example.test",
			ToAddr:          user.Email,
			Date:            time.Now().UTC(),
			InternalDate:    time.Now().UTC(),
			UID:             1,
			UIDValidity:     int64(uidValidity),
			Size:            blob.Size,
			BlobPath:        blob.Path,
			BodyText:        "body",
		})
		result <- err
	}()
	<-started

	// Give CreateMessage time to reach the transaction blocked by this writer.
	time.Sleep(100 * time.Millisecond)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("CreateMessage returned while waiting for a concurrent writer: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("CreateMessage did not resume after the concurrent writer committed: %v", ctx.Err())
	}

	if _, err := db.GetMessageByUID(ctx, user.ID, account.ID, mailbox.ID, 1); err != nil {
		t.Fatalf("load created message: %v", err)
	}
}
