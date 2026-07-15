package store

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"rolltop/backend/plugins"
)

func TestOpenServerSerializesEachUserDatabase(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	st, err := OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	user, err := st.CreateUser(ctx, "serialized@example.test", "Serialized", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	userDB, err := st.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := userDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("user database MaxOpenConnections = %d, want 1", got)
	}
	if got := st.DB().Stats().MaxOpenConnections; got <= 1 {
		t.Fatalf("system database MaxOpenConnections = %d, want concurrent system reads", got)
	}
}

func TestSplitUserDatabaseHandlesPluginWritesDuringCoreSyncAndSettingsReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate store test source")
	}
	manifests, err := plugins.LoadManifests(filepath.Join(filepath.Dir(sourceFile), "..", "..", "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenServerWithPluginManifests(filepath.Join(dataDir, "rolltop.db"), dataDir, manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	user, account, mailbox, blob := testMailbox(t, ctx, st)
	const uidValidity = uint32(8801)
	if err := st.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 0, 0, 100, uidValidity); err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "plugin-concurrency-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	userDB, err := st.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherDB, err := st.UserDB(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Unix()
	result, err := userDB.ExecContext(ctx, `INSERT INTO plugin_remote_imap_sync_routines
		(user_id, name, source_host, source_username, encrypted_source_password,
		 source_mailbox, destination_account_id, destination_mailbox_id, marker_secret,
		 created_at, updated_at)
		VALUES (?, 'migration', 'imap.source.example', 'source@example.test', 'encrypted',
		 'INBOX', ?, ?, 'marker-secret', ?, ?)`, user.ID, account.ID, mailbox.ID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	routineID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 1; i <= 200; i++ {
			tx, err := userDB.BeginTx(ctx, nil)
			if err != nil {
				errCh <- fmt.Errorf("begin plugin write %d: %w", i, err)
				return
			}
			_, err = tx.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines
				SET last_source_uid = ?, last_activity_at = ?, transferred_total = transferred_total + 1,
				    updated_at = ? WHERE user_id = ? AND id = ?`, i, now, now, user.ID, routineID)
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO plugin_remote_imap_sync_messages
					(user_id, routine_id, source_uidvalidity, source_uid, source_fingerprint,
					 marker, destination_uid, status, copied_at)
					VALUES (?, ?, 1, ?, ?, ?, ?, 'transferred', ?)`,
					user.ID, routineID, i, fmt.Sprintf("fingerprint-%d", i),
					fmt.Sprintf("marker-%d", i), i, now)
			}
			if err != nil {
				_ = tx.Rollback()
				errCh <- fmt.Errorf("plugin write %d: %w", i, err)
				return
			}
			if err := tx.Commit(); err != nil {
				errCh <- fmt.Errorf("commit plugin write %d: %w", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 1; i <= 40; i++ {
			uid := uint32(i)
			_, err := st.CreateMessage(ctx, CreateMessage{
				UserID:          user.ID,
				AccountID:       account.ID,
				MailboxID:       mailbox.ID,
				BlobID:          blob.ID,
				MessageIDHeader: fmt.Sprintf("<concurrent-%d@example.test>", i),
				Subject:         fmt.Sprintf("Concurrent %d", i),
				FromAddr:        "sender@example.test",
				ToAddr:          user.Email,
				Date:            time.Unix(now+int64(i), 0).UTC(),
				InternalDate:    time.Unix(now+int64(i), 0).UTC(),
				UID:             uid,
				UIDValidity:     int64(uidValidity),
				Size:            blob.Size,
				BlobPath:        blob.Path,
				BodyText:        "body",
			})
			if err != nil {
				errCh <- fmt.Errorf("core message write %d: %w", i, err)
				return
			}
			accounts, err := st.ListMailAccountsForUser(ctx, user.ID)
			if err != nil {
				errCh <- fmt.Errorf("settings read %d: %w", i, err)
				return
			}
			if len(accounts) != 1 || accounts[0].ID != account.ID {
				errCh <- fmt.Errorf("settings read %d returned accounts %+v", i, accounts)
				return
			}
		}
	}()
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("concurrent user database work timed out: %v", ctx.Err())
	}

	var pluginRows, messageRows int
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_remote_imap_sync_messages WHERE user_id = ?`, user.ID).Scan(&pluginRows); err != nil {
		t.Fatal(err)
	}
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE user_id = ?`, user.ID).Scan(&messageRows); err != nil {
		t.Fatal(err)
	}
	if pluginRows != 200 || messageRows != 40 {
		t.Fatalf("owner rows = plugin %d, messages %d; want 200 and 40", pluginRows, messageRows)
	}
	if err := otherDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_remote_imap_sync_messages WHERE user_id = ?`, other.ID).Scan(&pluginRows); err != nil {
		t.Fatal(err)
	}
	if err := otherDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE user_id = ?`, other.ID).Scan(&messageRows); err != nil {
		t.Fatal(err)
	}
	if pluginRows != 0 || messageRows != 0 {
		t.Fatalf("other tenant rows = plugin %d, messages %d; want 0 and 0", pluginRows, messageRows)
	}
}
