package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type generationPrewarmFetcher struct {
	*fakeFetcher
	beforeSnapshot  func()
	beforeAscending func()
	beforeSparse    func(attempt int, uids []uint32)
	afterSparse     func(attempt int, uids []uint32)
	ascendingErr    error
	ascendingAfter  []uint32
	sparseAttempts  [][]uint32
	sparseErr       error
	sparseErrAt     int
	snapshotErr     error
	snapshotCalls   int
}

func (f *generationPrewarmFetcher) SnapshotMailboxUIDs(ctx context.Context, account store.MailAccount, mailbox string) (syncer.MailboxUIDSnapshot, error) {
	f.snapshotCalls++
	if f.beforeSnapshot != nil {
		f.beforeSnapshot()
	}
	if f.snapshotErr != nil {
		err := f.snapshotErr
		f.snapshotErr = nil
		return syncer.MailboxUIDSnapshot{}, err
	}
	status, err := f.MailboxStatus(ctx, account, mailbox)
	if err != nil {
		return syncer.MailboxUIDSnapshot{}, err
	}
	uids, err := f.UIDs(ctx, account, mailbox)
	if err != nil {
		return syncer.MailboxUIDSnapshot{}, err
	}
	return syncer.MailboxUIDSnapshot{UIDs: uids, UIDValidity: status.UIDValidity, UIDNext: status.UIDNext}, nil
}

func TestPendingGenerationRebuildNotifiesOnlyUIDsAtOrAboveDurableArrivalFloor(t *testing.T) {
	fixture := newGenerationPrewarmFixture(t, "generation-prewarm-arrival-floor@example.test")
	remote := generationPrewarmMessages(fixture.firstRaw, 205)
	base := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{fixture.user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	fetcher := &generationPrewarmFetcher{fakeFetcher: base}
	allMessages := generationPrewarmMessages(fixture.firstRaw, 207)
	insertedPrewarmArrival := false
	fetcher.beforeSnapshot = func() {
		if insertedPrewarmArrival {
			return
		}
		insertedPrewarmArrival = true
		base.messages[fixture.user.ID] = append(base.messages[fixture.user.ID], allMessages[205])
	}
	insertedAscendingArrival := false
	fetcher.beforeAscending = func() {
		if insertedAscendingArrival {
			return
		}
		insertedAscendingArrival = true
		floor, err := fixture.db.MailboxGenerationRebuildArrivalUIDFloor(fixture.ctx, fixture.user.ID,
			fixture.account.ID, fixture.mailbox.ID, 2)
		if err != nil || floor != 206 {
			t.Fatalf("durable arrival floor before ascending fetch=%d err=%v, want 206/nil", floor, err)
		}
		var pendingPrewarmArrival int
		if err := fixture.db.DB().QueryRowContext(fixture.ctx, `SELECT COUNT(*)
			FROM pending_inbox_arrivals arrival
			JOIN messages message ON message.user_id = arrival.user_id AND message.id = arrival.message_id
			WHERE arrival.user_id = ? AND message.uid = 206`, fixture.user.ID).Scan(&pendingPrewarmArrival); err != nil {
			t.Fatal(err)
		}
		if pendingPrewarmArrival != 1 {
			t.Fatalf("prewarmed post-floor pending arrivals=%d, want 1", pendingPrewarmArrival)
		}
		base.messages[fixture.user.ID] = append(base.messages[fixture.user.ID], allMessages[206])
	}
	fixture.service.Fetcher = fetcher
	fixture.service.ScheduleInboxArrival = func(int64, int64, time.Time) {}
	run, err := fixture.service.SyncUserAccountMailboxes(fixture.ctx, fixture.user.ID, fixture.account.ID, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if !insertedPrewarmArrival || !insertedAscendingArrival {
		t.Fatalf("delivery injection prewarm=%t ascending=%t, want both", insertedPrewarmArrival, insertedAscendingArrival)
	}
	if _, err := fixture.db.MailboxGenerationRebuildArrivalUIDFloor(fixture.ctx, fixture.user.ID,
		fixture.account.ID, fixture.mailbox.ID, 2); !store.IsNotFound(err) {
		t.Fatalf("completed rebuild retained arrival floor marker: %v", err)
	}
	created, _, err := fixture.service.FinalizePendingInboxArrivals(fixture.ctx, fixture.user.ID,
		fixture.account.ID, time.Now().UTC().Add(time.Hour))
	if err != nil || created != 2 {
		t.Fatalf("finalize post-floor arrivals created=%d err=%v, want 2/nil", created, err)
	}
	events, count, _, err := fixture.db.NewMailEventsAfter(fixture.ctx, fixture.user.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(events) != 2 {
		t.Fatalf("new-mail events=%+v count=%d, want exactly two post-floor arrivals", events, count)
	}
	for _, event := range events {
		message, err := fixture.db.GetMessageForUser(fixture.ctx, fixture.user.ID, event.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		if message.UID != 206 && message.UID != 207 {
			t.Fatalf("historical UID %d below floor emitted new-mail event", message.UID)
		}
	}
	run, err = fixture.db.GetSyncRunForUser(fixture.ctx, fixture.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.NewMessages != 2 {
		t.Fatalf("sync run new messages=%d, want 2", run.NewMessages)
	}
}

func TestLegacyZeroFloorMarkerInitializesOnceAcrossRecoveryResume(t *testing.T) {
	fixture := newGenerationPrewarmFixture(t, "generation-prewarm-legacy-floor@example.test")
	reset, err := fixture.service.ResetMailboxGenerationIfNeeded(fixture.ctx, fixture.user.ID,
		fixture.account, fixture.mailbox, 2, 1)
	if err != nil || !reset {
		t.Fatalf("generation reset=%t err=%v, want true/nil", reset, err)
	}
	// Only migration-era databases may contain a zero floor. Production reset
	// APIs reject it, so model that legacy row explicitly for recovery coverage.
	if _, err := fixture.db.DB().ExecContext(fixture.ctx, `UPDATE mailbox_generation_rebuilds
		SET arrival_uid_floor = 0 WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`,
		fixture.user.ID, fixture.account.ID, fixture.mailbox.ID); err != nil {
		t.Fatal(err)
	}
	floor, err := fixture.db.MailboxGenerationRebuildArrivalUIDFloor(fixture.ctx, fixture.user.ID,
		fixture.account.ID, fixture.mailbox.ID, 2)
	if err != nil || floor != 0 {
		t.Fatalf("legacy marker floor=%d err=%v, want 0/nil", floor, err)
	}

	remote := generationPrewarmMessages(fixture.firstRaw, 3)
	firstBase := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{fixture.user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	firstStop := errors.New("stop first legacy-floor resume")
	fixture.service.Fetcher = &generationPrewarmFetcher{fakeFetcher: firstBase, ascendingErr: firstStop}
	fixture.service.ScheduleInboxArrival = func(int64, int64, time.Time) {}
	if _, err := fixture.service.SyncUserAccountMailboxes(fixture.ctx, fixture.user.ID,
		fixture.account.ID, []string{"INBOX"}); !errors.Is(err, firstStop) {
		t.Fatalf("first resume error=%v, want %v", err, firstStop)
	}
	floor, err = fixture.db.MailboxGenerationRebuildArrivalUIDFloor(fixture.ctx, fixture.user.ID,
		fixture.account.ID, fixture.mailbox.ID, 2)
	if err != nil || floor != 4 {
		t.Fatalf("initialized legacy marker floor=%d err=%v, want 4/nil", floor, err)
	}

	remote = append(remote, generationPrewarmMessages(fixture.firstRaw, 4)[3])
	secondBase := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{fixture.user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	secondStop := errors.New("stop second legacy-floor resume")
	fixture.service.Fetcher = &generationPrewarmFetcher{fakeFetcher: secondBase, ascendingErr: secondStop}
	if _, err := fixture.service.SyncUserAccountMailboxes(fixture.ctx, fixture.user.ID,
		fixture.account.ID, []string{"INBOX"}); !errors.Is(err, secondStop) {
		t.Fatalf("second resume error=%v, want %v", err, secondStop)
	}
	floor, err = fixture.db.MailboxGenerationRebuildArrivalUIDFloor(fixture.ctx, fixture.user.ID,
		fixture.account.ID, fixture.mailbox.ID, 2)
	if err != nil || floor != 4 {
		t.Fatalf("resumed legacy marker floor=%d err=%v, want immutable 4/nil", floor, err)
	}
	var pendingUID4, pendingTotal int
	if err := fixture.db.DB().QueryRowContext(fixture.ctx, `SELECT COUNT(*)
		FROM pending_inbox_arrivals arrival
		JOIN messages message ON message.user_id = arrival.user_id AND message.id = arrival.message_id
		WHERE arrival.user_id = ? AND message.uid = 4`, fixture.user.ID).Scan(&pendingUID4); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.DB().QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM pending_inbox_arrivals
		WHERE user_id = ?`, fixture.user.ID).Scan(&pendingTotal); err != nil {
		t.Fatal(err)
	}
	if pendingUID4 != 1 || pendingTotal != 1 {
		t.Fatalf("pending arrivals UID4=%d total=%d, want 1/1 with no historical arrivals", pendingUID4, pendingTotal)
	}
}

func TestGenerationRebuildResumeRepairsStoredArrivalBeforeNetworkFetch(t *testing.T) {
	fixture := newGenerationPrewarmFixture(t, "generation-prewarm-crash-gap@example.test")
	if _, reset, err := fixture.db.ResetMailboxForRemoteGeneration(fixture.ctx, fixture.user.ID,
		fixture.account.ID, fixture.mailbox.ID, 2, 4); err != nil || !reset {
		t.Fatalf("generation reset=%t err=%v, want true/nil", reset, err)
	}
	remote := generationPrewarmMessages(fixture.firstRaw, 4)
	firstBase := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{fixture.user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	firstStop := errors.New("stop after storing post-floor arrival")
	fixture.service.Fetcher = &generationPrewarmFetcher{fakeFetcher: firstBase, ascendingErr: firstStop}
	fixture.service.ScheduleInboxArrival = func(int64, int64, time.Time) {}
	if _, err := fixture.service.SyncUserAccountMailboxes(fixture.ctx, fixture.user.ID,
		fixture.account.ID, []string{"INBOX"}); !errors.Is(err, firstStop) {
		t.Fatalf("first recovery error=%v, want %v", err, firstStop)
	}
	if _, err := fixture.db.DB().ExecContext(fixture.ctx, `DELETE FROM pending_inbox_arrivals
		WHERE user_id = ?`, fixture.user.ID); err != nil {
		t.Fatal(err)
	}
	assertNoGenerationPrewarmNotifications(t, fixture.ctx, fixture.db, fixture.user.ID)

	resumeBase := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{fixture.user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	resumeStop := errors.New("ascending recovery unavailable")
	resume := &generationPrewarmFetcher{
		fakeFetcher:  resumeBase,
		snapshotErr:  errors.New("sparse prewarm unavailable"),
		ascendingErr: resumeStop,
	}
	observedBeforeSnapshot := false
	resume.beforeSnapshot = func() {
		observedBeforeSnapshot = true
		var pendingUID4 int
		if err := fixture.db.DB().QueryRowContext(fixture.ctx, `SELECT COUNT(*)
			FROM pending_inbox_arrivals arrival
			JOIN messages message ON message.user_id = arrival.user_id AND message.id = arrival.message_id
			WHERE arrival.user_id = ? AND message.uid = 4`, fixture.user.ID).Scan(&pendingUID4); err != nil {
			t.Fatal(err)
		}
		if pendingUID4 != 1 {
			t.Fatalf("replayed UID 4 pending before network snapshot=%d, want 1", pendingUID4)
		}
	}
	fixture.service.Fetcher = resume
	if _, err := fixture.service.SyncUserAccountMailboxes(fixture.ctx, fixture.user.ID,
		fixture.account.ID, []string{"INBOX"}); !errors.Is(err, resumeStop) {
		t.Fatalf("resume error=%v, want %v", err, resumeStop)
	}
	if !observedBeforeSnapshot {
		t.Fatal("network prewarm began before observing local arrival replay")
	}
	var pendingTotal int
	if err := fixture.db.DB().QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM pending_inbox_arrivals
		WHERE user_id = ?`, fixture.user.ID).Scan(&pendingTotal); err != nil {
		t.Fatal(err)
	}
	if pendingTotal != 1 {
		t.Fatalf("pending arrivals after immediate ascending failure=%d, want exactly 1", pendingTotal)
	}
	assertNoGenerationPrewarmNotifications(t, fixture.ctx, fixture.db, fixture.user.ID)
}

func (f *generationPrewarmFetcher) FetchUIDsWithUIDValidity(
	ctx context.Context,
	account store.MailAccount,
	mailbox string,
	uids []uint32,
	expectedUIDValidity uint32,
	handle func(syncer.FetchedMessage) error,
) error {
	requested := append([]uint32(nil), uids...)
	f.sparseAttempts = append(f.sparseAttempts, requested)
	attempt := len(f.sparseAttempts)
	if f.beforeSparse != nil {
		f.beforeSparse(attempt, requested)
	}
	if f.sparseErr != nil && attempt == f.sparseErrAt {
		return f.sparseErr
	}
	err := f.fakeFetcher.FetchUIDsWithUIDValidity(ctx, account, mailbox, uids, expectedUIDValidity, handle)
	if err == nil && f.afterSparse != nil {
		f.afterSparse(attempt, requested)
	}
	return err
}

func TestLargeGenerationRebuildFinalRefreshCachesPostSnapshotArrival(t *testing.T) {
	fixture := newGenerationPrewarmFixture(t, "generation-prewarm-final-refresh@example.test")
	remote := generationPrewarmMessages(fixture.firstRaw, 600)
	allMessages := generationPrewarmMessages(fixture.firstRaw, 601)
	base := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{fixture.user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	fetcher := &generationPrewarmFetcher{fakeFetcher: base}
	injected := false
	fetcher.beforeSparse = func(attempt int, _ []uint32) {
		if attempt == 3 && !injected {
			injected = true
			base.messages[fixture.user.ID] = append(base.messages[fixture.user.ID], allMessages[600])
		}
	}
	observedBeforeMarkerRemoval := false
	fetcher.afterSparse = func(attempt int, uids []uint32) {
		if attempt != 5 {
			return
		}
		if len(uids) != 1 || uids[0] != 601 {
			t.Fatalf("final refresh UIDs=%v, want [601]", uids)
		}
		exists, err := fixture.db.MailboxGenerationRebuildExists(fixture.ctx, fixture.user.ID,
			fixture.account.ID, fixture.mailbox.ID)
		if err != nil || !exists {
			t.Fatalf("final refresh marker exists=%t err=%v, want true/nil", exists, err)
		}
		message, err := fixture.db.GetMessageByUID(fixture.ctx, fixture.user.ID, fixture.account.ID,
			fixture.mailbox.ID, 601)
		if err != nil {
			t.Fatalf("final refresh did not cache UID 601: %v", err)
		}
		var pending int
		if err := fixture.db.DB().QueryRowContext(fixture.ctx, `SELECT COUNT(*)
			FROM pending_inbox_arrivals WHERE user_id = ? AND message_id = ? AND classification = 'pending'`,
			fixture.user.ID, message.ID).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending != 1 {
			t.Fatalf("post-snapshot UID 601 pending arrivals=%d, want 1", pending)
		}
		observedBeforeMarkerRemoval = true
	}
	fixture.service.Fetcher = fetcher
	fixture.service.ScheduleInboxArrival = func(int64, int64, time.Time) {}
	run, err := fixture.service.SyncUserAccountMailboxes(fixture.ctx, fixture.user.ID,
		fixture.account.ID, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if !injected || !observedBeforeMarkerRemoval {
		t.Fatalf("post-snapshot injection=%t observed=%t, want true/true", injected, observedBeforeMarkerRemoval)
	}
	if len(fetcher.ascendingAfter) != 0 {
		t.Fatalf("large recovery used monolithic ascending fetches=%v", fetcher.ascendingAfter)
	}
	if len(fetcher.sparseAttempts) != 5 {
		t.Fatalf("large recovery sparse attempts=%v, want prewarm, two history batches, and final refresh", fetcher.sparseAttempts)
	}
	refreshed, err := fixture.db.GetMailbox(fixture.ctx, fixture.user.ID, fixture.account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.LastUID != 600 {
		t.Fatalf("final refresh advanced recovery checkpoint to %d, want fixed snapshot maximum 600", refreshed.LastUID)
	}
	if pending, err := fixture.db.MailboxGenerationRebuildPending(fixture.ctx, fixture.user.ID,
		fixture.account.ID, fixture.mailbox.ID, 2); err != nil || pending {
		t.Fatalf("completed large rebuild pending=%t err=%v, want false/nil", pending, err)
	}
	created, _, err := fixture.service.FinalizePendingInboxArrivals(fixture.ctx, fixture.user.ID,
		fixture.account.ID, time.Now().UTC().Add(time.Hour))
	if err != nil || created != 1 {
		t.Fatalf("post-snapshot arrival finalization created=%d err=%v, want 1/nil", created, err)
	}
	created, _, err = fixture.service.FinalizePendingInboxArrivals(fixture.ctx, fixture.user.ID,
		fixture.account.ID, time.Now().UTC().Add(time.Hour))
	if err != nil || created != 0 {
		t.Fatalf("idempotent post-snapshot finalization created=%d err=%v, want 0/nil", created, err)
	}
	run, err = fixture.db.GetSyncRunForUser(fixture.ctx, fixture.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.NewMessages != 1 {
		t.Fatalf("large rebuild run new messages=%d, want 1", run.NewMessages)
	}
}

func TestPendingGenerationRebuildFallsBackToAscendingAfterSnapshotTimeout(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "generation-prewarm-timeout@example.test", "Generation Prewarm Timeout", "hash", false)
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
	firstRaw := []byte(rawMessage("sender-1@example.test", "Generation message 1", "body 1", false))
	initialFetcher := &fakeFetcher{
		messages: map[int64][]syncer.FetchedMessage{user.ID: {{
			Mailbox: "INBOX", UID: 1, InternalDate: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC), Raw: firstRaw,
		}}},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 1},
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: initialFetcher}
	if _, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	remote := generationPrewarmMessages(firstRaw, 3)
	base := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	timeoutFetcher := &generationPrewarmFetcher{
		fakeFetcher: base,
		snapshotErr: context.DeadlineExceeded,
	}
	service.Fetcher = timeoutFetcher
	run, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"INBOX"})
	if err != nil {
		t.Fatalf("sync after prewarm snapshot timeout: %v", err)
	}
	if timeoutFetcher.snapshotCalls != 2 {
		t.Fatalf("snapshot calls=%d, want prewarm timeout plus reconciliation", timeoutFetcher.snapshotCalls)
	}
	if len(timeoutFetcher.fetchUIDCalls) != 0 {
		t.Fatalf("snapshot timeout unexpectedly attempted sparse fetches=%v", timeoutFetcher.fetchUIDCalls)
	}
	if len(timeoutFetcher.ascendingAfter) != 1 || timeoutFetcher.ascendingAfter[0] != 0 {
		t.Fatalf("ascending checkpoints=%v, want [0]", timeoutFetcher.ascendingAfter)
	}
	refreshed, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.LastUID != 3 {
		t.Fatalf("completed rebuild last_uid=%d, want 3", refreshed.LastUID)
	}
	pending, err := db.MailboxGenerationRebuildPending(ctx, user.ID, accountRecord.ID, mailbox.ID, 2)
	if err != nil || pending {
		t.Fatalf("completed rebuild pending=%v err=%v, want false/nil", pending, err)
	}
	var rows, distinctUIDs int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT uid)
		FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`,
		user.ID, accountRecord.ID, mailbox.ID).Scan(&rows, &distinctUIDs); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || distinctUIDs != 3 {
		t.Fatalf("completed rebuild rows=%d distinct_uids=%d, want 3/3", rows, distinctUIDs)
	}
	run, err = db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.NewMessages != 0 {
		t.Fatalf("completed rebuild emitted %d new-message notifications", run.NewMessages)
	}
	assertNoGenerationPrewarmNotifications(t, ctx, db, user.ID)
}

func TestPendingGenerationRebuildKeepsNewestPageWhenOlderPrewarmTimesOut(t *testing.T) {
	fixture := newGenerationPrewarmFixture(t, "generation-prewarm-older-timeout@example.test")
	remote := generationPrewarmMessages(fixture.firstRaw, 205)
	base := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{fixture.user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	timeoutFetcher := &generationPrewarmFetcher{
		fakeFetcher: base,
		sparseErr:   context.DeadlineExceeded,
		sparseErrAt: 2,
	}
	newestPageObserved := false
	timeoutFetcher.beforeSparse = func(attempt int, uids []uint32) {
		if attempt != 2 {
			return
		}
		newestPageObserved = true
		refreshed, err := fixture.db.GetMailbox(fixture.ctx, fixture.user.ID, fixture.account.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		if refreshed.LastUID != 0 {
			t.Fatalf("newest-page prewarm advanced last_uid to %d, want 0", refreshed.LastUID)
		}
		local, err := fixture.db.CountMessagesForMailbox(fixture.ctx, fixture.user.ID, fixture.mailbox.ID)
		if err != nil {
			t.Fatal(err)
		}
		if local != 50 {
			t.Fatalf("local rows before timed-out older phase=%d, want newest page of 50", local)
		}
		if _, err := fixture.db.GetMessageByUID(fixture.ctx, fixture.user.ID, fixture.account.ID, fixture.mailbox.ID, 205); err != nil {
			t.Fatalf("newest UID was not visible before older phase timeout: %v", err)
		}
		if _, err := fixture.db.GetMessageByUID(fixture.ctx, fixture.user.ID, fixture.account.ID, fixture.mailbox.ID, 155); !store.IsNotFound(err) {
			t.Fatalf("older UID was visible before its timed-out phase: %v", err)
		}
	}
	fixture.service.Fetcher = timeoutFetcher
	run, err := fixture.service.SyncUserAccountMailboxes(fixture.ctx, fixture.user.ID, fixture.account.ID, []string{"INBOX"})
	if err != nil {
		t.Fatalf("sync after older prewarm phase timeout: %v", err)
	}
	if !newestPageObserved {
		t.Fatal("older prewarm phase began without observing the newest page")
	}
	if len(timeoutFetcher.sparseAttempts) != 2 {
		t.Fatalf("sparse attempts=%v, want newest and older phases", timeoutFetcher.sparseAttempts)
	}
	newestPageUIDs := timeoutFetcher.sparseAttempts[0]
	if len(newestPageUIDs) != 50 || newestPageUIDs[0] != 156 || newestPageUIDs[len(newestPageUIDs)-1] != 205 {
		t.Fatalf("newest prewarm phase UIDs=%s, want range 156..205", describeUIDTestRange(newestPageUIDs))
	}
	olderUIDs := timeoutFetcher.sparseAttempts[1]
	if len(olderUIDs) != 150 || olderUIDs[0] != 6 || olderUIDs[len(olderUIDs)-1] != 155 {
		t.Fatalf("older prewarm phase UIDs=%s, want range 6..155", describeUIDTestRange(olderUIDs))
	}
	if len(timeoutFetcher.fetchUIDCalls) != 1 {
		t.Fatalf("completed sparse fetch calls=%v, want only newest phase", timeoutFetcher.fetchUIDCalls)
	}
	if len(timeoutFetcher.ascendingAfter) != 1 || timeoutFetcher.ascendingAfter[0] != 0 {
		t.Fatalf("ascending checkpoints=%v, want fallback from 0", timeoutFetcher.ascendingAfter)
	}
	refreshed, err := fixture.db.GetMailbox(fixture.ctx, fixture.user.ID, fixture.account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.LastUID != 205 {
		t.Fatalf("completed rebuild last_uid=%d, want 205", refreshed.LastUID)
	}
	pending, err := fixture.db.MailboxGenerationRebuildPending(fixture.ctx, fixture.user.ID, fixture.account.ID, fixture.mailbox.ID, 2)
	if err != nil || pending {
		t.Fatalf("completed rebuild pending=%v err=%v, want false/nil", pending, err)
	}
	var rows, distinctUIDs int
	if err := fixture.db.DB().QueryRowContext(fixture.ctx, `SELECT COUNT(*), COUNT(DISTINCT uid)
		FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`,
		fixture.user.ID, fixture.account.ID, fixture.mailbox.ID).Scan(&rows, &distinctUIDs); err != nil {
		t.Fatal(err)
	}
	if rows != 205 || distinctUIDs != 205 {
		t.Fatalf("completed rebuild rows=%d distinct_uids=%d, want 205/205", rows, distinctUIDs)
	}
	run, err = fixture.db.GetSyncRunForUser(fixture.ctx, fixture.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.NewMessages != 0 || run.MessagesStored != 205 {
		t.Fatalf("completed rebuild new=%d stored=%d, want 0/205", run.NewMessages, run.MessagesStored)
	}
	assertNoGenerationPrewarmNotifications(t, fixture.ctx, fixture.db, fixture.user.ID)
}

func (f *generationPrewarmFetcher) FetchMailboxWithUIDValidity(
	ctx context.Context,
	account store.MailAccount,
	mailbox string,
	afterUID, expectedUIDValidity uint32,
	handle func(syncer.FetchedMessage) error,
) error {
	f.ascendingAfter = append(f.ascendingAfter, afterUID)
	if f.beforeAscending != nil {
		f.beforeAscending()
	}
	if f.ascendingErr != nil {
		return f.ascendingErr
	}
	return f.fakeFetcher.FetchMailboxWithUIDValidity(ctx, account, mailbox, afterUID, expectedUIDValidity, handle)
}

func TestPendingGenerationRebuildPrewarmsNewestMessagesBeforeAscendingResume(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "generation-prewarm@example.test", "Generation Prewarm", "hash", false)
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
	firstRaw := []byte(rawMessage("sender-1@example.test", "Generation message 1", "body 1", false))
	initialFetcher := &fakeFetcher{
		messages: map[int64][]syncer.FetchedMessage{user.ID: {{
			Mailbox: "INBOX", UID: 1, InternalDate: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC), Raw: firstRaw,
		}}},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 1},
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: initialFetcher}
	if _, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	remote := generationPrewarmMessages(firstRaw, 205)
	base := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	interruptErr := errors.New("stop after newest-message prewarm")
	prewarmObserved := false
	newestPageObserved := false
	interrupted := &generationPrewarmFetcher{fakeFetcher: base, ascendingErr: interruptErr}
	interrupted.beforeSparse = func(attempt int, uids []uint32) {
		if attempt != 2 {
			return
		}
		newestPageObserved = true
		refreshed, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		if refreshed.LastUID != 0 {
			t.Fatalf("newest-page prewarm advanced last_uid to %d, want 0", refreshed.LastUID)
		}
		local, err := db.CountMessagesForMailbox(ctx, user.ID, mailbox.ID)
		if err != nil {
			t.Fatal(err)
		}
		if local != 50 {
			t.Fatalf("local rows before older prewarm phase=%d, want newest page of 50", local)
		}
		if _, err := db.GetMessageByUID(ctx, user.ID, accountRecord.ID, mailbox.ID, 205); err != nil {
			t.Fatalf("newest UID was not visible before older prewarm phase: %v", err)
		}
		if _, err := db.GetMessageByUID(ctx, user.ID, accountRecord.ID, mailbox.ID, 155); !store.IsNotFound(err) {
			t.Fatalf("older prewarm UID was visible before its phase: %v", err)
		}
	}
	interrupted.beforeAscending = func() {
		prewarmObserved = true
		refreshed, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		if refreshed.LastUID != 0 {
			t.Fatalf("prewarm advanced last_uid to %d, want 0", refreshed.LastUID)
		}
		local, err := db.CountMessagesForMailbox(ctx, user.ID, mailbox.ID)
		if err != nil {
			t.Fatal(err)
		}
		if local != 200 {
			t.Fatalf("local rows before ascending fetch=%d, want newest 200", local)
		}
		if _, err := db.GetMessageByUID(ctx, user.ID, accountRecord.ID, mailbox.ID, 205); err != nil {
			t.Fatalf("newest UID was not visible after prewarm: %v", err)
		}
		if _, err := db.GetMessageByUID(ctx, user.ID, accountRecord.ID, mailbox.ID, 5); !store.IsNotFound(err) {
			t.Fatalf("historical UID was fetched before ascending pass: %v", err)
		}
	}
	service.Fetcher = interrupted
	failedRun, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"INBOX"})
	if !errors.Is(err, interruptErr) {
		t.Fatalf("interrupted rebuild error=%v, want %v", err, interruptErr)
	}
	if !prewarmObserved {
		t.Fatal("ascending fetch began without observing the newest-message prewarm")
	}
	if !newestPageObserved {
		t.Fatal("older prewarm phase began without observing the newest page")
	}
	if interrupted.snapshotCalls != 1 || len(interrupted.fetchUIDCalls) != 2 {
		t.Fatalf("prewarm calls snapshot=%d sparse=%v, want one snapshot and two sparse phases", interrupted.snapshotCalls, interrupted.fetchUIDCalls)
	}
	newestPageUIDs := interrupted.fetchUIDCalls[0]
	if len(newestPageUIDs) != 50 || newestPageUIDs[0] != 156 || newestPageUIDs[len(newestPageUIDs)-1] != 205 {
		t.Fatalf("newest prewarm phase UIDs=%s, want range 156..205", describeUIDTestRange(newestPageUIDs))
	}
	olderUIDs := interrupted.fetchUIDCalls[1]
	if len(olderUIDs) != 150 || olderUIDs[0] != 6 || olderUIDs[len(olderUIDs)-1] != 155 {
		t.Fatalf("older prewarm phase UIDs=%s, want range 6..155", describeUIDTestRange(olderUIDs))
	}
	failedRun, err = db.GetSyncRunForUser(ctx, user.ID, failedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedRun.NewMessages != 0 || failedRun.MessagesStored != 200 {
		t.Fatalf("interrupted prewarm run new=%d stored=%d, want 0/200", failedRun.NewMessages, failedRun.MessagesStored)
	}
	pending, err := db.MailboxGenerationRebuildPending(ctx, user.ID, accountRecord.ID, mailbox.ID, 2)
	if err != nil || !pending {
		t.Fatalf("interrupted rebuild pending=%v err=%v, want true/nil", pending, err)
	}
	assertNoGenerationPrewarmNotifications(t, ctx, db, user.ID)

	resumeBase := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	resume := &generationPrewarmFetcher{fakeFetcher: resumeBase}
	service.Fetcher = resume
	completedRun, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resume.fetchUIDCalls) != 0 {
		t.Fatalf("resume refetched already prewarmed tail with sparse calls=%v", resume.fetchUIDCalls)
	}
	if len(resume.ascendingAfter) != 1 || resume.ascendingAfter[0] != 0 {
		t.Fatalf("resume ascending checkpoints=%v, want [0]", resume.ascendingAfter)
	}
	refreshed, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.LastUID != 205 {
		t.Fatalf("completed rebuild last_uid=%d, want 205", refreshed.LastUID)
	}
	pending, err = db.MailboxGenerationRebuildPending(ctx, user.ID, accountRecord.ID, mailbox.ID, 2)
	if err != nil || pending {
		t.Fatalf("completed rebuild pending=%v err=%v, want false/nil", pending, err)
	}
	var rows, distinctUIDs int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT uid)
		FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`,
		user.ID, accountRecord.ID, mailbox.ID).Scan(&rows, &distinctUIDs); err != nil {
		t.Fatal(err)
	}
	if rows != 205 || distinctUIDs != 205 {
		t.Fatalf("completed rebuild rows=%d distinct_uids=%d, want 205/205", rows, distinctUIDs)
	}
	completedRun, err = db.GetSyncRunForUser(ctx, user.ID, completedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completedRun.NewMessages != 0 {
		t.Fatalf("completed rebuild emitted %d new-message notifications", completedRun.NewMessages)
	}
	assertNoGenerationPrewarmNotifications(t, ctx, db, user.ID)
}

func generationPrewarmMessages(firstRaw []byte, count int) []syncer.FetchedMessage {
	messages := make([]syncer.FetchedMessage, 0, count)
	base := time.Date(2025, time.January, 1, 12, 0, 0, 0, time.UTC)
	for uid := 1; uid <= count; uid++ {
		raw := firstRaw
		if uid > 1 {
			raw = []byte(rawMessage(
				fmt.Sprintf("sender-%d@example.test", uid),
				fmt.Sprintf("Generation message %d", uid),
				fmt.Sprintf("body %d", uid),
				false,
			))
		}
		messages = append(messages, syncer.FetchedMessage{
			Mailbox: "INBOX", UID: uint32(uid), InternalDate: base.Add(time.Duration(uid) * time.Minute), Raw: raw,
		})
	}
	return messages
}

func assertNoGenerationPrewarmNotifications(t *testing.T, ctx context.Context, db *store.Store, userID int64) {
	t.Helper()
	events, count, cursor, err := db.NewMailEventsAfter(ctx, userID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || cursor != 0 || len(events) != 0 {
		t.Fatalf("generation prewarm emitted new-mail events=%+v count=%d cursor=%d", events, count, cursor)
	}
}

func describeUIDTestRange(uids []uint32) string {
	if len(uids) == 0 {
		return "[]"
	}
	return fmt.Sprintf("len=%d first=%d last=%d", len(uids), uids[0], uids[len(uids)-1])
}

type generationPrewarmFixture struct {
	ctx      context.Context
	db       *store.Store
	user     store.User
	account  store.MailAccount
	mailbox  store.Mailbox
	firstRaw []byte
	service  *syncer.Service
}

func newGenerationPrewarmFixture(t *testing.T, email string) generationPrewarmFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := db.CreateUser(ctx, email, "Generation Prewarm", "hash", false)
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
	firstRaw := []byte(rawMessage("sender-1@example.test", "Generation message 1", "body 1", false))
	initialFetcher := &fakeFetcher{
		messages: map[int64][]syncer.FetchedMessage{user.ID: {{
			Mailbox: "INBOX", UID: 1, InternalDate: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC), Raw: firstRaw,
		}}},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 1},
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: initialFetcher}
	if _, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	return generationPrewarmFixture{
		ctx: ctx, db: db, user: user, account: accountRecord, mailbox: mailbox,
		firstRaw: firstRaw, service: service,
	}
}
