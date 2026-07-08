// File overview: Account, SMTP server, identity, folder settings, and setup API handlers.

package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type accountSettingsInput struct {
	ID                  int64  `json:"id"`
	Email               string `json:"email"`
	Label               string `json:"label"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	Password            string `json:"password"`
	UseTLS              bool   `json:"use_tls"`
	SMTPHost            string `json:"smtp_host"`
	SMTPPort            int    `json:"smtp_port"`
	SMTPUsername        string `json:"smtp_username"`
	SMTPPassword        string `json:"smtp_password"`
	SMTPUseTLS          bool   `json:"smtp_use_tls"`
	SMTPSameAsIMAP      bool   `json:"smtp_same_as_imap"`
	Mailbox             string `json:"mailbox"`
	SyncIntervalMinutes int    `json:"sync_interval_minutes"`
}

// apiAccount is the settings dashboard endpoint. It returns the account graph
// needed by the React settings page; writes happen through the IMAP/SMTP/identity
// endpoints so account-server editing stays explicit.
func (s *Server) apiAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	accounts, err := s.store.ListMailAccountsForUser(r.Context(), cu.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	accounts = s.filterDeletingAccounts(cu.User.ID, accounts)
	if len(accounts) > 0 && s.syncer != nil && s.syncer.Fetcher != nil {
		s.refreshMailboxStatusesAsync(cu.User.ID)
	}
	smtpAccounts, err := s.store.ListSMTPAccountsForUser(r.Context(), cu.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	identities, err := s.store.ListMailIdentitiesForUser(r.Context(), cu.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	meContacts, err := s.store.ListMeContactsForUser(r.Context(), cu.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	runs, err := s.store.ListSyncRunsForUser(r.Context(), cu.User.ID, 20)
	if err != nil {
		s.serverError(w, err)
		return
	}
	needsPassword, notice := s.accountCredentialNotice(r.Context(), cu.User.ID)
	writeJSONCached(w, r, map[string]any{
		"imap_accounts":          apiAccountsFromStore(accounts),
		"smtp_accounts":          apiSMTPAccountsFromStore(smtpAccounts),
		"identities":             apiMailIdentitiesFromStore(identities),
		"me_contacts":            apiContactsFromStore(meContacts),
		"sync_runs":              apiSyncRuns(runs),
		"sync_folders":           apiSyncFolders(s.syncFolderViews(r.Context(), cu.User.ID, runs)),
		"notice":                 notice,
		"account_needs_password": needsPassword,
	})
}

// apiIMAPAccount saves one explicit IMAP server page. It validates ownership via
// user-scoped store lookups and then runs onboarding so SMTP/identity records exist.
func (s *Server) apiIMAPAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	var in accountSettingsInput
	if !decodeJSON(w, r, &in) {
		return
	}
	account, msg, err := s.saveMailAccountFromInput(r.Context(), cu.User.ID, in)
	if err != nil {
		if msg != "" {
			writeAPIError(w, http.StatusBadRequest, msg)
			return
		}
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		writeAPIError(w, http.StatusBadRequest, "Could not save IMAP account.")
		return
	}
	if err := s.ensureMailAccountOnboarding(r.Context(), cu.User, account); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearComposeIdentityCache(cu.User.ID)
	writeJSON(w, map[string]any{"ok": true, "account": apiAccountFromStore(account)})
}

func (s *Server) apiIMAPAccountPath(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	accountID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || accountID <= 0 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "folders":
		s.apiCreateIMAPFolder(w, r, accountID)
	case "purge-estimate":
		s.apiIMAPAccountPurgeEstimate(w, r, accountID)
	case "delete":
		s.apiDeleteIMAPAccount(w, r, accountID)
	default:
		http.NotFound(w, r)
	}
}

type createIMAPFolderInput struct {
	Name string `json:"name"`
}

func (s *Server) apiCreateIMAPFolder(w http.ResponseWriter, r *http.Request, accountID int64) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if s.syncer == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "IMAP sync is not configured.")
		return
	}
	account, err := s.store.GetMailAccountForUser(r.Context(), cu.User.ID, accountID)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	var in createIMAPFolderInput
	if !decodeJSON(w, r, &in) {
		return
	}
	mb, err := s.syncer.CreateRemoteFolder(r.Context(), cu.User.ID, accountID, in.Name)
	if err != nil {
		switch {
		case errors.Is(err, syncer.ErrFolderExists):
			writeAPIError(w, http.StatusConflict, err.Error())
		case errors.Is(err, syncer.ErrRemoteFolderCreateUnsupported):
			writeAPIError(w, http.StatusServiceUnavailable, "This IMAP connection cannot create folders.")
		default:
			writeAPIError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	s.notifyUserChanged(cu.User.ID)
	writeJSON(w, map[string]any{"ok": true, "mailbox": apiMailboxFromStoreForAccount(mb, account)})
}

func (s *Server) apiIMAPAccountPurgeEstimate(w http.ResponseWriter, r *http.Request, accountID int64) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	estimate, err := s.accountPurgeEstimate(r.Context(), cu.User.ID, accountID)
	if err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	writeJSON(w, estimate)
}

type deleteIMAPAccountInput struct {
	Confirm string `json:"confirm"`
}

func (s *Server) accountPurgeEstimate(ctx context.Context, userID, accountID int64) (map[string]any, error) {
	estimate, err := s.store.AccountPurgeEstimate(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	searchCount := 0
	if s.search != nil {
		mailboxes, err := s.store.ListMailboxesForAccount(ctx, userID, accountID)
		if err != nil {
			return nil, err
		}
		for _, mailbox := range mailboxes {
			count, err := s.search.CountMailboxMessages(ctx, userID, mailbox.ID)
			if err != nil {
				return nil, err
			}
			searchCount += count
		}
	}
	return map[string]any{
		"account_id":         estimate.Account.ID,
		"account_name":       accountDeleteConfirmationName(estimate.Account),
		"account_email":      estimate.Account.Email,
		"mailbox_count":      estimate.MailboxCount,
		"message_count":      estimate.MessageCount,
		"blob_count":         estimate.BlobCount,
		"blob_bytes":         estimate.BlobBytes,
		"search_index_count": searchCount,
	}, nil
}

func accountDeleteConfirmationName(account store.MailAccount) string {
	if strings.TrimSpace(account.Label) != "" {
		return strings.TrimSpace(account.Label)
	}
	if strings.TrimSpace(account.Email) != "" {
		return strings.TrimSpace(account.Email)
	}
	return strings.TrimSpace(account.Host)
}

func (s *Server) apiDeleteIMAPAccount(w http.ResponseWriter, r *http.Request, accountID int64) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	var in deleteIMAPAccountInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if s.imapAccountDeleting(cu.User.ID, accountID) {
		writeAPIError(w, http.StatusConflict, "That IMAP server is already being deleted.")
		return
	}
	account, err := s.store.GetMailAccountForUser(r.Context(), cu.User.ID, accountID)
	if err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	expected := accountDeleteConfirmationName(account)
	if strings.TrimSpace(in.Confirm) != expected {
		writeAPIError(w, http.StatusBadRequest, "Confirmation did not match the IMAP server name.")
		return
	}
	if s.syncer == nil || s.syncRunner == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "Account deletion requires the local sync service.")
		return
	}
	mailboxes, err := s.store.ListMailboxesForAccount(r.Context(), cu.User.ID, accountID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, mailbox := range mailboxes {
		if s.syncRunner.IsAccountMailboxRunning(cu.User.ID, accountID, mailbox.Name) {
			writeAPIError(w, http.StatusConflict, "Sync or maintenance is already running for this IMAP server.")
			return
		}
	}
	estimate, err := s.accountPurgeEstimate(r.Context(), cu.User.ID, accountID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.markDeletingIMAPAccount(cu.User.ID, accountID)
	run, started, err := s.syncRunner.StartAccountMaintenance(cu.User.ID, account, mailboxes, "Deleting local IMAP account data", func(ctx context.Context, runID int64, progress *store.SyncProgress) error {
		defer s.clearDeletingIMAPAccount(cu.User.ID, accountID)
		if _, err := s.syncer.PurgeAccountLocalDataWithProgress(ctx, cu.User.ID, account, mailboxes, runID, progress); err != nil {
			return err
		}
		if err := s.store.ClearIdentityMailboxRefsForAccount(ctx, cu.User.ID, accountID); err != nil {
			return err
		}
		if err := s.store.DeleteMailAccountForUser(ctx, cu.User.ID, accountID); err != nil {
			return err
		}
		s.clearComposeIdentityCache(cu.User.ID)
		s.notifyUserChanged(cu.User.ID)
		return nil
	})
	if !started && err == nil {
		s.clearDeletingIMAPAccount(cu.User.ID, accountID)
		writeAPIError(w, http.StatusConflict, "Sync or maintenance is already running for this IMAP server.")
		return
	}
	if err != nil {
		s.clearDeletingIMAPAccount(cu.User.ID, accountID)
		writeAPIError(w, http.StatusBadGateway, "Could not start IMAP server deletion.")
		return
	}
	s.notifyUserChanged(cu.User.ID)
	writeJSON(w, map[string]any{"ok": true, "queued": true, "run_id": run.ID, "estimate": estimate})
}

// saveMailAccountFromInput normalizes account form data, preserves encrypted
// passwords when the user leaves password fields blank, and returns friendly
// validation text when the current master key cannot decrypt a saved password.
func (s *Server) saveMailAccountFromInput(ctx context.Context, userID int64, in accountSettingsInput) (store.MailAccount, string, error) {
	in.Email = strings.TrimSpace(in.Email)
	in.Label = strings.TrimSpace(in.Label)
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	in.Mailbox = strings.TrimSpace(in.Mailbox)
	if in.Username == "" {
		in.Username = in.Email
	}
	if in.Label == "" {
		in.Label = firstNonEmpty(in.Email, in.Username, in.Host)
	}
	if in.Mailbox == "" {
		in.Mailbox = store.DefaultMailboxPattern
	}
	if in.SMTPSameAsIMAP && in.SMTPPort == 0 {
		in.SMTPPort = 587
	}
	if in.SMTPPort == 0 {
		in.SMTPPort = 587
	}
	if in.Port <= 0 || in.Port > 65535 || in.SMTPPort <= 0 || in.SMTPPort > 65535 {
		return store.MailAccount{}, "Ports must be valid TCP ports.", store.ErrNotFound
	}
	var existing store.MailAccount
	existingErr := store.ErrNotFound
	if in.ID > 0 {
		existing, existingErr = s.store.GetMailAccountForUser(ctx, userID, in.ID)
	}
	if existingErr != nil && !store.IsNotFound(existingErr) {
		return store.MailAccount{}, "", existingErr
	}
	encrypted := ""
	if in.Password == "" && existingErr == nil {
		if _, err := mmcrypto.DecryptString(s.masterKey, existing.EncryptedPassword); err != nil {
			return store.MailAccount{}, "Saved IMAP password cannot be decrypted with the current master key. Re-enter the IMAP password.", err
		}
		encrypted = existing.EncryptedPassword
	} else if in.Password != "" {
		var err error
		encrypted, err = mmcrypto.EncryptString(s.masterKey, in.Password)
		if err != nil {
			return store.MailAccount{}, "", err
		}
	} else {
		return store.MailAccount{}, "IMAP password is required for a new account.", store.ErrNotFound
	}
	encryptedSMTP := ""
	if in.SMTPPassword == "" && existingErr == nil {
		encryptedSMTP = existing.EncryptedSMTPPassword
	}
	if encryptedSMTP == "" && in.SMTPPassword == "" {
		encryptedSMTP = encrypted
	}
	if in.SMTPPassword != "" {
		var err error
		encryptedSMTP, err = mmcrypto.EncryptString(s.masterKey, in.SMTPPassword)
		if err != nil {
			return store.MailAccount{}, "", err
		}
	}
	if in.SMTPSameAsIMAP {
		in.SMTPHost = in.Host
		in.SMTPUsername = in.Username
		in.SMTPUseTLS = in.UseTLS
		encryptedSMTP = encrypted
		if in.SMTPPort <= 0 {
			in.SMTPPort = 587
		}
	}
	account := store.MailAccount{
		ID:                    in.ID,
		UserID:                userID,
		Email:                 in.Email,
		Label:                 in.Label,
		Host:                  in.Host,
		Port:                  in.Port,
		Username:              in.Username,
		EncryptedPassword:     encrypted,
		UseTLS:                in.UseTLS,
		SMTPHost:              in.SMTPHost,
		SMTPPort:              in.SMTPPort,
		SMTPUsername:          in.SMTPUsername,
		EncryptedSMTPPassword: encryptedSMTP,
		SMTPUseTLS:            in.SMTPUseTLS,
		Mailbox:               in.Mailbox,
		SyncIntervalMinutes:   in.SyncIntervalMinutes,
	}
	if in.ID == 0 {
		saved, err := s.store.CreateMailAccount(ctx, account)
		return saved, "", err
	}
	saved, err := s.store.UpsertMailAccount(ctx, account)
	return saved, "", err
}

// ensureMailAccountOnboarding fills in the expected single-address startup graph:
// a matching SMTP server when none exists, a Me contact for the account email, and
// downstream identity rows through store-level sync helpers.
func (s *Server) ensureMailAccountOnboarding(ctx context.Context, user store.User, account store.MailAccount) error {
	smtpAccounts, err := s.store.ListSMTPAccountsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(smtpAccounts) == 0 {
		password := account.EncryptedSMTPPassword
		if strings.TrimSpace(password) == "" {
			password = account.EncryptedPassword
		}
		if strings.TrimSpace(password) != "" {
			if _, err := s.store.CreateSMTPAccount(ctx, store.SMTPAccount{
				UserID:            user.ID,
				Label:             firstNonEmpty(account.Label, account.Email, account.Username),
				Host:              inferredSMTPHost(account),
				Port:              firstPositive(account.SMTPPort, 587),
				Username:          firstNonEmpty(account.SMTPUsername, account.Username, account.Email),
				EncryptedPassword: password,
				UseTLS:            account.SMTPUseTLS,
			}); err != nil {
				return err
			}
		}
	}
	if _, err := s.store.EnsureMeContactForEmail(ctx, user.ID, account.Email, firstNonEmpty(user.Name, account.Label, account.Email)); err != nil && !store.IsNotFound(err) {
		return err
	}
	return s.store.EnsureMailIdentityMailboxDefaults(ctx, user.ID)
}

// inferredSMTPHost converts common imap.example.com hosts to smtp.example.com
// during onboarding unless the user provided a distinct SMTP host.
func inferredSMTPHost(account store.MailAccount) string {
	host := strings.TrimSpace(account.SMTPHost)
	if host == "" || strings.EqualFold(host, strings.TrimSpace(account.Host)) {
		if inferred := inferSMTPHostFromIMAP(strings.TrimSpace(account.Host)); inferred != "" {
			return inferred
		}
	}
	return host
}

func inferSMTPHostFromIMAP(host string) string {
	lower := strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(lower, "imap.") && len(host) > len("imap.") {
		return "smtp." + host[len("imap."):]
	}
	return strings.TrimSpace(host)
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (s *Server) apiSMTPAccountPath(w http.ResponseWriter, r *http.Request, path string) {
	id, err := strconv.ParseInt(strings.Trim(path, "/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	s.apiDeleteSMTPAccount(w, r, id)
}

func (s *Server) apiDeleteSMTPAccount(w http.ResponseWriter, r *http.Request, accountID int64) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if err := s.store.DeleteSMTPAccountForUser(r.Context(), cu.User.ID, accountID); err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	s.clearComposeIdentityCache(cu.User.ID)
	writeJSON(w, map[string]any{"ok": true})
}

// apiSMTPAccount saves an outgoing server. Identities are managed separately so
// multiple Me addresses can point at the same SMTP account.
func (s *Server) apiSMTPAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	var in struct {
		ID       int64  `json:"id"`
		Label    string `json:"label"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		UseTLS   bool   `json:"use_tls"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Port <= 0 || in.Port > 65535 {
		writeAPIError(w, http.StatusBadRequest, "Port must be a valid TCP port.")
		return
	}
	encrypted := ""
	if in.ID > 0 {
		existing, err := s.store.GetSMTPAccountForUser(r.Context(), cu.User.ID, in.ID)
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		encrypted = existing.EncryptedPassword
	}
	if in.Password != "" {
		var err error
		encrypted, err = mmcrypto.EncryptString(s.masterKey, in.Password)
		if err != nil {
			s.serverError(w, err)
			return
		}
	}
	account, err := s.store.UpsertSMTPAccount(r.Context(), store.SMTPAccount{
		ID:                in.ID,
		UserID:            cu.User.ID,
		Label:             in.Label,
		Host:              in.Host,
		Port:              in.Port,
		Username:          in.Username,
		EncryptedPassword: encrypted,
		UseTLS:            in.UseTLS,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Could not save SMTP account.")
		return
	}
	s.clearComposeIdentityCache(cu.User.ID)
	writeJSON(w, map[string]any{"ok": true, "smtp_account": apiSMTPAccountFromStore(account)})
}

// apiMailIdentity creates or updates a Me-contact-backed outgoing identity:
// server choices, display name, primary flag, folder choices, and signature line.
func (s *Server) apiMailIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	var in apiMailIdentity
	if !decodeJSON(w, r, &in) {
		return
	}
	identityInput := store.MailIdentity{
		ID:               in.ID,
		SMTPAccountID:    in.SMTPAccountID,
		IMAPAccountID:    in.IMAPAccountID,
		SentMailboxID:    in.SentMailboxID,
		DraftsMailboxID:  in.DraftsMailboxID,
		Email:            in.Email,
		DisplayName:      in.DisplayName,
		Signature:        in.Signature,
		AutocryptEnabled: in.AutocryptEnabled,
		IsPrimary:        in.IsPrimary,
	}
	var identity store.MailIdentity
	var err error
	if in.ID == 0 {
		identity, err = s.store.CreateMailIdentityForUser(r.Context(), cu.User.ID, identityInput)
	} else {
		identity, err = s.store.UpdateMailIdentityForUser(r.Context(), cu.User.ID, identityInput)
	}
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Could not save identity.")
		return
	}
	s.clearComposeIdentityCache(cu.User.ID)
	identities, err := s.store.ListMailIdentitiesForUser(r.Context(), cu.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "identity": apiMailIdentityFromStore(identity), "identities": apiMailIdentitiesFromStore(identities)})
}

func (s *Server) apiAccountSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if s.syncRunner == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "Sync is not configured.")
		return
	}
	if !s.syncRunner.Start(cu.User.ID) {
		writeAPIError(w, http.StatusConflict, "Sync is already running for this account.")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiAccountFolder handles small folder-setting actions from the settings page.
// It keeps each mailbox user-scoped, and when search visibility changes it starts
// an asynchronous index reconcile rather than blocking the request.
func (s *Server) apiAccountFolder(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	mailboxID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || mailboxID <= 0 {
		http.NotFound(w, r)
		return
	}
	mb, err := s.store.GetMailboxForUser(r.Context(), cu.User.ID, mailboxID)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	action := parts[1]
	if len(parts) == 3 && parts[1] == "search-index" && parts[2] == "purge" {
		action = "purge-search-index"
	} else if len(parts) == 3 && parts[1] == "local-references" && parts[2] == "purge" {
		action = "purge-local-references"
	} else if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "mode":
		var in struct {
			SyncMode string `json:"sync_mode"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		if err := s.store.UpdateMailboxSyncMode(r.Context(), cu.User.ID, mailboxID, in.SyncMode); err != nil {
			s.serverError(w, err)
			return
		}
		s.notifyUserChanged(cu.User.ID)
		writeJSON(w, map[string]any{"ok": true})
	case "settings":
		var in struct {
			SyncMode        string `json:"sync_mode"`
			Role            string `json:"role"`
			Icon            string `json:"icon"`
			ShowInSidebar   bool   `json:"show_in_sidebar"`
			ShowInAllMail   bool   `json:"show_in_all_mail"`
			IncludeInSearch bool   `json:"include_in_search"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		previousInclude := mb.IncludeInSearch
		if err := s.store.UpdateMailboxSettings(r.Context(), cu.User.ID, mailboxID, store.MailboxSettings{
			SyncMode:        in.SyncMode,
			Role:            in.Role,
			Icon:            in.Icon,
			ShowInSidebar:   in.ShowInSidebar,
			ShowInAllMail:   in.ShowInAllMail,
			IncludeInSearch: in.IncludeInSearch,
		}); err != nil {
			if errors.Is(err, store.ErrDuplicateMailboxRole) {
				writeAPIError(w, http.StatusConflict, err.Error())
				return
			}
			if errors.Is(err, store.ErrInvalidMailboxSettings) {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.serverError(w, err)
			return
		}
		if previousInclude != in.IncludeInSearch && s.syncer != nil {
			go func(userID, mailboxID int64, include bool) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if err := s.syncer.ReconcileMailboxSearchIndex(ctx, userID, mailboxID, include); err != nil {
					log.Printf("reconcile mailbox search index user_id=%d mailbox_id=%d include=%t: %v", userID, mailboxID, include, err)
				}
				s.notifyUserChanged(userID)
			}(cu.User.ID, mailboxID, in.IncludeInSearch)
		}
		s.notifyUserChanged(cu.User.ID)
		writeJSON(w, map[string]any{"ok": true})
	case "sync":
		effectiveMode := mb.SyncMode
		if effectiveMode == "inherit" {
			if mode, err := s.store.EffectiveMailboxSyncMode(r.Context(), cu.User.ID, mb.AccountID, mb); err == nil {
				effectiveMode = mode
			}
		}
		if strings.EqualFold(effectiveMode, "never") {
			writeAPIError(w, http.StatusBadRequest, "That folder is set to never sync.")
			return
		}
		if s.syncRunner == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "Sync is not configured.")
			return
		}
		if !s.syncRunner.StartAccountMailboxes(cu.User.ID, mb.AccountID, []string{mb.Name}) {
			writeAPIError(w, http.StatusConflict, "Sync is already running for this folder.")
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case "purge-search-index":
		if s.syncer == nil || s.syncRunner == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "Search indexing is not configured.")
			return
		}
		run, started, err := s.syncRunner.StartMailboxMaintenance(cu.User.ID, mb, "Purging full-text index", func(ctx context.Context, runID int64, progress *store.SyncProgress) error {
			_, err := s.syncer.PurgeMailboxSearchIndexWithProgress(ctx, cu.User.ID, mb.ID, runID, progress)
			return err
		})
		if !started && err == nil {
			writeAPIError(w, http.StatusConflict, "Sync or purge is already running for this folder.")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "could not start search index purge")
			return
		}
		s.notifyUserChanged(cu.User.ID)
		writeJSON(w, map[string]any{"ok": true, "queued": true, "run_id": run.ID})
	case "purge-local-references":
		if s.syncer == nil || s.syncRunner == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "Sync is not configured.")
			return
		}
		run, started, err := s.syncRunner.StartMailboxMaintenance(cu.User.ID, mb, "Purging local references and full-text index", func(ctx context.Context, runID int64, progress *store.SyncProgress) error {
			_, err := s.syncer.PurgeMailboxLocalReferencesWithProgress(ctx, cu.User.ID, mb.ID, runID, progress)
			return err
		})
		if !started && err == nil {
			writeAPIError(w, http.StatusConflict, "Sync or purge is already running for this folder.")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "could not start local references purge")
			return
		}
		s.notifyUserChanged(cu.User.ID)
		writeJSON(w, map[string]any{"ok": true, "queued": true, "run_id": run.ID})
	default:
		http.NotFound(w, r)
	}
}
