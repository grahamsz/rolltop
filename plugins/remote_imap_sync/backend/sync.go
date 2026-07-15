// File overview: Per-routine reconciliation, IDLE watching, and copy-only IMAP transfer engine.

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"rolltop/backend/imapclient"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

const (
	reconcileInterval                     = 30 * time.Second
	pollInterval                          = 15 * time.Minute
	runProgressWriteEvery                 = 25
	remoteSyncChunkSize                   = 25
	remoteSyncChunkYield                  = 500 * time.Millisecond
	mailboxGenerationRecoveryPollInterval = 2 * time.Second
)

var errRemoteSyncDeferredForRecovery = errors.New("remote IMAP sync deferred for mailbox recovery")

type workerKey struct {
	userID    int64
	routineID int64
}

type routineManager struct {
	host    plugins.BackendStartHost
	store   *store.Store
	fetcher *imapclient.Fetcher

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	workers     map[workerKey]*routineWorker
	pending     map[workerKey]string
	wg          sync.WaitGroup
}

func newRoutineManager(host plugins.BackendStartHost, st *store.Store, fetcher *imapclient.Fetcher) *routineManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &routineManager{
		host: host, store: st, fetcher: fetcher, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), workers: make(map[workerKey]*routineWorker),
		pending: make(map[workerKey]string),
	}
}

func (m *routineManager) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.reconcileLoop()
	}()
	if m.ctx.Err() == nil {
		m.Wake()
	}
}

func (m *routineManager) Stop() {
	m.cancel()
	m.lifecycleMu.Lock()
	m.mu.Lock()
	workers := make([]*routineWorker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.workers = make(map[workerKey]*routineWorker)
	m.mu.Unlock()
	for _, worker := range workers {
		worker.Stop()
	}
	m.lifecycleMu.Unlock()
	m.wg.Wait()
}

// MutateRoutine stops any active copy before changing its persisted settings.
// This prevents an edit, pause, or delete from racing a remote APPEND that still
// uses the prior source or destination configuration.
func (m *routineManager) MutateRoutine(userID, routineID int64, mutate func() error) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	key := workerKey{userID: userID, routineID: routineID}
	m.mu.Lock()
	worker := m.workers[key]
	delete(m.workers, key)
	delete(m.pending, key)
	m.mu.Unlock()
	if worker != nil {
		worker.Stop()
	}
	err := mutate()
	m.Wake()
	return err
}

func (m *routineManager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *routineManager) Trigger(userID, routineID int64, trigger string) bool {
	key := workerKey{userID: userID, routineID: routineID}
	m.mu.Lock()
	worker := m.workers[key]
	if worker == nil {
		m.pending[key] = trigger
	}
	m.mu.Unlock()
	if worker != nil {
		worker.Trigger(trigger)
		return true
	}
	m.Wake()
	return false
}

func (m *routineManager) reconcileLoop() {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		if err := m.reconcile(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("remote imap sync reconciliation: %v", err)
		}
		select {
		case <-m.ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

func (m *routineManager) reconcile() error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return err
	}
	users, err := m.store.ListUsers(m.ctx)
	if err != nil {
		return err
	}
	desired := make(map[workerKey]routine)
	for _, user := range users {
		if m.ctx.Err() != nil {
			return m.ctx.Err()
		}
		db, err := m.store.UserDB(m.ctx, user.ID)
		if err != nil {
			continue
		}
		items, err := listRoutines(m.ctx, db, user.ID, true)
		if err != nil {
			continue
		}
		for _, item := range items {
			desired[workerKey{userID: user.ID, routineID: item.ID}] = item
		}
	}

	var stop []*routineWorker
	var start []*routineWorker
	m.mu.Lock()
	for key, worker := range m.workers {
		item, ok := desired[key]
		if !ok || !worker.SameVersion(item) {
			delete(m.workers, key)
			stop = append(stop, worker)
		}
	}
	for key, item := range desired {
		if m.workers[key] != nil {
			continue
		}
		worker := newRoutineWorker(m.ctx, m.host, m.store, m.fetcher, item)
		m.workers[key] = worker
		start = append(start, worker)
	}
	pending := make(map[workerKey]string, len(m.pending))
	for key, trigger := range m.pending {
		pending[key] = trigger
		delete(m.pending, key)
	}
	m.mu.Unlock()

	for _, worker := range stop {
		worker.Stop()
	}
	for _, worker := range start {
		worker.Start()
	}
	for key, trigger := range pending {
		m.mu.Lock()
		worker := m.workers[key]
		m.mu.Unlock()
		if worker != nil {
			worker.Trigger(trigger)
		}
	}
	return nil
}

type routineWorker struct {
	host    plugins.BackendStartHost
	store   *store.Store
	fetcher *imapclient.Fetcher
	item    routine

	ctx      context.Context
	cancel   context.CancelFunc
	triggers chan string
	wg       sync.WaitGroup
	stopOnce sync.Once

	recoveryPollInterval time.Duration
	watchSourceMailbox   func(context.Context, store.MailAccount, string, func()) error
	searchSourceMailbox  func(context.Context, store.MailAccount, string, uint32, time.Time) (imapclient.MailboxUIDSearch, error)
}

func newRoutineWorker(parent context.Context, host plugins.BackendStartHost, st *store.Store, fetcher *imapclient.Fetcher, item routine) *routineWorker {
	ctx, cancel := context.WithCancel(parent)
	return &routineWorker{host: host, store: st, fetcher: fetcher, item: item,
		ctx: ctx, cancel: cancel, triggers: make(chan string, 1)}
}

func (w *routineWorker) SameVersion(item routine) bool {
	return w.item.ID == item.ID && w.item.Enabled == item.Enabled &&
		w.item.SourceHost == item.SourceHost && w.item.SourcePort == item.SourcePort &&
		w.item.SourceUsername == item.SourceUsername &&
		w.item.EncryptedSourcePassword == item.EncryptedSourcePassword &&
		w.item.SourceUseTLS == item.SourceUseTLS && w.item.SourceMailbox == item.SourceMailbox &&
		w.item.DestinationAccountID == item.DestinationAccountID &&
		w.item.DestinationMailboxID == item.DestinationMailboxID &&
		w.item.AfterDate.Equal(item.AfterDate) && w.item.MarkerSecret == item.MarkerSecret
}

func (w *routineWorker) Start() {
	w.wg.Add(2)
	go func() {
		defer w.wg.Done()
		w.runLoop()
	}()
	go func() {
		defer w.wg.Done()
		w.idleLoop()
	}()
	w.Trigger("startup")
}

func (w *routineWorker) Stop() {
	w.stopOnce.Do(w.cancel)
	w.wg.Wait()
}

func (w *routineWorker) Trigger(trigger string) {
	if trigger == "" {
		trigger = "scheduled"
	}
	select {
	case w.triggers <- trigger:
	default:
	}
}

func (w *routineWorker) runLoop() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	failures := 0
	for {
		var trigger string
		select {
		case <-w.ctx.Done():
			return
		case trigger = <-w.triggers:
		case <-ticker.C:
			trigger = "scheduled"
		}
		err := w.runOnce(trigger, failures+1)
		if err == nil {
			failures = 0
			continue
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, errRemoteSyncDeferredForRecovery) {
			failures = 0
			w.Trigger("recovery")
			continue
		}
		failures++
		delay := retryDelay(failures)
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(delay):
			w.Trigger("retry")
		}
	}
}

func (w *routineWorker) idleLoop() {
	failures := 0
	for {
		if w.ctx.Err() != nil {
			return
		}
		if err := w.waitForMailboxGenerationRecovery(w.ctx, w.item.UserID); err != nil {
			if errors.Is(err, context.Canceled) || w.ctx.Err() != nil {
				return
			}
			failures++
			log.Printf("remote imap sync idle recovery gate user_id=%d routine_id=%d: %s",
				w.item.UserID, w.item.ID, sanitizeRemoteError(err))
			if waitForRemoteSyncChunk(w.ctx, retryDelay(failures)) != nil {
				return
			}
			continue
		}
		source := sourceAccount(w.item)
		err := w.watchMailboxUntilRecovery(source, func() {
			w.Trigger("idle")
		})
		if errors.Is(err, context.Canceled) || w.ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRemoteSyncDeferredForRecovery) {
			failures = 0
			continue
		}
		failures++
		log.Printf("remote imap sync idle user_id=%d routine_id=%d: %s",
			w.item.UserID, w.item.ID, sanitizeRemoteError(err))
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(retryDelay(failures)):
		}
	}
}

func (w *routineWorker) runOnce(trigger string, failureAttempt int) error {
	db, err := w.store.UserDB(w.ctx, w.item.UserID)
	if err != nil {
		return err
	}
	item, err := getRoutine(w.ctx, db, w.item.UserID, w.item.ID)
	if err != nil {
		return err
	}
	if !item.Enabled {
		return nil
	}
	if err := w.waitForMailboxGenerationRecovery(w.ctx, item.UserID); err != nil {
		return err
	}
	if err := beginRoutineRun(w.ctx, db, item); err != nil {
		return err
	}
	runID, err := createRun(w.ctx, db, item.UserID, item.ID, trigger)
	if err != nil {
		return err
	}
	var scanned, transferred, skipped int64
	var currentUID uint32
	var persistedScanned int64
	persistProgress := func(ctx context.Context) error {
		if !runProgressNeedsFlush(scanned, persistedScanned) {
			return nil
		}
		if err := updateRunProgress(ctx, db, item.UserID, runID, scanned, transferred, skipped, currentUID); err != nil {
			return err
		}
		persistedScanned = scanned
		return nil
	}
	fail := func(runErr error) error {
		_ = persistProgress(context.Background())
		if errors.Is(runErr, context.Canceled) || w.ctx.Err() != nil {
			_ = finishRun(context.Background(), db, item.UserID, runID, "canceled", "")
			return runErr
		}
		message := sanitizeRemoteError(runErr)
		_ = finishRun(context.Background(), db, item.UserID, runID, "failed", message)
		_ = failRoutineRun(context.Background(), db, item, message, time.Now().UTC().Add(retryDelay(failureAttempt)))
		return runErr
	}
	deferForRecovery := func() error {
		if progressErr := persistProgress(context.Background()); progressErr != nil {
			return fail(progressErr)
		}
		if deferErr := finishDeferredRoutineRun(context.Background(), db, item, runID); deferErr != nil {
			return fail(deferErr)
		}
		return errRemoteSyncDeferredForRecovery
	}
	destinationMailbox, err := w.store.GetMailboxForUser(w.ctx, item.UserID, item.DestinationMailboxID)
	if err != nil || destinationMailbox.AccountID != item.DestinationAccountID {
		return fail(fmt.Errorf("destination mailbox is no longer available"))
	}
	destinationAccount, err := w.store.GetMailAccountForUser(w.ctx, item.UserID, item.DestinationAccountID)
	if err != nil {
		return fail(fmt.Errorf("destination account is no longer available"))
	}
	if sameIMAPEndpoint(item, destinationAccount) {
		return fail(fmt.Errorf("source and destination now resolve to the same IMAP account"))
	}
	search, err := w.searchMailboxUIDsSince(w.ctx, sourceAccount(item), item.SourceMailbox, item.LastSourceUID, item.AfterDate)
	if err != nil {
		return fail(err)
	}
	pendingRecovery, err := w.remoteSyncRecoveryPending(w.ctx, item.UserID)
	if err != nil {
		return fail(err)
	}
	if pendingRecovery {
		return deferForRecovery()
	}
	if item.SourceUIDValidity != 0 && item.SourceUIDValidity != search.UIDValidity {
		if err := adoptUIDValidity(w.ctx, db, item.UserID, item.ID, search.UIDValidity); err != nil {
			return fail(err)
		}
		item.SourceUIDValidity = search.UIDValidity
		item.LastSourceUID = 0
		search, err = w.searchMailboxUIDsSince(w.ctx, sourceAccount(item), item.SourceMailbox, 0, item.AfterDate)
		if err != nil {
			return fail(err)
		}
		pendingRecovery, err = w.remoteSyncRecoveryPending(w.ctx, item.UserID)
		if err != nil {
			return fail(err)
		}
		if pendingRecovery {
			return deferForRecovery()
		}
	}
	if search.UIDValidity == 0 {
		return fail(fmt.Errorf("source mailbox did not report UIDVALIDITY"))
	}
	if len(search.UIDs) == 0 {
		if err := w.ctx.Err(); err != nil {
			return fail(err)
		}
		if err := finishRun(w.ctx, db, item.UserID, runID, "completed", ""); err != nil {
			return fail(err)
		}
		return completeRoutineRun(w.ctx, db, item)
	}
	writer, err := w.fetcher.OpenSyncDestinationSession(w.ctx, destinationAccount, destinationMailbox.Name)
	if err != nil {
		return fail(err)
	}
	defer writer.Close()
	pendingRecovery, err = w.remoteSyncRecoveryPending(w.ctx, item.UserID)
	if err != nil {
		return fail(err)
	}
	if pendingRecovery {
		return deferForRecovery()
	}
	var unrefreshedCopies bool
	flushDestinationRefresh := func() {
		if !unrefreshedCopies {
			return
		}
		w.queueDestinationRefresh(item, destinationAccount, destinationMailbox)
		unrefreshedCopies = false
	}
	totalUIDs := int64(len(search.UIDs))
	err = w.fetcher.FetchUIDs(w.ctx, sourceAccount(item), item.SourceMailbox, search.UIDs, func(message syncer.FetchedMessage) error {
		if w.mailboxGenerationRecoveryActive(item.UserID) {
			return errRemoteSyncDeferredForRecovery
		}
		scanned++
		currentUID = message.UID
		fingerprint := messageFingerprint(message.Raw)
		handled, err := sourceMessageAlreadyHandled(w.ctx, db, item, search.UIDValidity, message, fingerprint)
		if err != nil {
			return err
		}
		marker, err := imapclient.MessageSyncMarker(item.MarkerSecret, search.UIDValidity, message.UID)
		if err != nil {
			return err
		}
		if handled {
			skipped++
			if err := recordHandledMessage(w.ctx, db, item, search.UIDValidity, message.UID,
				fingerprint, marker, 0, "skipped"); err != nil {
				return err
			}
		} else {
			syncedAt := time.Now().UTC().Truncate(time.Second)
			appended, err := writer.AppendMessageWithSyncMarkerAt(w.ctx, message.Raw, marker, syncedAt,
				message.InternalDate, message.Flags)
			if err != nil {
				return err
			}
			transferred++
			unrefreshedCopies = true
			if headerTime, ok := imapclient.SyncTimestampForMarker(appended.Raw, marker); ok {
				syncedAt = headerTime
			}
			destinationSHA256 := ""
			if len(appended.Raw) > 0 {
				destinationSHA256 = messageFingerprint(appended.Raw)
			}
			if err := recordHandledMessageAt(w.ctx, db, item, search.UIDValidity, message.UID,
				fingerprint, marker, appended.UID, "transferred", syncedAt, writer.UIDValidity(), destinationSHA256); err != nil {
				return err
			}
			if transferred == 1 {
				w.queueDestinationRefresh(item, destinationAccount, destinationMailbox)
				unrefreshedCopies = false
			}
		}
		if shouldWriteRunProgress(scanned) {
			if err := persistProgress(w.ctx); err != nil {
				return err
			}
		}
		if shouldYieldRemoteSync(scanned, totalUIDs) {
			flushDestinationRefresh()
			pendingRecovery, err := w.remoteSyncRecoveryPending(w.ctx, item.UserID)
			if err != nil {
				return err
			}
			if pendingRecovery {
				return errRemoteSyncDeferredForRecovery
			}
			return waitForRemoteSyncChunk(w.ctx, remoteSyncChunkYield)
		}
		return nil
	})
	if err != nil {
		return handleRemoteSyncFetchError(err, flushDestinationRefresh, deferForRecovery, fail)
	}
	pendingRecovery, err = w.remoteSyncRecoveryPending(w.ctx, item.UserID)
	if err != nil {
		return fail(err)
	}
	if pendingRecovery {
		flushDestinationRefresh()
		return deferForRecovery()
	}
	if err := w.ctx.Err(); err != nil {
		return fail(err)
	}
	if runProgressNeedsFlush(scanned, persistedScanned) {
		if err := persistProgress(w.ctx); err != nil {
			return fail(err)
		}
	}
	flushDestinationRefresh()
	if err := finishRun(w.ctx, db, item.UserID, runID, "completed", ""); err != nil {
		return fail(err)
	}
	if err := completeRoutineRun(w.ctx, db, item); err != nil {
		return err
	}
	return nil
}

func handleRemoteSyncFetchError(fetchErr error, flushDestinationRefresh func(), deferForRecovery func() error, fail func(error) error) error {
	if errors.Is(fetchErr, errRemoteSyncDeferredForRecovery) {
		flushDestinationRefresh()
		return deferForRecovery()
	}
	return fail(fetchErr)
}

func shouldWriteRunProgress(scanned int64) bool {
	if scanned <= 0 {
		return false
	}
	return scanned == 1 || scanned%runProgressWriteEvery == 0
}

func runProgressNeedsFlush(scanned, persistedScanned int64) bool {
	return scanned > 0 && scanned > persistedScanned
}

func shouldYieldRemoteSync(scanned, total int64) bool {
	return scanned > 0 && scanned%remoteSyncChunkSize == 0 && scanned < total
}

func shouldDeferRemoteSyncForRecovery(ctx context.Context, st *store.Store, userID, scanned, total int64) (bool, error) {
	if !shouldYieldRemoteSync(scanned, total) {
		return false, nil
	}
	return remoteSyncRecoveryPending(ctx, st, userID)
}

func remoteSyncRecoveryPending(ctx context.Context, st *store.Store, userID int64) (bool, error) {
	if st == nil {
		return false, errors.New("remote IMAP sync store is not available")
	}
	return st.HasPendingMailboxGenerationRebuildsForUser(ctx, userID)
}

func (w *routineWorker) mailboxGenerationRecoveryActive(userID int64) bool {
	host, ok := w.host.(plugins.MailboxGenerationRecoveryHost)
	return ok && host.MailboxGenerationRecoveryActive(userID)
}

func (w *routineWorker) searchMailboxUIDsSince(ctx context.Context, account store.MailAccount, mailbox string, afterUID uint32, since time.Time) (imapclient.MailboxUIDSearch, error) {
	if w.searchSourceMailbox != nil {
		return w.searchSourceMailbox(ctx, account, mailbox, afterUID, since)
	}
	if w.fetcher == nil {
		return imapclient.MailboxUIDSearch{}, errors.New("remote IMAP sync fetcher is not available")
	}
	return w.fetcher.SearchMailboxUIDsSince(ctx, account, mailbox, afterUID, since)
}

func (w *routineWorker) remoteSyncRecoveryPending(ctx context.Context, userID int64) (bool, error) {
	if w.mailboxGenerationRecoveryActive(userID) {
		return true, nil
	}
	return remoteSyncRecoveryPending(ctx, w.store, userID)
}

func waitForRemoteSyncChunk(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitForMailboxGenerationRecovery(ctx context.Context, st *store.Store, userID int64, pollInterval time.Duration) error {
	return waitForMailboxGenerationRecoveryCheck(ctx, userID, pollInterval, func(ctx context.Context, userID int64) (bool, error) {
		return remoteSyncRecoveryPending(ctx, st, userID)
	})
}

func waitForMailboxGenerationRecoveryCheck(ctx context.Context, userID int64, pollInterval time.Duration, pending func(context.Context, int64) (bool, error)) error {
	if pending == nil {
		return errors.New("remote IMAP sync recovery gate is not available")
	}
	if pollInterval <= 0 {
		pollInterval = mailboxGenerationRecoveryPollInterval
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		isPending, err := pending(ctx, userID)
		if err != nil {
			return err
		}
		if !isPending {
			return nil
		}
		if err := waitForRemoteSyncChunk(ctx, pollInterval); err != nil {
			return err
		}
	}
}

func waitForMailboxGenerationRecoveryStart(ctx context.Context, st *store.Store, userID int64, pollInterval time.Duration) error {
	return waitForMailboxGenerationRecoveryStartCheck(ctx, userID, pollInterval, func(ctx context.Context, userID int64) (bool, error) {
		return remoteSyncRecoveryPending(ctx, st, userID)
	})
}

func waitForMailboxGenerationRecoveryStartCheck(ctx context.Context, userID int64, pollInterval time.Duration, pending func(context.Context, int64) (bool, error)) error {
	if pending == nil {
		return errors.New("remote IMAP sync recovery gate is not available")
	}
	if pollInterval <= 0 {
		pollInterval = mailboxGenerationRecoveryPollInterval
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		isPending, err := pending(ctx, userID)
		if err != nil {
			return err
		}
		if isPending {
			return nil
		}
		if err := waitForRemoteSyncChunk(ctx, pollInterval); err != nil {
			return err
		}
	}
}

func (w *routineWorker) waitForMailboxGenerationRecovery(ctx context.Context, userID int64) error {
	return waitForMailboxGenerationRecoveryCheck(ctx, userID, w.recoveryPollEvery(), w.remoteSyncRecoveryPending)
}

func (w *routineWorker) recoveryPollEvery() time.Duration {
	if w.recoveryPollInterval > 0 {
		return w.recoveryPollInterval
	}
	return mailboxGenerationRecoveryPollInterval
}

func (w *routineWorker) watchMailbox(ctx context.Context, source store.MailAccount, mailbox string, onChange func()) error {
	if w.watchSourceMailbox != nil {
		return w.watchSourceMailbox(ctx, source, mailbox, onChange)
	}
	if w.fetcher == nil {
		return errors.New("remote IMAP sync fetcher is not available")
	}
	return w.fetcher.WatchMailbox(ctx, source, mailbox, onChange)
}

func (w *routineWorker) watchMailboxUntilRecovery(source store.MailAccount, onChange func()) error {
	watchCtx, cancel := context.WithCancel(w.ctx)
	monitorDone := make(chan error, 1)
	go func() {
		err := waitForMailboxGenerationRecoveryStartCheck(watchCtx, w.item.UserID, w.recoveryPollEvery(), w.remoteSyncRecoveryPending)
		cancel()
		monitorDone <- err
	}()

	watchErr := w.watchMailbox(watchCtx, source, w.item.SourceMailbox, onChange)
	cancel()
	monitorErr := <-monitorDone
	if w.ctx.Err() != nil {
		return w.ctx.Err()
	}
	if monitorErr == nil {
		return errRemoteSyncDeferredForRecovery
	}
	if !errors.Is(monitorErr, context.Canceled) {
		return monitorErr
	}
	return watchErr
}

func finishDeferredRoutineRun(ctx context.Context, db *sql.DB, item routine, runID int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_runs
		SET status = 'deferred', error = '', completed_at = ?
		WHERE user_id = ? AND id = ?`, now, item.UserID, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines
		SET state = 'queued', last_error = '', next_retry_at = 0, updated_at = ?
		WHERE user_id = ? AND id = ? AND enabled = 1`, now, item.UserID, item.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func sourceMessageAlreadyHandled(ctx context.Context, db *sql.DB, item routine, uidValidity uint32, message syncer.FetchedMessage, fingerprint string) (bool, error) {
	if imapclient.HasSyncMarkerForTask(message.Raw, item.MarkerSecret) {
		return true, nil
	}
	return messageAlreadyHandled(ctx, db, item.UserID, item.ID, uidValidity, message.UID, fingerprint)
}

func (w *routineWorker) queueDestinationRefresh(item routine, account store.MailAccount, mailbox store.Mailbox) {
	refreshHost, ok := w.host.(plugins.AccountMailboxSyncHost)
	if !ok {
		return
	}
	if err := refreshHost.QueueAccountMailboxSync(w.ctx, item.UserID, account.ID, mailbox.Name); err != nil && w.ctx.Err() == nil {
		log.Printf("remote imap sync destination refresh user_id=%d routine_id=%d: %v", item.UserID, item.ID, err)
	}
}

func sourceAccount(item routine) store.MailAccount {
	return store.MailAccount{
		ID: item.ID, UserID: item.UserID, Host: item.SourceHost, Port: item.SourcePort,
		Username: item.SourceUsername, EncryptedPassword: item.EncryptedSourcePassword,
		UseTLS: item.SourceUseTLS,
	}
}

func adoptUIDValidity(ctx context.Context, db *sql.DB, userID, routineID int64, uidValidity uint32) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines
		SET source_uidvalidity = ?, last_source_uid = 0, updated_at = ?
		WHERE user_id = ? AND id = ?`, uidValidity, time.Now().UTC().Unix(), userID, routineID)
	return err
}

func messageFingerprint(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func retryDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	shift := failures - 1
	if shift > 6 {
		shift = 6
	}
	delay := 5 * time.Second * time.Duration(1<<shift)
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
