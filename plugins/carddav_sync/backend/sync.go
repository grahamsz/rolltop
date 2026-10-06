// File overview: Per-routine CardDAV reconciliation and one-way contact sync.
// Each enabled routine gets a worker that polls its address book on the
// routine's interval; sync-collection tokens make repeat runs incremental.

package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const (
	carddavReconcileInterval = 30 * time.Second
	carddavDefaultPollMin    = 15
	carddavMinPollMin        = 5
	carddavMaxPollMin        = 1440
)

type workerKey struct {
	userID    int64
	routineID int64
}

type routineManager struct {
	host  plugins.BackendStartHost
	store *store.Store

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	workers     map[workerKey]*routineWorker
	pending     map[workerKey]string
	wg          sync.WaitGroup
}

func newRoutineManager(host plugins.BackendStartHost, st *store.Store) *routineManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &routineManager{
		host: host, store: st, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), workers: make(map[workerKey]*routineWorker),
		pending: make(map[workerKey]string),
	}
}

func (m *routineManager) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.recoverInterruptedRuns()
		m.reconcileLoop()
	}()
	if m.ctx.Err() == nil {
		m.Wake()
	}
}

func (m *routineManager) recoverInterruptedRuns() {
	users, err := m.store.ListUsers(m.ctx)
	if err != nil {
		if m.ctx.Err() == nil {
			log.Printf("carddav sync startup recovery: %v", err)
		}
		return
	}
	for _, user := range users {
		if m.ctx.Err() != nil {
			return
		}
		db, err := m.store.UserDB(m.ctx, user.ID)
		if err != nil {
			continue
		}
		interrupted, err := recoverInterruptedRuns(m.ctx, db, user.ID)
		if err != nil {
			log.Printf("carddav sync startup recovery user_id=%d: %v", user.ID, err)
			continue
		}
		if interrupted > 0 {
			log.Printf("carddav sync startup recovery user_id=%d interrupted_runs=%d", user.ID, interrupted)
		}
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

// MutateRoutine stops any active sync before changing persisted settings so an
// edit, pause, or delete cannot race a run using prior credentials.
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
	ticker := time.NewTicker(carddavReconcileInterval)
	defer ticker.Stop()
	for {
		if err := m.reconcile(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("carddav sync reconciliation: %v", err)
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
		worker := newRoutineWorker(m.ctx, m.host, m.store, item)
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
	host  plugins.BackendStartHost
	store *store.Store
	item  routine

	ctx      context.Context
	cancel   context.CancelFunc
	triggers chan string
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func newRoutineWorker(parent context.Context, host plugins.BackendStartHost, st *store.Store, item routine) *routineWorker {
	ctx, cancel := context.WithCancel(parent)
	return &routineWorker{host: host, store: st, item: item,
		ctx: ctx, cancel: cancel, triggers: make(chan string, 1)}
}

func (w *routineWorker) SameVersion(item routine) bool {
	return w.item.ID == item.ID && w.item.Enabled == item.Enabled &&
		w.item.ServerURL == item.ServerURL && w.item.Username == item.Username &&
		w.item.EncryptedPassword == item.EncryptedPassword &&
		w.item.AddressbookURL == item.AddressbookURL &&
		w.item.PollIntervalMin == item.PollIntervalMin
}

func (w *routineWorker) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.runLoop()
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

func (w *routineWorker) pollInterval() time.Duration {
	minutes := w.item.PollIntervalMin
	if minutes < carddavMinPollMin {
		minutes = carddavDefaultPollMin
	}
	if minutes > carddavMaxPollMin {
		minutes = carddavMaxPollMin
	}
	return time.Duration(minutes) * time.Minute
}

func (w *routineWorker) runLoop() {
	ticker := time.NewTicker(w.pollInterval())
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
		err := w.runOnce(trigger)
		if err == nil {
			failures = 0
			continue
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		failures++
		delay := carddavRetryDelay(failures)
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(delay):
			w.Trigger("retry")
		}
	}
}

func carddavRetryDelay(failures int) time.Duration {
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

func (w *routineWorker) runOnce(trigger string) error {
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
	password, err := mmcrypto.DecryptString(w.host.MasterKey(), item.EncryptedPassword)
	if err != nil {
		_ = suspendRoutineForCredentialError(context.Background(), db, item, "The stored CardDAV password could not be decrypted; enter it again.")
		w.stopOnce.Do(w.cancel)
		return errors.New("could not decrypt CardDAV password")
	}
	client, err := newCardDAVClient(item.ServerURL, carddavCredentials{Username: item.Username, Password: password})
	if err != nil {
		_ = failRoutineRun(context.Background(), db, item, sanitizeCardDAVMessage(err), false)
		return err
	}
	if err := beginRoutineRun(w.ctx, db, item); err != nil {
		return err
	}
	runID, err := createRun(w.ctx, db, item.UserID, item.ID, trigger)
	if err != nil {
		return err
	}
	var added, updated, deleted, scanned int64
	fail := func(runErr error) error {
		if errors.Is(runErr, context.Canceled) || w.ctx.Err() != nil {
			_ = finishRun(context.Background(), db, item.UserID, runID, "canceled", "", scanned, added, updated, deleted)
			return runErr
		}
		message := sanitizeCardDAVMessage(runErr)
		_ = finishRun(context.Background(), db, item.UserID, runID, "failed", message, scanned, added, updated, deleted)
		if isCardDAVAuthenticationError(runErr) {
			if err := suspendRoutineForCredentialError(context.Background(), db, item, message); err != nil {
				_ = failRoutineRun(context.Background(), db, item, message, true)
			} else {
				w.stopOnce.Do(w.cancel)
			}
		} else {
			_ = failRoutineRun(context.Background(), db, item, message, true)
		}
		return runErr
	}
	changes, newToken, err := client.SyncAddressBook(w.ctx, item.AddressbookURL, item.SyncToken)
	if errors.Is(err, errSyncCollectionUnsupported) || isInvalidSyncToken(err) {
		changes, newToken, err = w.fullFetch(w.ctx, client, db, item)
	}
	if err != nil {
		return fail(err)
	}
	mappings, err := listMappings(w.ctx, db, item.UserID, item.ID)
	if err != nil {
		return fail(err)
	}
	for _, change := range changes {
		if w.ctx.Err() != nil {
			return fail(w.ctx.Err())
		}
		scanned++
		if scanned%25 == 0 {
			if _, err := db.ExecContext(w.ctx, `UPDATE plugin_carddav_sync_runs SET scanned=?, added=?, updated=?, deleted=? WHERE user_id=? AND id=?`, scanned, added, updated, deleted, item.UserID, runID); err != nil {
				return fail(err)
			}
		}
		if change.Deleted {
			n, err := w.applyDeletion(w.ctx, db, item, mappings, change.Href)
			if err != nil {
				return fail(err)
			}
			deleted += n
			continue
		}
		cards := parseVCards(change.VCard)
		if len(cards) == 0 {
			continue
		}
		for _, card := range cards {
			if contactIsEmpty(card.Contact) {
				continue
			}
			kind, err := w.applyCard(w.ctx, db, item, mappings, card, change)
			if err != nil {
				return fail(err)
			}
			switch kind {
			case "added":
				added++
			case "updated":
				updated++
			}
		}
	}
	if err := finishRun(w.ctx, db, item.UserID, runID, "completed", "", scanned, added, updated, deleted); err != nil {
		return fail(err)
	}
	if err := completeRoutineRun(w.ctx, db, item, newToken, added, updated, deleted); err != nil {
		return fail(err)
	}
	log.Printf("carddav sync completed user_id=%d routine_id=%d trigger=%s scanned=%d added=%d updated=%d deleted=%d",
		item.UserID, item.ID, trigger, scanned, added, updated, deleted)
	return nil
}

// fullFetch is the fallback when the server does not support sync-collection:
// list every href, multiget the ones whose etag changed, and treat missing
// hrefs as deletions since the PROPFIND listing is authoritative.
func (w *routineWorker) fullFetch(ctx context.Context, client *carddavClient, db *sql.DB, item routine) ([]syncChange, string, error) {
	hrefs, err := client.ListHrefs(ctx, item.AddressbookURL)
	if err != nil {
		return nil, "", err
	}
	mappings, err := listMappings(ctx, db, item.UserID, item.ID)
	if err != nil {
		return nil, "", err
	}
	byHref := make(map[string]mappingRow, len(mappings))
	for _, row := range mappings {
		byHref[row.Href] = row
	}
	// The listing establishes presence; fetch failures must not imply removal.
	present := make(map[string]bool, len(hrefs))
	var fetch []string
	for href, etag := range hrefs {
		present[href] = true
		if row, ok := byHref[href]; !ok || row.ETag == "" || row.ETag != etag {
			fetch = append(fetch, href)
		}
	}
	changes, err := client.MultigetVCards(ctx, item.AddressbookURL, fetch)
	if err != nil {
		return nil, "", err
	}
	out := append([]syncChange(nil), changes...)
	for href := range byHref {
		if !present[href] {
			out = append(out, syncChange{Href: href, Deleted: true})
		}
	}
	return out, "", nil
}

// applyCard creates or updates the Rolltop contact for one vCard. The mapping
// table is checked first, then an existing contact is matched by email the way
// the core vCard import does.
func (w *routineWorker) applyCard(ctx context.Context, db *sql.DB, item routine, mappings map[string]mappingRow, card parsedVCard, change syncChange) (string, error) {
	if row, ok := mappings[card.UID]; ok {
		if change.ETag != "" && row.ETag == change.ETag {
			row.Href = change.Href
			if err := upsertMapping(ctx, db, item.UserID, item.ID, row); err != nil {
				return "", err
			}
			mappings[card.UID] = row
			return "", nil
		}
		existing, err := w.store.GetContactForUser(ctx, item.UserID, row.ContactID)
		if err == nil {
			merged := mergeSyncedContact(existing, card.Contact)
			if _, err := w.store.UpdateContact(ctx, item.UserID, existing.ID, merged); err != nil {
				return "", err
			}
			row.Href = change.Href
			row.ETag = change.ETag
			if err := upsertMapping(ctx, db, item.UserID, item.ID, row); err != nil {
				return "", err
			}
			mappings[card.UID] = row
			return "updated", nil
		}
		if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		// The mapped contact was deleted by the user; fall through and recreate.
	}
	for _, email := range card.Contact.Emails {
		existing, err := w.store.GetContactByEmailForUser(ctx, item.UserID, email.Email)
		if err == nil {
			merged := mergeSyncedContact(existing, card.Contact)
			saved, err := w.store.UpdateContact(ctx, item.UserID, existing.ID, merged)
			if err != nil {
				return "", err
			}
			row := mappingRow{VCardUID: card.UID, Href: change.Href, ETag: change.ETag, ContactID: saved.ID, OwnsContact: false}
			if err := upsertMapping(ctx, db, item.UserID, item.ID, row); err != nil {
				return "", err
			}
			mappings[card.UID] = row
			return "updated", nil
		}
		if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	saved, err := w.store.CreateContact(ctx, item.UserID, card.Contact)
	if err != nil {
		return "", err
	}
	// Record its origin, but retain local data if the remote card disappears.
	row := mappingRow{VCardUID: card.UID, Href: change.Href, ETag: change.ETag, ContactID: saved.ID, OwnsContact: true}
	if err := upsertMapping(ctx, db, item.UserID, item.ID, row); err != nil {
		return "", err
	}
	mappings[card.UID] = row
	return "added", nil
}

// applyDeletion removes sync links only. A contact created by this plugin
// can later gain local edits or links from other routines, so origin alone
// does not establish permission to delete its local data.
func (w *routineWorker) applyDeletion(ctx context.Context, db *sql.DB, item routine, mappings map[string]mappingRow, href string) (int64, error) {
	var removed int64
	for uid, row := range mappings {
		if row.Href != href {
			continue
		}
		if err := deleteMapping(ctx, db, item.UserID, item.ID, uid); err != nil {
			return removed, err
		}
		delete(mappings, uid)
		removed++
	}
	return removed, nil
}

func isInvalidSyncToken(err error) bool { return errors.Is(err, errInvalidSyncToken) }

func isCardDAVAuthenticationError(err error) bool { return errors.Is(err, errCardDAVAuth) }

func sanitizeCardDAVMessage(err error) string {
	if err == nil {
		return ""
	}
	if sanitized := sanitizeCardDAVError(err); sanitized != err {
		return sanitized.Error()
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "authentication"), strings.Contains(message, "unauthorized"), strings.Contains(message, "401"), strings.Contains(message, "403"):
		return "CardDAV authentication failed. Check the username and app password."
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "The CardDAV server timed out."
	case strings.Contains(message, "certificate"), strings.Contains(message, "tls"):
		return "The CardDAV server's TLS connection could not be verified."
	case strings.Contains(message, "no such host"), strings.Contains(message, "connection refused"), strings.Contains(message, "no route to host"):
		return "The CardDAV server could not be reached."
	case errors.Is(err, context.Canceled):
		return "The CardDAV sync was canceled."
	default:
		return "The CardDAV server could not complete the request."
	}
}
