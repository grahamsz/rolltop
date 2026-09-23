// File overview: Runtime API and lifecycle for generic CardDAV contact sync.
// Credentials are encrypted with the Rolltop master key at rest and decrypted
// only while opening a CardDAV connection; the API never returns passwords.

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const (
	apiPath  = "plugins/carddav_sync"
	pluginID = "carddav_sync"
)

type carddavSyncBackend struct {
	mu      sync.Mutex
	routes  []plugins.ProtectedAPIRouteHandle
	manager *routineManager
}

// RolltopPlugin is loaded by the runtime Go plugin host.
func RolltopPlugin() plugins.BackendPlugin {
	return &carddavSyncBackend{}
}

func (*carddavSyncBackend) ID() string { return pluginID }

func (p *carddavSyncBackend) Start(host plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	handle, err := host.RegisterProtectedAPI(p.ID(), plugins.ProtectedAPIRoute{
		Path: apiPath, Prefix: true, Handle: p.handleAPI,
	})
	if err != nil {
		return err
	}
	p.routes = append(p.routes, handle)
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		p.stopLocked()
		return fmt.Errorf("CardDAV sync store is not available")
	}
	p.manager = newRoutineManager(host, st)
	p.manager.Start()
	return nil
}

func (p *carddavSyncBackend) Stop(plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	return nil
}

func (p *carddavSyncBackend) stopLocked() {
	if p.manager != nil {
		p.manager.Stop()
		p.manager = nil
	}
	p.routes = nil
}

func (p *carddavSyncBackend) handleAPI(host plugins.APIHost, path string, w http.ResponseWriter, r *http.Request) {
	current, ok := host.RequireAPIAuth(w, r)
	if !ok {
		return
	}
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		host.WriteAPIError(w, http.StatusServiceUnavailable, "CardDAV sync is not available")
		return
	}
	db, err := st.UserDB(r.Context(), current.UserID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(path, apiPath), "/")
	switch {
	case rest == "routines" && r.Method == http.MethodGet:
		p.apiListRoutines(host, db, current.UserID, w, r)
	case rest == "routines" && r.Method == http.MethodPost:
		p.apiSaveRoutine(host, db, current.UserID, 0, w, r)
	case rest == "routines/discover" && r.Method == http.MethodPost:
		p.apiDiscover(host, db, current.UserID, w, r)
	case strings.HasPrefix(rest, "routines/"):
		p.apiRoutineAction(host, db, current.UserID, rest, w, r)
	default:
		host.WriteAPIError(w, http.StatusNotFound, "CardDAV sync route not found")
	}
}

type routineView struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	ServerURL       string     `json:"server_url"`
	Username        string     `json:"username"`
	HasPassword     bool       `json:"has_password"`
	AddressbookURL  string     `json:"addressbook_url"`
	PollIntervalMin int        `json:"poll_interval_minutes"`
	State           string     `json:"state"`
	LastError       string     `json:"last_error"`
	SyncedTotal     int64      `json:"synced_total"`
	LastStartedAt   int64      `json:"last_started_at"`
	LastCompletedAt int64      `json:"last_completed_at"`
	LastRun         *runRecord `json:"last_run,omitempty"`
}

type routineInput struct {
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	ServerURL       string `json:"server_url"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	AddressbookURL  string `json:"addressbook_url"`
	PollIntervalMin int    `json:"poll_interval_minutes"`
}

type discoverInput struct {
	ServerURL string `json:"server_url"`
	Username  string `json:"username"`
	Password  string `json:"password"`
}

type addressBookView struct {
	URL         string `json:"url"`
	DisplayName string `json:"display_name"`
}

func (p *carddavSyncBackend) apiListRoutines(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	items, err := listRoutines(r.Context(), db, userID, false)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	views := make([]routineView, 0, len(items))
	for _, item := range items {
		view, err := presentRoutine(r.Context(), db, item)
		if err != nil {
			host.ServerError(w, err)
			return
		}
		views = append(views, view)
	}
	host.WriteJSON(w, map[string]any{"routines": views})
}

func (p *carddavSyncBackend) apiRoutineAction(host plugins.APIHost, db *sql.DB, userID int64, rest string, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 || parts[0] != "routines" {
		host.WriteAPIError(w, http.StatusNotFound, "routine route not found")
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		host.WriteAPIError(w, http.StatusBadRequest, "invalid routine id")
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPut {
		p.apiSaveRoutine(host, db, userID, id, w, r)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodDelete {
		if !host.VerifyCSRF(w, r) {
			return
		}
		if err := p.mutateRoutine(userID, id, func() error {
			return deleteRoutine(r.Context(), db, userID, id)
		}); err != nil {
			writeScopedError(host, w, err, "routine not found")
			return
		}
		host.WriteJSON(w, map[string]any{"ok": true})
		return
	}
	if len(parts) == 3 && parts[2] == "test" && r.Method == http.MethodPost {
		p.apiTestRoutine(host, db, userID, id, w, r)
		return
	}
	if len(parts) == 3 && parts[2] == "sync" && r.Method == http.MethodPost {
		p.apiTriggerSync(host, db, userID, id, w, r)
		return
	}
	if len(parts) == 3 && parts[2] == "runs" && r.Method == http.MethodGet {
		if _, err := getRoutine(r.Context(), db, userID, id); err != nil {
			writeScopedError(host, w, err, "routine not found")
			return
		}
		runs, err := recentRuns(r.Context(), db, userID, id, 20)
		if err != nil {
			host.ServerError(w, err)
			return
		}
		host.WriteJSON(w, map[string]any{"runs": runs})
		return
	}
	host.WriteAPIError(w, http.StatusNotFound, "routine route not found")
}

func (p *carddavSyncBackend) apiSaveRoutine(host plugins.APIHost, db *sql.DB, userID, routineID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in routineInput
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	item, err := p.prepareRoutine(r.Context(), host, db, userID, routineID, in)
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var saved routine
	persist := func() error {
		var persistErr error
		saved, persistErr = persistRoutine(r.Context(), db, userID, item)
		return persistErr
	}
	if routineID > 0 {
		err = p.mutateRoutine(userID, routineID, persist)
	} else {
		err = persist()
	}
	if err != nil {
		if isUniqueError(err) {
			host.WriteAPIError(w, http.StatusConflict, "a routine already exists for that server, username, and address book")
		} else {
			host.ServerError(w, err)
		}
		return
	}
	p.wakeManager()
	view, err := presentRoutine(r.Context(), db, saved)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	host.WriteJSON(w, map[string]any{"ok": true, "routine": view})
}

func (p *carddavSyncBackend) prepareRoutine(ctx context.Context, host plugins.APIHost, db *sql.DB, userID, routineID int64, in routineInput) (routine, error) {
	var existing routine
	if routineID > 0 {
		var err error
		existing, err = getRoutine(ctx, db, userID, routineID)
		if err != nil {
			return routine{}, fmt.Errorf("routine not found")
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return routine{}, fmt.Errorf("name is required")
	}
	if len([]rune(name)) > 100 {
		return routine{}, fmt.Errorf("name is too long")
	}
	serverURL := strings.TrimRight(strings.TrimSpace(in.ServerURL), "/")
	if serverURL == "" {
		return routine{}, fmt.Errorf("server URL is required")
	}
	username := strings.TrimSpace(in.Username)
	if username == "" {
		return routine{}, fmt.Errorf("username is required")
	}
	addressbookURL := strings.TrimRight(strings.TrimSpace(in.AddressbookURL), "/")
	if addressbookURL == "" {
		return routine{}, fmt.Errorf("choose an address book")
	}
	pollMin := in.PollIntervalMin
	if pollMin <= 0 {
		pollMin = carddavDefaultPollMin
	}
	if pollMin < carddavMinPollMin || pollMin > carddavMaxPollMin {
		return routine{}, fmt.Errorf("poll interval must be between %d and %d minutes", carddavMinPollMin, carddavMaxPollMin)
	}
	item := existing
	item.UserID = userID
	item.Name = name
	item.ServerURL = serverURL
	item.Username = username
	item.AddressbookURL = addressbookURL
	item.PollIntervalMin = pollMin
	item.Enabled = in.Enabled
	if in.Password != "" {
		encrypted, err := mmcrypto.EncryptString(host.MasterKey(), in.Password)
		if err != nil {
			return routine{}, fmt.Errorf("could not encrypt the password")
		}
		item.EncryptedPassword = encrypted
	}
	if routineID == 0 {
		if in.Password == "" {
			return routine{}, fmt.Errorf("password is required")
		}
		item.State = "queued"
		if !item.Enabled {
			item.State = "paused"
		}
		return item, nil
	}
	if in.Password != "" {
		// Credentials rotated; treat the server as a fresh source so stale
		// mappings cannot shadow the new account's contacts.
		if err := resetRoutineProgress(ctx, db, userID, routineID); err != nil {
			return routine{}, fmt.Errorf("could not reset sync state: %w", err)
		}
		item.SyncToken = ""
	} else if sourceIdentityChanged(item, existing) {
		if err := resetRoutineProgress(ctx, db, userID, routineID); err != nil {
			return routine{}, fmt.Errorf("could not reset sync state: %w", err)
		}
		item.SyncToken = ""
	}
	if !item.Enabled {
		item.State = "paused"
	} else if existing.State == "" || existing.State == "paused" {
		item.State = "queued"
	}
	return item, nil
}

func persistRoutine(ctx context.Context, db *sql.DB, userID int64, item routine) (routine, error) {
	now := time.Now().UTC().Unix()
	if item.ID == 0 {
		state := item.State
		if state == "" {
			state = "queued"
		}
		res, err := db.ExecContext(ctx, `INSERT INTO plugin_carddav_sync_routines
			(user_id, name, enabled, server_url, username, encrypted_password,
			 addressbook_url, poll_interval_minutes, sync_token, state, last_error,
			 last_started_at, last_completed_at, synced_total, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', 0, 0, 0, ?, ?)`,
			userID, item.Name, boolInt(item.Enabled), item.ServerURL, item.Username,
			item.EncryptedPassword, item.AddressbookURL, item.PollIntervalMin, state, now, now)
		if err != nil {
			return routine{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return routine{}, err
		}
		item.ID = id
	} else {
		state := item.State
		if state == "" {
			state = "queued"
		}
		_, err := db.ExecContext(ctx, `UPDATE plugin_carddav_sync_routines SET
			name = ?, enabled = ?, server_url = ?, username = ?, encrypted_password = ?,
			addressbook_url = ?, poll_interval_minutes = ?, sync_token = ?,
			state = ?, last_error = ?, updated_at = ?
			WHERE user_id = ? AND id = ?`,
			item.Name, boolInt(item.Enabled), item.ServerURL, item.Username,
			item.EncryptedPassword, item.AddressbookURL, item.PollIntervalMin,
			item.SyncToken, state, item.LastError, now, userID, item.ID)
		if err != nil {
			return routine{}, err
		}
	}
	return getRoutine(ctx, db, userID, item.ID)
}

func presentRoutine(ctx context.Context, db *sql.DB, item routine) (routineView, error) {
	lastRun, err := latestRun(ctx, db, item.UserID, item.ID)
	if err != nil {
		return routineView{}, err
	}
	return routineView{
		ID:              item.ID,
		Name:            item.Name,
		Enabled:         item.Enabled,
		ServerURL:       item.ServerURL,
		Username:        item.Username,
		HasPassword:     item.EncryptedPassword != "",
		AddressbookURL:  item.AddressbookURL,
		PollIntervalMin: item.PollIntervalMin,
		State:           item.State,
		LastError:       item.LastError,
		SyncedTotal:     item.SyncedTotal,
		LastStartedAt:   unixOrZero(item.LastStartedAt),
		LastCompletedAt: unixOrZero(item.LastCompletedAt),
		LastRun:         lastRun,
	}, nil
}

func (p *carddavSyncBackend) apiDiscover(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in discoverInput
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	client, err := newCardDAVClient(in.ServerURL, carddavCredentials{Username: in.Username, Password: in.Password})
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	books, err := client.DiscoverAddressBooks(ctx)
	if err != nil {
		host.WriteAPIError(w, http.StatusBadGateway, sanitizeCardDAVMessage(err))
		return
	}
	views := make([]addressBookView, 0, len(books))
	for _, book := range books {
		views = append(views, addressBookView{URL: book.URL, DisplayName: book.DisplayName})
	}
	host.WriteJSON(w, map[string]any{"address_books": views})
}

func (p *carddavSyncBackend) apiTestRoutine(host plugins.APIHost, db *sql.DB, userID, routineID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	item, err := getRoutine(r.Context(), db, userID, routineID)
	if err != nil {
		writeScopedError(host, w, err, "routine not found")
		return
	}
	password, err := mmcrypto.DecryptString(host.MasterKey(), item.EncryptedPassword)
	if err != nil {
		host.WriteAPIError(w, http.StatusBadGateway, "the stored password could not be decrypted")
		return
	}
	client, err := newCardDAVClient(item.ServerURL, carddavCredentials{Username: item.Username, Password: password})
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	books, err := client.DiscoverAddressBooks(ctx)
	if err != nil {
		host.WriteAPIError(w, http.StatusBadGateway, sanitizeCardDAVMessage(err))
		return
	}
	views := make([]addressBookView, 0, len(books))
	for _, book := range books {
		views = append(views, addressBookView{URL: book.URL, DisplayName: book.DisplayName})
	}
	host.WriteJSON(w, map[string]any{"ok": true, "address_books": views})
}

func (p *carddavSyncBackend) apiTriggerSync(host plugins.APIHost, db *sql.DB, userID, routineID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	item, err := getRoutine(r.Context(), db, userID, routineID)
	if err != nil {
		writeScopedError(host, w, err, "routine not found")
		return
	}
	if !item.Enabled {
		host.WriteAPIError(w, http.StatusConflict, "enable this routine before running it")
		return
	}
	if !p.triggerManager(userID, routineID, "manual") {
		p.wakeManager()
	}
	host.WriteJSON(w, map[string]any{"ok": true, "queued": true})
}

func (p *carddavSyncBackend) wakeManager() {
	p.mu.Lock()
	manager := p.manager
	p.mu.Unlock()
	if manager != nil {
		manager.Wake()
	}
}

func (p *carddavSyncBackend) triggerManager(userID, routineID int64, trigger string) bool {
	p.mu.Lock()
	manager := p.manager
	p.mu.Unlock()
	if manager == nil {
		return false
	}
	return manager.Trigger(userID, routineID, trigger)
}

func (p *carddavSyncBackend) mutateRoutine(userID, routineID int64, mutate func() error) error {
	p.mu.Lock()
	manager := p.manager
	p.mu.Unlock()
	if manager == nil {
		return mutate()
	}
	return manager.MutateRoutine(userID, routineID, mutate)
}

func writeScopedError(host plugins.APIHost, w http.ResponseWriter, err error, notFound string) {
	if errors.Is(err, sql.ErrNoRows) {
		host.WriteAPIError(w, http.StatusNotFound, notFound)
		return
	}
	host.ServerError(w, err)
}
