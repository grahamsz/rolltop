package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestRecoverMarkedSearchIndexesQuarantinesOnlyTargetAndQueuesEveryVisibleLocalFolder(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := store.OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUser(ctx, "stalled-search@example.test", "Stalled Search", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "healthy-search@example.test", "Healthy Search", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	blobStore := blob.New(dataDir)
	ownerAccount := createSearchRecoveryAccount(t, ctx, db, owner)
	otherAccount := createSearchRecoveryAccount(t, ctx, db, other)
	manual := createSearchRecoveryMessage(t, ctx, db, blobStore, owner, ownerAccount, "Manual Archive", "manual", true, 1)
	never := createSearchRecoveryMessage(t, ctx, db, blobStore, owner, ownerAccount, "Offline Archive", "never", true, 2)
	hidden := createSearchRecoveryMessage(t, ctx, db, blobStore, owner, ownerAccount, "Excluded", "never", false, 3)
	otherMessage := createSearchRecoveryMessage(t, ctx, db, blobStore, other, otherAccount, "INBOX", "auto", true, 1)

	searchRoot := filepath.Join(dataDir, "users")
	ownerIndex := filepath.Join(searchRoot, strconv.FormatInt(owner.ID, 10), "bleve")
	otherIndex := filepath.Join(searchRoot, strconv.FormatInt(other.ID, 10), "bleve")
	for _, path := range []string{ownerIndex, otherIndex} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ownerIndex, "owner"), []byte("stalled"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherIndex, "other"), []byte("healthy"), 0o600); err != nil {
		t.Fatal(err)
	}

	searchSvc, err := search.OpenPerUser(searchRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	if err := searchSvc.MarkSearchIndexRecoveryRequired(owner.ID); err != nil {
		t.Fatal(err)
	}
	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recoverMarkedSearchIndexes(ctx, db, searchSvc, searchRoot, users,
		time.Date(2026, 7, 17, 12, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recovered, []int64{owner.ID}) {
		t.Fatalf("recovered users = %v, want [%d]", recovered, owner.ID)
	}
	required, err := searchSvc.SearchIndexRecoveryRequired(owner.ID)
	if err != nil || required {
		t.Fatalf("owner recovery marker required=%t err=%v, want false, nil", required, err)
	}
	if _, err := os.Stat(ownerIndex); !os.IsNotExist(err) {
		t.Fatalf("owner live index still exists: %v", err)
	}
	quarantines, err := filepath.Glob(ownerIndex + ".quarantine-*")
	if err != nil || len(quarantines) != 1 {
		t.Fatalf("owner quarantines = %v, %v", quarantines, err)
	}
	if raw, err := os.ReadFile(filepath.Join(otherIndex, "other")); err != nil || string(raw) != "healthy" {
		t.Fatalf("other tenant index changed: %q, %v", raw, err)
	}

	pending, err := db.ListMessagesNeedingAttachmentIndex(ctx, owner.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	pendingIDs := make([]int64, 0, len(pending))
	for _, message := range pending {
		pendingIDs = append(pendingIDs, message.ID)
	}
	slices.Sort(pendingIDs)
	wantPending := []int64{manual.ID, never.ID}
	slices.Sort(wantPending)
	if !slices.Equal(pendingIDs, wantPending) {
		t.Fatalf("pending owner messages = %v, want manual/never %v", pendingIDs, wantPending)
	}
	assertSearchRecoveryMessagePreserved(t, ctx, db, owner.ID, hidden, false)
	assertSearchRecoveryMessagePreserved(t, ctx, db, other.ID, otherMessage, false)
	assertSearchRecoveryMessagePreserved(t, ctx, db, owner.ID, manual, true)
	assertSearchRecoveryMessagePreserved(t, ctx, db, owner.ID, never, true)

	runnerCtx, cancelRunner := context.WithCancel(ctx)
	defer cancelRunner()
	runner := syncer.NewRunnerWithContext(runnerCtx, &syncer.Service{
		Store: db, Blobs: blobStore, Search: searchSvc, PluginDir: t.TempDir(),
	})
	if !runner.StartAttachmentIndex(owner.ID) {
		t.Fatal("startup attachment-index worker did not start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		pending, err = db.ListMessagesNeedingAttachmentIndex(ctx, owner.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic local rebuild left pending messages: %+v", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count, err := searchSvc.CountUserMessages(ctx, owner.ID); err != nil || count != 0 {
		t.Fatalf("automatic recovery search documents = %d, %v; want 0, nil", count, err)
	}
	assertSearchRecoveryMessagePreserved(t, ctx, db, owner.ID, manual, false)
	assertSearchRecoveryMessagePreserved(t, ctx, db, owner.ID, never, false)
	assertSearchRecoveryMessagePreserved(t, ctx, db, owner.ID, hidden, false)
}

func TestRecoverMarkedSearchIndexesKeepsMarkerWhenQuarantineFailsAfterPendingWrite(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := store.OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "failed-search-recovery@example.test", "Failed Recovery", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	blobStore := blob.New(dataDir)
	account := createSearchRecoveryAccount(t, ctx, db, user)
	message := createSearchRecoveryMessage(t, ctx, db, blobStore, user, account, "Manual", "manual", true, 1)

	searchRoot := filepath.Join(dataDir, "users")
	indexPath := filepath.Join(searchRoot, strconv.FormatInt(user.ID, 10), "bleve")
	searchSvc, err := search.OpenPerUser(searchRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	if err := searchSvc.MarkSearchIndexRecoveryRequired(user.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = recoverMarkedSearchIndexes(ctx, db, searchSvc, searchRoot, []store.User{user}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "quarantine stalled search index") {
		t.Fatalf("recovery error = %v, want quarantine failure", err)
	}
	required, markerErr := searchSvc.SearchIndexRecoveryRequired(user.ID)
	if markerErr != nil || !required {
		t.Fatalf("recovery marker required=%t err=%v, want true, nil", required, markerErr)
	}
	assertSearchRecoveryMessagePreserved(t, ctx, db, user.ID, message, true)
	if raw, err := os.ReadFile(indexPath); err != nil || string(raw) != "not a directory" {
		t.Fatalf("failed recovery changed live index path: %q, %v", raw, err)
	}
}

func createSearchRecoveryAccount(t *testing.T, ctx context.Context, db *store.Store, user store.User) store.MailAccount {
	t.Helper()
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func createSearchRecoveryMessage(t *testing.T, ctx context.Context, db *store.Store, blobStore *blob.Store, user store.User, account store.MailAccount,
	mailboxName, syncMode string, includeInSearch bool, uid uint32,
) store.MessageRecord {
	t.Helper()
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, mailboxName)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSettings(ctx, user.ID, mailbox.ID, store.MailboxSettings{
		SyncMode: syncMode, Role: mailbox.Role, Icon: mailbox.Icon, ShowInSidebar: mailbox.ShowInSidebar,
		ShowInAllMail: mailbox.ShowInAllMail, IncludeInSearch: includeInSearch,
	}); err != nil {
		t.Fatal(err)
	}
	raw := []byte(fmt.Sprintf("From: Sender <sender@example.test>\r\nTo: %s\r\nSubject: Recovery message %d\r\nMessage-ID: <recovery-%d-%d@example.test>\r\n\r\npreserved local body\r\n", user.Email, uid, user.ID, uid))
	saved, err := blobStore.SaveRawMessage(user.ID, account.ID, mailbox.Name, uid, raw)
	if err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message", Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blobRecord.ID,
		MessageIDHeader: fmt.Sprintf("<recovery-%d-%d@example.test>", user.ID, uid),
		Subject:         fmt.Sprintf("Recovery message %d", uid), FromAddr: "sender@example.test", ToAddr: user.Email,
		Date: time.Now(), InternalDate: time.Now(), UID: uid, Size: saved.Size, BlobPath: saved.Path, BodyText: "preserved local body",
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

func assertSearchRecoveryMessagePreserved(t *testing.T, ctx context.Context, db *store.Store, userID int64, before store.MessageRecord, pending bool) {
	t.Helper()
	after, err := db.GetMessageForUser(ctx, userID, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AttachmentIndexedAt.IsZero() != pending {
		t.Fatalf("message %d pending=%t, want %t", before.ID, after.AttachmentIndexedAt.IsZero(), pending)
	}
	if after.Subject != before.Subject || after.BodyText != before.BodyText || after.BlobID != before.BlobID || after.BlobPath != before.BlobPath || after.UID != before.UID {
		t.Fatalf("message content changed: before=%+v after=%+v", before, after)
	}
	if _, err := db.GetBlobForUser(ctx, userID, before.BlobID); err != nil {
		t.Fatalf("message %d blob metadata changed: %v", before.ID, err)
	}
}
