// File overview: Runtime API and lifecycle for one-way remote IMAP migration routines.

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
	"rolltop/backend/imapclient"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const (
	apiPath              = "plugins/remote_imap_sync"
	pluginID             = "remote_imap_sync"
	remoteFetchBatchSize = 1 // Bound each routine to one in-memory raw message.
)

type remoteIMAPSyncBackend struct {
	mu      sync.Mutex
	routes  []plugins.ProtectedAPIRouteHandle
	manager *routineManager
	fetcher *imapclient.Fetcher
}

// RolltopPlugin is loaded by the runtime Go plugin host.
func RolltopPlugin() plugins.BackendPlugin {
	return &remoteIMAPSyncBackend{}
}

func (*remoteIMAPSyncBackend) ID() string { return pluginID }

func (p *remoteIMAPSyncBackend) Start(host plugins.BackendStartHost) error {
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
	p.fetcher = &imapclient.Fetcher{MasterKey: host.MasterKey(), Timeout: time.Minute, BatchSize: remoteFetchBatchSize}
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		p.stopLocked()
		return fmt.Errorf("remote IMAP sync store is not available")
	}
	p.manager = newRoutineManager(host, st, p.fetcher)
	p.manager.Start()
	return nil
}

func (p *remoteIMAPSyncBackend) Stop(plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	return nil
}

func (p *remoteIMAPSyncBackend) stopLocked() {
	if p.manager != nil {
		p.manager.Stop()
		p.manager = nil
	}
	for _, route := range p.routes {
		route.Unregister()
	}
	p.routes = nil
}

type sourceInput struct {
	Provider string `json:"provider"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	Password string `json:"password"`
	Mailbox  string `json:"mailbox"`
	UseTLS   *bool  `json:"use_tls"`
}

type destinationInput struct {
	AccountID int64 `json:"account_id"`
	MailboxID int64 `json:"mailbox_id"`
}

type routineInput struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	Enabled     *bool            `json:"enabled"`
	Source      sourceInput      `json:"source"`
	Destination destinationInput `json:"destination"`
	AfterDate   string           `json:"after_date"`
}

type enabledInput struct {
	Enabled bool `json:"enabled"`
}

type discoverInput struct {
	RoutineID int64       `json:"routine_id"`
	Source    sourceInput `json:"source"`
}

type sourceView struct {
	Provider    string `json:"provider"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	UseTLS      bool   `json:"use_tls"`
	Username    string `json:"username"`
	Mailbox     string `json:"mailbox"`
	HasPassword bool   `json:"has_password"`
}

type destinationView struct {
	AccountID    int64  `json:"account_id"`
	AccountLabel string `json:"account_label"`
	AccountEmail string `json:"account_email"`
	MailboxID    int64  `json:"mailbox_id"`
	MailboxName  string `json:"mailbox_name"`
}

type routineView struct {
	ID               int64           `json:"id"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	Source           sourceView      `json:"source"`
	Destination      destinationView `json:"destination"`
	AfterDate        string          `json:"after_date"`
	State            string          `json:"state"`
	LastError        string          `json:"last_error"`
	LastStartedAt    int64           `json:"last_started_at"`
	LastSuccessAt    int64           `json:"last_success_at"`
	LastActivityAt   int64           `json:"last_activity_at"`
	NextRetryAt      int64           `json:"next_retry_at"`
	TransferredTotal int64           `json:"transferred_total"`
	SkippedTotal     int64           `json:"skipped_total"`
	LatestRun        *runRecord      `json:"latest_run"`
	ActiveRun        *runRecord      `json:"active_run"`
}

type destinationOption struct {
	ID      int64               `json:"id"`
	Label   string              `json:"label"`
	Email   string              `json:"email"`
	Folders []destinationFolder `json:"folders"`
}

type destinationFolder struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func (p *remoteIMAPSyncBackend) handleAPI(host plugins.APIHost, path string, w http.ResponseWriter, r *http.Request) {
	current, ok := host.RequireAPIAuth(w, r)
	if !ok {
		return
	}
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		host.WriteAPIError(w, http.StatusServiceUnavailable, "remote IMAP sync is not available")
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
		p.apiListRoutines(host, st, db, current.UserID, w, r)
	case rest == "routines" && r.Method == http.MethodPost:
		p.apiSaveRoutine(host, st, db, current.UserID, 0, w, r)
	case rest == "source/discover" && r.Method == http.MethodPost:
		p.apiDiscoverSource(host, db, current.UserID, w, r)
	case strings.HasPrefix(rest, "routines/"):
		p.apiRoutineAction(host, st, db, current.UserID, rest, w, r)
	default:
		host.WriteAPIError(w, http.StatusNotFound, "remote IMAP sync route not found")
	}
}

func (p *remoteIMAPSyncBackend) apiListRoutines(host plugins.APIHost, st *store.Store, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	items, err := listRoutines(r.Context(), db, userID, false)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	views := make([]routineView, 0, len(items))
	for _, item := range items {
		view, err := presentRoutine(r.Context(), st, db, item)
		if err != nil {
			host.ServerError(w, err)
			return
		}
		views = append(views, view)
	}
	destinations, err := listDestinationOptions(r.Context(), st, userID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	host.WriteJSON(w, map[string]any{"routines": views, "destinations": destinations})
}

func (p *remoteIMAPSyncBackend) apiRoutineAction(host plugins.APIHost, st *store.Store, db *sql.DB, userID int64, rest string, w http.ResponseWriter, r *http.Request) {
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
		p.apiSaveRoutine(host, st, db, userID, id, w, r)
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
	if len(parts) == 3 && parts[2] == "enabled" && r.Method == http.MethodPost {
		if !host.VerifyCSRF(w, r) {
			return
		}
		var in enabledInput
		if !host.DecodeJSON(w, r, &in) {
			return
		}
		if err := p.mutateRoutine(userID, id, func() error {
			return setRoutineEnabled(r.Context(), db, userID, id, in.Enabled)
		}); err != nil {
			writeScopedError(host, w, err, "routine not found")
			return
		}
		host.WriteJSON(w, map[string]any{"ok": true})
		return
	}
	if len(parts) == 3 && parts[2] == "run" && r.Method == http.MethodPost {
		if !host.VerifyCSRF(w, r) {
			return
		}
		item, err := getRoutine(r.Context(), db, userID, id)
		if err != nil {
			writeScopedError(host, w, err, "routine not found")
			return
		}
		if !item.Enabled {
			host.WriteAPIError(w, http.StatusConflict, "enable this routine before running it")
			return
		}
		if !p.triggerManager(userID, id, "manual") {
			p.wakeManager()
		}
		host.WriteJSON(w, map[string]any{"ok": true, "queued": true})
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

func (p *remoteIMAPSyncBackend) apiSaveRoutine(host plugins.APIHost, st *store.Store, db *sql.DB, userID, routineID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in routineInput
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	item, err := p.prepareRoutine(r.Context(), host, st, db, userID, routineID, in)
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var saved routine
	persist := func() error {
		var persistErr error
		saved, persistErr = persistRoutine(r.Context(), db, item)
		return persistErr
	}
	if routineID > 0 {
		err = p.mutateRoutine(userID, routineID, persist)
	} else {
		err = persist()
	}
	if err != nil {
		if isUniqueError(err) {
			host.WriteAPIError(w, http.StatusConflict, "a routine already uses that source and destination")
		} else {
			host.ServerError(w, err)
		}
		return
	}
	p.wakeManager()
	view, err := presentRoutine(r.Context(), st, db, saved)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	host.WriteJSON(w, map[string]any{"ok": true, "routine": view})
}

func (p *remoteIMAPSyncBackend) prepareRoutine(ctx context.Context, host plugins.APIHost, st *store.Store, db *sql.DB, userID, routineID int64, in routineInput) (routine, error) {
	var existing routine
	if routineID > 0 {
		var err error
		existing, err = getRoutine(ctx, db, userID, routineID)
		if err != nil {
			return routine{}, fmt.Errorf("routine not found")
		}
	}
	provider := strings.ToLower(strings.TrimSpace(in.Source.Provider))
	if provider == "" {
		provider = "custom"
	}
	hostname := strings.TrimSpace(in.Source.Host)
	port := in.Source.Port
	if provider == "gmail" {
		if hostname == "" {
			hostname = "imap.gmail.com"
		}
		if port == 0 {
			port = 993
		}
	}
	if port == 0 {
		port = 993
	}
	security := strings.ToLower(strings.TrimSpace(in.Source.Security))
	switch security {
	case "", "tls", "ssl", "plain", "none":
	case "starttls":
		return routine{}, fmt.Errorf("STARTTLS sources are not supported yet; use implicit TLS on port 993")
	default:
		return routine{}, fmt.Errorf("source security mode is invalid")
	}
	useTLS := true
	if in.Source.UseTLS != nil {
		useTLS = *in.Source.UseTLS
	} else if security != "" {
		switch security {
		case "starttls":
			return routine{}, fmt.Errorf("STARTTLS sources are not supported yet; use implicit TLS on port 993")
		case "tls", "ssl":
			useTLS = true
		case "plain", "none":
			useTLS = false
		}
	} else if routineID > 0 {
		useTLS = existing.SourceUseTLS
	}
	afterDate, err := parseAfterDate(in.AfterDate)
	if err != nil {
		return routine{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	} else if routineID > 0 {
		enabled = existing.Enabled
	}
	item := routine{
		ID: routineID, UserID: userID, Name: strings.TrimSpace(in.Name), Enabled: enabled,
		SourceProvider: provider, SourceHost: hostname, SourcePort: port,
		SourceUsername: strings.TrimSpace(in.Source.Username), SourceUseTLS: useTLS,
		SourceMailbox:        strings.TrimSpace(in.Source.Mailbox),
		DestinationAccountID: in.Destination.AccountID,
		DestinationMailboxID: in.Destination.MailboxID,
		AfterDate:            afterDate,
	}
	if item.SourceMailbox == "" {
		item.SourceMailbox = "INBOX"
	}
	if err := validateRoutineRecord(item); err != nil {
		return routine{}, err
	}
	mailbox, err := st.GetMailboxForUser(ctx, userID, item.DestinationMailboxID)
	if err != nil || mailbox.AccountID != item.DestinationAccountID {
		return routine{}, fmt.Errorf("destination folder was not found")
	}
	destinationAccount, err := st.GetMailAccountForUser(ctx, userID, item.DestinationAccountID)
	if err != nil {
		return routine{}, fmt.Errorf("destination account was not found")
	}
	if sameIMAPEndpoint(item, destinationAccount) {
		return routine{}, fmt.Errorf("source and destination cannot use the same IMAP account")
	}
	password := in.Source.Password
	if provider == "gmail" {
		password = strings.ReplaceAll(password, " ", "")
	}
	if password != "" {
		encrypted, err := mmcrypto.EncryptString(host.MasterKey(), password)
		if err != nil {
			return routine{}, fmt.Errorf("could not encrypt source password: %w", err)
		}
		item.EncryptedSourcePassword = encrypted
	} else if routineID > 0 && !sourceConnectionChanged(existing, item) {
		item.EncryptedSourcePassword = existing.EncryptedSourcePassword
	} else if routineID > 0 {
		return routine{}, fmt.Errorf("source password is required when connection settings change")
	} else {
		return routine{}, fmt.Errorf("source password is required")
	}
	if routineID > 0 {
		item.MarkerSecret = existing.MarkerSecret
		item.SourceUIDValidity = existing.SourceUIDValidity
		item.LastSourceUID = existing.LastSourceUID
		item.State = existing.State
		item.LastError = existing.LastError
		item.LastStartedAt = existing.LastStartedAt
		item.LastCompletedAt = existing.LastCompletedAt
		item.LastActivityAt = existing.LastActivityAt
		item.NextRetryAt = existing.NextRetryAt
		item.TransferredTotal = existing.TransferredTotal
		item.SkippedTotal = existing.SkippedTotal
		item.CreatedAt = existing.CreatedAt
		item.UpdatedAt = existing.UpdatedAt
	} else {
		item.MarkerSecret, err = newMarkerSecret()
		if err != nil {
			return routine{}, err
		}
	}
	if item.Enabled {
		item.State = "queued"
	} else {
		item.State = "paused"
	}
	return item, nil
}

func persistRoutine(ctx context.Context, db *sql.DB, item routine) (routine, error) {
	now := time.Now().UTC().Unix()
	if item.ID == 0 {
		res, err := db.ExecContext(ctx, `INSERT INTO plugin_remote_imap_sync_routines
			(user_id, name, enabled, source_provider, source_host, source_port, source_username,
			 encrypted_source_password, source_use_tls, source_mailbox, destination_account_id,
			 destination_mailbox_id, after_date, marker_secret, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.UserID, item.Name, boolInt(item.Enabled), item.SourceProvider, item.SourceHost,
			item.SourcePort, item.SourceUsername, item.EncryptedSourcePassword, boolInt(item.SourceUseTLS),
			item.SourceMailbox, item.DestinationAccountID, item.DestinationMailboxID,
			unixOrZero(item.AfterDate), item.MarkerSecret, item.State, now, now)
		if err != nil {
			return routine{}, err
		}
		item.ID, err = res.LastInsertId()
		if err != nil {
			return routine{}, err
		}
		return getRoutine(ctx, db, item.UserID, item.ID)
	}
	existing, err := getRoutine(ctx, db, item.UserID, item.ID)
	if err != nil {
		return routine{}, err
	}
	changed := structuralChange(existing, item)
	if sourceIdentityChanged(existing, item) {
		item.MarkerSecret, err = newMarkerSecret()
		if err != nil {
			return routine{}, err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return routine{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines SET
		name = ?, enabled = ?, source_provider = ?, source_host = ?, source_port = ?,
		source_username = ?, encrypted_source_password = ?, source_use_tls = ?, source_mailbox = ?,
		destination_account_id = ?, destination_mailbox_id = ?, after_date = ?, marker_secret = ?,
		state = ?, updated_at = ? WHERE user_id = ? AND id = ?`, item.Name, boolInt(item.Enabled),
		item.SourceProvider, item.SourceHost, item.SourcePort, item.SourceUsername,
		item.EncryptedSourcePassword, boolInt(item.SourceUseTLS), item.SourceMailbox,
		item.DestinationAccountID, item.DestinationMailboxID, unixOrZero(item.AfterDate), item.MarkerSecret,
		item.State, now, item.UserID, item.ID)
	if err != nil {
		return routine{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return routine{}, err
		}
		return routine{}, sql.ErrNoRows
	}
	if changed {
		if err := resetRoutineProgress(ctx, tx, item.UserID, item.ID); err != nil {
			return routine{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return routine{}, err
	}
	return getRoutine(ctx, db, item.UserID, item.ID)
}

func sourceIdentityChanged(a, b routine) bool {
	return sourceConnectionChanged(a, b) || a.SourceMailbox != b.SourceMailbox
}

func sourceConnectionChanged(a, b routine) bool {
	return !strings.EqualFold(strings.TrimSpace(a.SourceHost), strings.TrimSpace(b.SourceHost)) ||
		a.SourcePort != b.SourcePort || a.SourceUsername != b.SourceUsername ||
		a.SourceUseTLS != b.SourceUseTLS
}

func sameIMAPEndpoint(item routine, account store.MailAccount) bool {
	return strings.EqualFold(strings.TrimSpace(item.SourceHost), strings.TrimSpace(account.Host)) &&
		item.SourcePort == account.Port &&
		strings.EqualFold(strings.TrimSpace(item.SourceUsername), strings.TrimSpace(account.Username)) &&
		item.SourceUseTLS == account.UseTLS
}

func (p *remoteIMAPSyncBackend) apiDiscoverSource(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in discoverInput
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	source, err := p.sourceAccountForDiscover(r.Context(), host, db, userID, in)
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	fetcher := p.currentFetcher(host.MasterKey())
	mailboxes, err := fetcher.ListMailboxes(r.Context(), source)
	if err != nil {
		host.WriteAPIError(w, http.StatusUnprocessableEntity, sanitizeRemoteError(err))
		return
	}
	items := make([]map[string]any, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		items = append(items, map[string]any{"name": mailbox.Name, "selectable": true})
	}
	response := map[string]any{"mailboxes": items}
	if capabilities, err := fetcher.ProbeCapabilities(r.Context(), source); err == nil {
		response["capabilities"] = map[string]bool{
			"idle": capabilities.IDLE, "uidplus": capabilities.UIDPlus,
		}
	}
	host.WriteJSON(w, response)
}

func (p *remoteIMAPSyncBackend) sourceAccountForDiscover(ctx context.Context, host plugins.APIHost, db *sql.DB, userID int64, in discoverInput) (store.MailAccount, error) {
	source := in.Source
	var encrypted string
	if source.Password == "" && in.RoutineID > 0 {
		item, err := getRoutine(ctx, db, userID, in.RoutineID)
		if err != nil {
			return store.MailAccount{}, fmt.Errorf("routine not found")
		}
		if !sourceInputMatchesStoredConnection(source, item) {
			return store.MailAccount{}, fmt.Errorf("source password is required when connection settings change")
		}
		encrypted = item.EncryptedSourcePassword
		source.Host = item.SourceHost
		source.Port = item.SourcePort
		source.Username = item.SourceUsername
		useTLS := item.SourceUseTLS
		source.UseTLS = &useTLS
	} else {
		password := source.Password
		if strings.EqualFold(source.Provider, "gmail") {
			password = strings.ReplaceAll(password, " ", "")
		}
		if password == "" {
			return store.MailAccount{}, fmt.Errorf("source password is required")
		}
		var err error
		encrypted, err = mmcrypto.EncryptString(host.MasterKey(), password)
		if err != nil {
			return store.MailAccount{}, fmt.Errorf("could not encrypt source password")
		}
	}
	security := strings.ToLower(strings.TrimSpace(source.Security))
	switch security {
	case "starttls":
		return store.MailAccount{}, fmt.Errorf("STARTTLS sources are not supported yet; use implicit TLS on port 993")
	case "", "tls", "ssl", "plain", "none":
	default:
		return store.MailAccount{}, fmt.Errorf("source security mode is invalid")
	}
	useTLS := true
	if source.UseTLS != nil {
		useTLS = *source.UseTLS
	} else if security == "plain" || security == "none" {
		useTLS = false
	}
	if strings.EqualFold(strings.TrimSpace(source.Provider), "gmail") && strings.TrimSpace(source.Host) == "" {
		source.Host = "imap.gmail.com"
	}
	if source.Port == 0 {
		source.Port = 993
	}
	if source.Port < 1 || source.Port > 65535 {
		return store.MailAccount{}, fmt.Errorf("source port is invalid")
	}
	if strings.TrimSpace(source.Host) == "" || strings.TrimSpace(source.Username) == "" {
		return store.MailAccount{}, fmt.Errorf("source host and username are required")
	}
	if !useTLS && !isLoopbackHost(source.Host) {
		return store.MailAccount{}, fmt.Errorf("TLS is required for remote IMAP sources")
	}
	return store.MailAccount{UserID: userID, Host: strings.TrimSpace(source.Host), Port: source.Port,
		Username: strings.TrimSpace(source.Username), EncryptedPassword: encrypted, UseTLS: useTLS}, nil
}

func sourceInputMatchesStoredConnection(source sourceInput, item routine) bool {
	if host := strings.TrimSpace(source.Host); host != "" && !strings.EqualFold(host, item.SourceHost) {
		return false
	}
	if source.Port != 0 && source.Port != item.SourcePort {
		return false
	}
	if username := strings.TrimSpace(source.Username); username != "" && username != item.SourceUsername {
		return false
	}
	if source.UseTLS != nil && *source.UseTLS != item.SourceUseTLS {
		return false
	}
	security := strings.ToLower(strings.TrimSpace(source.Security))
	if security == "starttls" {
		return false
	}
	if security == "tls" || security == "ssl" {
		return item.SourceUseTLS
	}
	if security == "plain" || security == "none" {
		return !item.SourceUseTLS
	}
	return true
}

func presentRoutine(ctx context.Context, st *store.Store, db *sql.DB, item routine) (routineView, error) {
	view := routineView{
		ID: item.ID, Name: item.Name, Enabled: item.Enabled,
		Source: sourceView{Provider: item.SourceProvider, Host: item.SourceHost, Port: item.SourcePort,
			Security: securityName(item.SourceUseTLS), UseTLS: item.SourceUseTLS,
			Username: item.SourceUsername, Mailbox: item.SourceMailbox,
			HasPassword: item.EncryptedSourcePassword != ""},
		Destination: destinationView{AccountID: item.DestinationAccountID, MailboxID: item.DestinationMailboxID},
		AfterDate:   formatAfterDate(item.AfterDate), State: item.State, LastError: item.LastError,
		LastStartedAt: unixOrZero(item.LastStartedAt), LastSuccessAt: unixOrZero(item.LastCompletedAt),
		LastActivityAt: unixOrZero(item.LastActivityAt), NextRetryAt: unixOrZero(item.NextRetryAt),
		TransferredTotal: item.TransferredTotal, SkippedTotal: item.SkippedTotal,
	}
	if account, err := st.GetMailAccountForUser(ctx, item.UserID, item.DestinationAccountID); err == nil {
		view.Destination.AccountLabel = account.Label
		view.Destination.AccountEmail = account.Email
	}
	if mailbox, err := st.GetMailboxForUser(ctx, item.UserID, item.DestinationMailboxID); err == nil && mailbox.AccountID == item.DestinationAccountID {
		view.Destination.MailboxName = mailbox.Name
	}
	latest, err := latestRun(ctx, db, item.UserID, item.ID)
	if err != nil {
		return routineView{}, err
	}
	view.LatestRun = latest
	if latest != nil && (latest.Status == "running" || latest.Status == "queued") {
		view.ActiveRun = latest
	}
	return view, nil
}

func listDestinationOptions(ctx context.Context, st *store.Store, userID int64) ([]destinationOption, error) {
	accounts, err := st.ListMailAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	mailboxes, err := st.ListMailboxesForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	byAccount := make(map[int64][]destinationFolder)
	for _, mailbox := range mailboxes {
		byAccount[mailbox.AccountID] = append(byAccount[mailbox.AccountID], destinationFolder{
			ID: mailbox.ID, Name: mailbox.Name, Role: mailbox.Role,
		})
	}
	out := make([]destinationOption, 0, len(accounts))
	for _, account := range accounts {
		label := strings.TrimSpace(account.Label)
		if label == "" {
			label = account.Email
		}
		out = append(out, destinationOption{ID: account.ID, Label: label, Email: account.Email,
			Folders: byAccount[account.ID]})
	}
	return out, nil
}

func (p *remoteIMAPSyncBackend) currentFetcher(masterKey []byte) *imapclient.Fetcher {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fetcher == nil {
		p.fetcher = &imapclient.Fetcher{MasterKey: masterKey, Timeout: time.Minute, BatchSize: remoteFetchBatchSize}
	}
	return p.fetcher
}

func (p *remoteIMAPSyncBackend) wakeManager() {
	p.mu.Lock()
	manager := p.manager
	p.mu.Unlock()
	if manager != nil {
		manager.Wake()
	}
}

func (p *remoteIMAPSyncBackend) triggerManager(userID, routineID int64, trigger string) bool {
	p.mu.Lock()
	manager := p.manager
	p.mu.Unlock()
	return manager != nil && manager.Trigger(userID, routineID, trigger)
}

func (p *remoteIMAPSyncBackend) mutateRoutine(userID, routineID int64, mutate func() error) error {
	p.mu.Lock()
	manager := p.manager
	p.mu.Unlock()
	if manager == nil {
		return mutate()
	}
	return manager.MutateRoutine(userID, routineID, mutate)
}

func parseAfterDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("after date must use YYYY-MM-DD")
	}
	return parsed.UTC(), nil
}

func formatAfterDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02")
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().Unix()
}

func securityName(useTLS bool) string {
	if useTLS {
		return "tls"
	}
	return "plain"
}

func sanitizeRemoteError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "destination mailbox"), strings.Contains(message, "destination folder"):
		return "The destination folder is no longer available."
	case strings.Contains(message, "destination account"):
		return "The destination account is no longer available."
	case strings.Contains(message, "authentication"), strings.Contains(message, "login"):
		return "IMAP authentication failed. Check the username and app password."
	case strings.Contains(message, "certificate"), strings.Contains(message, "tls"):
		return "The IMAP server's TLS connection could not be verified."
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "The IMAP server timed out."
	case strings.Contains(message, "no such host"), strings.Contains(message, "connection refused"):
		return "The IMAP server could not be reached."
	case errors.Is(err, context.Canceled):
		return "The IMAP operation was canceled."
	default:
		return "The IMAP server could not complete the request."
	}
}

func writeScopedError(host plugins.APIHost, w http.ResponseWriter, err error, notFound string) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrNotFound) {
		host.WriteAPIError(w, http.StatusNotFound, notFound)
		return
	}
	host.ServerError(w, err)
}
