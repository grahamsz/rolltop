package syncer_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestGenerationRebuildReschedulesRestoredPendingInboxArrival(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchService, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()
	user, err := db.CreateUser(ctx, "generation-arrival@example.test", "Generation Arrival", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	accountRecord, err := db.UpsertMailAccount(ctx, account(user.ID, encrypted))
	if err != nil {
		t.Fatal(err)
	}
	internalDate := time.Now().UTC().Truncate(time.Second)
	raw := []byte(rawMessage("generation-arrival@example.test", "Held arrival", "heldarrivalbody", false))
	fetcher := &fakeFetcher{
		messages: map[int64][]syncer.FetchedMessage{user.ID: {{
			Mailbox: "INBOX", UID: 5, InternalDate: internalDate, Raw: raw,
		}}},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 1},
	}
	service := &syncer.Service{
		Store: db, Blobs: blob.New(dir), Search: searchService, Fetcher: fetcher,
	}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := db.ListMessagesForMailbox(ctx, user.ID, mailbox.ID, 10, 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("initial messages=%+v err=%v", messages, err)
	}
	run, err := db.CreateSyncRun(ctx, user.ID, accountRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := store.MessageArrivalFingerprint(raw, messages[0].MessageIDHeader,
		messages[0].InternalDate, messages[0].Size)
	base := time.Now().UTC().Truncate(time.Second)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, run.ID, messages[0], fingerprint, base)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != store.ArrivalPending {
		t.Fatalf("initial classification=%q, want pending", decision.Arrival.Classification)
	}
	if err := db.UpdateMailboxSyncMode(ctx, user.ID, mailbox.ID, "never"); err != nil {
		t.Fatal(err)
	}
	if _, reset, err := db.ResetMailboxForRemoteGeneration(ctx, user.ID, accountRecord.ID, mailbox.ID, 2, 8); err != nil || !reset {
		t.Fatalf("crash-state generation reset reset=%v err=%v", reset, err)
	}
	schedules, err := db.ListPendingInboxArrivalSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 0 {
		t.Fatalf("active rebuild exposed arrival schedules=%+v", schedules)
	}
	rebuilds, err := db.ListPendingMailboxGenerationRebuilds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilds) != 1 || rebuilds[0].UserID != user.ID ||
		rebuilds[0].AccountID != accountRecord.ID || rebuilds[0].MailboxID != mailbox.ID ||
		rebuilds[0].MailboxName != "INBOX" {
		t.Fatalf("pending rebuilds=%+v, want exact tenant/account/mailbox", rebuilds)
	}

	fetcher.messages[user.ID] = []syncer.FetchedMessage{{
		Mailbox: "INBOX", UID: 7, InternalDate: internalDate, Raw: raw,
	}}
	fetcher.uidValidityByMailbox["inbox"] = 2
	runnerCtx, cancelRunner := context.WithCancel(ctx)
	defer cancelRunner()
	runner := syncer.NewRunnerWithContext(runnerCtx, service)
	type scheduledArrival struct {
		userID, accountID int64
		due               time.Time
	}
	scheduled := make(chan scheduledArrival, 1)
	runnerSchedule := service.ScheduleInboxArrival
	service.ScheduleInboxArrival = func(userID, accountID int64, due time.Time) {
		select {
		case scheduled <- scheduledArrival{userID: userID, accountID: accountID, due: due}:
		default:
		}
		runnerSchedule(userID, accountID, due)
	}
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	var restoredSchedule scheduledArrival
	select {
	case restoredSchedule = <-scheduled:
	case <-time.After(5 * time.Second):
		t.Fatal("startup recovery did not resume the marked mailbox and re-arm its arrival")
	}
	if restoredSchedule.userID != user.ID || restoredSchedule.accountID != accountRecord.ID ||
		!restoredSchedule.due.Equal(decision.Arrival.AvailableAt) {
		t.Fatalf("restored schedule=%+v, want %d/%d/%v", restoredSchedule,
			user.ID, accountRecord.ID, decision.Arrival.AvailableAt)
	}
	due, err := db.ListDueInboxArrivals(ctx, user.ID, accountRecord.ID, decision.Arrival.AvailableAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != decision.Arrival.ID ||
		due[0].SyncRunID != run.ID || due[0].Classification != store.ArrivalPending {
		t.Fatalf("restored live arrival=%+v, want original pending arrival", due)
	}
}
