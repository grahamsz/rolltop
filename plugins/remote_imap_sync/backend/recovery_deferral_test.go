package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"rolltop/backend/imapclient"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

type recoveryGateHost struct {
	plugins.BackendStartHost
	mu     sync.Mutex
	active map[int64]bool
}

type foregroundRecoveryHost struct {
	plugins.BackendStartHost
	mu              sync.Mutex
	active          map[int64]bool
	held            map[int64]bool
	beginCount      int
	releaseCount    int
	activateOnBegin bool
}

func newForegroundRecoveryHost() *foregroundRecoveryHost {
	return &foregroundRecoveryHost{active: map[int64]bool{}, held: map[int64]bool{}}
}

func (h *foregroundRecoveryHost) MailboxGenerationRecoveryActive(userID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active[userID]
}

func (h *foregroundRecoveryHost) BeginForegroundOperation(ctx context.Context, userID int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.beginCount++
	h.held[userID] = true
	if h.activateOnBegin {
		h.active[userID] = true
	}
	h.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.held, userID)
			h.releaseCount++
			h.mu.Unlock()
		})
	}, nil
}

func (h *foregroundRecoveryHost) snapshot(userID int64) (held bool, begins, releases int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held[userID], h.beginCount, h.releaseCount
}

func newRecoveryGateHost() *recoveryGateHost {
	return &recoveryGateHost{active: map[int64]bool{}}
}

func (h *recoveryGateHost) MailboxGenerationRecoveryActive(userID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active[userID]
}

func (h *recoveryGateHost) setActive(userID int64, active bool) {
	h.mu.Lock()
	if active {
		h.active[userID] = true
	} else {
		delete(h.active, userID)
	}
	h.mu.Unlock()
}

func TestRemoteSyncRecoveryWaitHonorsCancellationBeforeRunStarts(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	targetUIDValidity := uint32(401)
	insertRemoteRecoveryMarker(t, fixture, targetUIDValidity)

	ctx, cancel := context.WithCancel(context.Background())
	worker := &routineWorker{store: fixture.store, item: item, ctx: ctx, cancel: cancel}
	done := make(chan error, 1)
	go func() {
		done <- worker.runOnce("manual", 1)
	}()

	select {
	case err := <-done:
		t.Fatalf("deferred routine returned before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	var runCount int
	if err := fixture.db.QueryRowContext(context.Background(), `SELECT COUNT(*)
		FROM plugin_remote_imap_sync_runs WHERE user_id = ? AND routine_id = ?`,
		fixture.owner.ID, item.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	current, err := getRoutine(context.Background(), fixture.db, fixture.owner.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runCount != 0 || current.State != "queued" {
		t.Fatalf("deferred routine runs=%d state=%q, want 0/queued", runCount, current.State)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled recovery wait error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote sync recovery wait ignored cancellation")
	}
}

func TestRemoteSyncRunHoldsOptionalForegroundReservationThroughCopyWork(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	host := newForegroundRecoveryHost()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	searchHeld := false
	worker := &routineWorker{
		host: host, store: fixture.store, item: item, ctx: ctx, cancel: cancel,
		searchSourceMailbox: func(context.Context, store.MailAccount, string, uint32, time.Time) (imapclient.MailboxUIDSearch, error) {
			searchHeld, _, _ = host.snapshot(item.UserID)
			return imapclient.MailboxUIDSearch{UIDValidity: 501}, nil
		},
	}
	if err := worker.runOnce("manual", 1); err != nil {
		t.Fatal(err)
	}
	held, begins, releases := host.snapshot(item.UserID)
	if !searchHeld {
		t.Fatal("source copy work ran without the foreground reservation")
	}
	if held || begins != 1 || releases != 1 {
		t.Fatalf("foreground reservation held=%t begins=%d releases=%d, want false/1/1", held, begins, releases)
	}
}

func TestRemoteSyncReleasesForegroundReservationWhenRecoveryWinsAcquireRace(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	host := newForegroundRecoveryHost()
	host.activateOnBegin = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := &routineWorker{host: host, store: fixture.store, item: item, ctx: ctx, cancel: cancel}

	if err := worker.runOnce("manual", 1); !errors.Is(err, errRemoteSyncDeferredForRecovery) {
		t.Fatalf("acquire-race error=%v, want clean recovery deferral", err)
	}
	held, begins, releases := host.snapshot(item.UserID)
	if held || begins != 1 || releases != 1 {
		t.Fatalf("foreground reservation held=%t begins=%d releases=%d, want false/1/1", held, begins, releases)
	}
	var runCount int
	if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_remote_imap_sync_runs
		WHERE user_id = ? AND routine_id = ?`, item.UserID, item.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("acquire-race created %d runs before recovery deferral, want 0", runCount)
	}
}

func TestRemoteSyncChunkYieldRunsDestinationWorkBetweenForegroundTurns(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	host := newForegroundRecoveryHost()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := &routineWorker{host: host, store: fixture.store, item: item, ctx: ctx, cancel: cancel}
	reservation := remoteSyncForegroundReservation{worker: worker, userID: item.UserID}
	if err := reservation.Acquire(ctx); err != nil {
		t.Fatal(err)
	}

	destinationWorkCompleted := false
	err := reservation.Yield(ctx, 0, func() {
		held, begins, releases := host.snapshot(item.UserID)
		if held || begins != 1 || releases != 1 {
			t.Fatalf("destination work observed reservation held=%t begins=%d releases=%d, want false/1/1",
				held, begins, releases)
		}
		destinationWorkCompleted = true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !destinationWorkCompleted {
		t.Fatal("destination mailbox work did not complete between foreground turns")
	}
	held, begins, releases := host.snapshot(item.UserID)
	if !held || begins != 2 || releases != 1 {
		t.Fatalf("reacquired reservation held=%t begins=%d releases=%d, want true/2/1", held, begins, releases)
	}
	reservation.Release()
	held, begins, releases = host.snapshot(item.UserID)
	if held || begins != 2 || releases != 2 {
		t.Fatalf("final reservation held=%t begins=%d releases=%d, want false/2/2", held, begins, releases)
	}
}

func TestRemoteSyncRecoveryWaitDoesNotCrossTenantBoundary(t *testing.T) {
	fixture := newBackendFixture(t)
	insertRemoteRecoveryMarker(t, fixture, 402)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := waitForMailboxGenerationRecovery(ctx, fixture.store, fixture.other.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("another user's recovery marker blocked for %s", elapsed)
	}
}

func TestRemoteSyncRecoveryHostGateCoversReplayWithoutBlockingAnotherTenant(t *testing.T) {
	fixture := newBackendFixture(t)
	host := newRecoveryGateHost()
	host.setActive(fixture.owner.ID, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := &routineWorker{
		host: host, store: fixture.store, ctx: ctx, recoveryPollInterval: 2 * time.Millisecond,
	}
	done := make(chan error, 1)
	go func() {
		done <- worker.waitForMailboxGenerationRecovery(ctx, fixture.owner.ID)
	}()
	select {
	case err := <-done:
		t.Fatalf("host replay gate returned while active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := worker.waitForMailboxGenerationRecovery(ctx, fixture.other.ID); err != nil {
		t.Fatal(err)
	}
	host.setActive(fixture.owner.ID, false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote sync did not resume after the host replay gate cleared")
	}
}

func TestRemoteSyncRecoveryWaitResumesAfterMarkerClears(t *testing.T) {
	fixture := newBackendFixture(t)
	targetUIDValidity := uint32(403)
	insertRemoteRecoveryMarker(t, fixture, targetUIDValidity)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- waitForMailboxGenerationRecovery(ctx, fixture.store,
			fixture.owner.ID, 2*time.Millisecond)
	}()
	select {
	case err := <-done:
		t.Fatalf("recovery wait returned while marker remained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := fixture.store.FinalizeMailboxGenerationRebuild(context.Background(), fixture.owner.ID,
		fixture.ownerAccount.ID, fixture.ownerMailbox.ID, targetUIDValidity); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote sync did not resume after the recovery marker cleared")
	}
}

func TestRemoteSyncActiveRunDefersCleanlyAtChunkBoundary(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	ctx := context.Background()
	if err := beginRoutineRun(ctx, fixture.db, item); err != nil {
		t.Fatal(err)
	}
	runID, err := createRun(ctx, fixture.db, item.UserID, item.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	deferNow, err := shouldDeferRemoteSyncForRecovery(ctx, fixture.store, item.UserID, 25, 50)
	if err != nil {
		t.Fatal(err)
	}
	if deferNow {
		t.Fatal("run deferred before a recovery marker existed")
	}

	insertRemoteRecoveryMarker(t, fixture, 404)
	deferNow, err = shouldDeferRemoteSyncForRecovery(ctx, fixture.store, item.UserID, 24, 50)
	if err != nil {
		t.Fatal(err)
	}
	if deferNow {
		t.Fatal("run deferred between bounded chunk checks")
	}
	deferNow, err = shouldDeferRemoteSyncForRecovery(ctx, fixture.store, item.UserID, 25, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !deferNow {
		t.Fatal("active run did not notice the new recovery marker at its chunk boundary")
	}
	if err := updateRunProgress(ctx, fixture.db, item.UserID, runID, 25, 20, 5, 25); err != nil {
		t.Fatal(err)
	}
	if err := finishDeferredRoutineRun(ctx, fixture.db, item, runID); err != nil {
		t.Fatal(err)
	}

	current, err := getRoutine(ctx, fixture.db, item.UserID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := latestRun(ctx, fixture.db, item.UserID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "queued" || current.LastError != "" {
		t.Fatalf("deferred routine state=%q error=%q, want queued without failure", current.State, current.LastError)
	}
	if latest == nil || latest.Status != "deferred" || latest.Error != "" || latest.CompletedAt == 0 ||
		latest.Scanned != 25 || latest.Transferred != 20 || latest.Skipped != 5 {
		t.Fatalf("deferred run=%+v", latest)
	}
	view, err := presentRoutine(ctx, fixture.store, fixture.db, current)
	if err != nil {
		t.Fatal(err)
	}
	if view.ActiveRun != nil || view.LatestRun == nil || view.LatestRun.Status != "deferred" {
		t.Fatalf("deferred routine view active=%+v latest=%+v", view.ActiveRun, view.LatestRun)
	}
}

func TestRemoteSyncRechecksRecoveryAfterSearchBeforeDestinationSession(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	ctx := context.Background()
	pending, err := remoteSyncRecoveryPending(ctx, fixture.store, item.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("recovery marker existed before the simulated source search")
	}
	if err := beginRoutineRun(ctx, fixture.db, item); err != nil {
		t.Fatal(err)
	}
	runID, err := createRun(ctx, fixture.db, item.UserID, item.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}

	// The source search has returned UIDs, but no destination session has opened.
	insertRemoteRecoveryMarker(t, fixture, 406)
	pending, err = remoteSyncRecoveryPending(ctx, fixture.store, item.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("post-search recovery recheck did not see the new marker")
	}
	if err := finishDeferredRoutineRun(ctx, fixture.db, item, runID); err != nil {
		t.Fatal(err)
	}
	latest, err := latestRun(ctx, fixture.db, item.UserID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Status != "deferred" || latest.Error != "" || latest.Scanned != 0 ||
		latest.Transferred != 0 || latest.Skipped != 0 || latest.CompletedAt == 0 {
		t.Fatalf("pre-writer deferred run=%+v", latest)
	}
}

func TestRemoteSyncPostSearchGatePrecedesEmptyCompletion(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	if _, err := fixture.db.ExecContext(context.Background(), `UPDATE plugin_remote_imap_sync_routines
		SET source_uidvalidity = 900, last_source_uid = 12 WHERE user_id = ? AND id = ?`, item.UserID, item.ID); err != nil {
		t.Fatal(err)
	}
	item, err := getRoutine(context.Background(), fixture.db, item.UserID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	host := newRecoveryGateHost()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := &routineWorker{
		host: host, store: fixture.store, item: item, ctx: ctx, cancel: cancel,
		searchSourceMailbox: func(context.Context, store.MailAccount, string, uint32, time.Time) (imapclient.MailboxUIDSearch, error) {
			host.setActive(item.UserID, true)
			return imapclient.MailboxUIDSearch{UIDValidity: 901}, nil
		},
	}
	if err := worker.runOnce("manual", 1); !errors.Is(err, errRemoteSyncDeferredForRecovery) {
		t.Fatalf("post-search gate error=%v, want clean recovery deferral", err)
	}
	latest, err := latestRun(ctx, fixture.db, item.UserID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Status != "deferred" || latest.Error != "" || latest.Scanned != 0 || latest.CompletedAt == 0 {
		t.Fatalf("post-search empty run=%+v, want deferred before completion", latest)
	}
	current, err := getRoutine(ctx, fixture.db, item.UserID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.SourceUIDValidity != 900 || current.LastSourceUID != 12 {
		t.Fatalf("post-search gate adopted UID state before deferral: validity=%d last_uid=%d",
			current.SourceUIDValidity, current.LastSourceUID)
	}
}

func TestRemoteSyncFinalRecoveryCheckDefersShortRunCleanly(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	ctx := context.Background()
	if err := beginRoutineRun(ctx, fixture.db, item); err != nil {
		t.Fatal(err)
	}
	runID, err := createRun(ctx, fixture.db, item.UserID, item.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := updateRunProgress(ctx, fixture.db, item.UserID, runID, 12, 10, 2, 12); err != nil {
		t.Fatal(err)
	}
	insertRemoteRecoveryMarker(t, fixture, 902)
	pending, err := remoteSyncRecoveryPending(ctx, fixture.store, item.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("final recovery check missed marker after a sub-chunk fetch")
	}
	if err := finishDeferredRoutineRun(ctx, fixture.db, item, runID); err != nil {
		t.Fatal(err)
	}
	latest, err := latestRun(ctx, fixture.db, item.UserID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Status != "deferred" || latest.Error != "" || latest.Scanned != 12 ||
		latest.Transferred != 10 || latest.Skipped != 2 || latest.CompletedAt == 0 {
		t.Fatalf("short final deferred run=%+v", latest)
	}
}

func TestRemoteSyncMidChunkRecoveryDeferralQueuesDestinationRefreshFirst(t *testing.T) {
	events := make([]string, 0, 2)
	wantErr := errors.New("deferred")
	err := handleRemoteSyncFetchError(
		errRemoteSyncDeferredForRecovery,
		func() { events = append(events, "refresh") },
		func() error {
			events = append(events, "defer")
			return wantErr
		},
		func(error) error {
			t.Fatal("recovery deferral was treated as a failed run")
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("mid-chunk recovery deferral error=%v, want %v", err, wantErr)
	}
	if len(events) != 2 || events[0] != "refresh" || events[1] != "defer" {
		t.Fatalf("mid-chunk recovery events=%v, want [refresh defer]", events)
	}
}

func TestRemoteSyncIdleStopsForRecoveryAndRestartsAfterClear(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 4)
	stopped := make(chan struct{}, 4)
	worker := &routineWorker{
		store: fixture.store, item: item, ctx: ctx, cancel: cancel,
		recoveryPollInterval: 2 * time.Millisecond,
		watchSourceMailbox: func(ctx context.Context, _ store.MailAccount, _ string, _ func()) error {
			started <- struct{}{}
			<-ctx.Done()
			stopped <- struct{}{}
			return ctx.Err()
		},
	}
	done := make(chan struct{})
	go func() {
		worker.idleLoop()
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("source IDLE did not start")
	}

	targetUIDValidity := uint32(405)
	insertRemoteRecoveryMarker(t, fixture, targetUIDValidity)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("source IDLE was not canceled after recovery began")
	}
	select {
	case <-started:
		cancel()
		t.Fatal("source IDLE restarted while recovery remained pending")
	case <-time.After(20 * time.Millisecond):
	}

	if err := fixture.store.FinalizeMailboxGenerationRebuild(context.Background(), fixture.owner.ID,
		fixture.ownerAccount.ID, fixture.ownerMailbox.ID, targetUIDValidity); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("source IDLE did not restart after recovery cleared")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("restarted source IDLE did not honor cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("source IDLE recovery monitor leaked after cancellation")
	}
}

func TestRemoteSyncIdleStopsForHostReplayGateAndRestartsAfterClear(t *testing.T) {
	fixture := newBackendFixture(t)
	item := createRecoveryDeferredRoutine(t, fixture)
	host := newRecoveryGateHost()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 4)
	stopped := make(chan struct{}, 4)
	worker := &routineWorker{
		host: host, store: fixture.store, item: item, ctx: ctx, cancel: cancel,
		recoveryPollInterval: 2 * time.Millisecond,
		watchSourceMailbox: func(ctx context.Context, _ store.MailAccount, _ string, _ func()) error {
			started <- struct{}{}
			<-ctx.Done()
			stopped <- struct{}{}
			return ctx.Err()
		},
	}
	done := make(chan struct{})
	go func() {
		worker.idleLoop()
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("source IDLE did not start")
	}

	host.setActive(item.UserID, true)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("source IDLE was not canceled by the host replay gate")
	}
	select {
	case <-started:
		cancel()
		t.Fatal("source IDLE restarted while the host replay gate remained active")
	case <-time.After(20 * time.Millisecond):
	}

	host.setActive(item.UserID, false)
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("source IDLE did not restart after the host replay gate cleared")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("restarted source IDLE did not honor cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("host recovery IDLE monitor leaked after cancellation")
	}
}

func createRecoveryDeferredRoutine(t *testing.T, fixture backendFixture) routine {
	t.Helper()
	backend := &remoteIMAPSyncBackend{}
	item, err := backend.prepareRoutine(context.Background(), testAPIHost{}, fixture.store, fixture.db,
		fixture.owner.ID, 0, fixture.inputForOwner("app-password"))
	if err != nil {
		t.Fatal(err)
	}
	item, err = persistRoutine(context.Background(), fixture.db, item)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func insertRemoteRecoveryMarker(t *testing.T, fixture backendFixture, targetUIDValidity uint32) {
	t.Helper()
	now := time.Now().UTC().Unix()
	if _, err := fixture.db.ExecContext(context.Background(), `INSERT INTO mailbox_generation_rebuilds
		(user_id, account_id, mailbox_id, target_uid_validity, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, fixture.owner.ID, fixture.ownerAccount.ID,
		fixture.ownerMailbox.ID, targetUIDValidity, now, now); err != nil {
		t.Fatal(err)
	}
}
