// File overview: Login, logout, setup, CSRF, and session API handlers.

package web

import (
	"net/http"
	"time"

	"mailmirror/backend/auth"
	mmcrypto "mailmirror/backend/crypto"
	"mailmirror/backend/store"
)

func (s *Server) apiBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	resp := map[string]any{
		"users_exist":           s.usersExist(r.Context()),
		"csrf":                  s.csrfToken(w, r),
		"server_started_at":     timeString(s.startedAt),
		"server_uptime_seconds": int(time.Since(s.startedAt).Seconds()),
	}
	if cu, ok := current(r); ok {
		resp["user"] = safeUser(cu.User)
		var chrome viewData
		s.loadMailboxChrome(r.Context(), cu.User.ID, &chrome)
		resp["mailboxes"] = apiMailboxes(chrome.Mailboxes)
		resp["latest_sync_run"] = apiSyncRunPtr(chrome.LatestSyncRun)
		resp["active_sync_runs"] = apiSyncRuns(chrome.ActiveSyncRuns)
		resp["sync_running"] = chrome.SyncRunning
		needsPassword, notice := s.accountCredentialNotice(r.Context(), cu.User.ID)
		resp["account_needs_password"] = needsPassword
		resp["account_notice"] = notice
		if settings, err := s.store.ListPluginSettings(r.Context()); err == nil {
			enabled := make([]string, 0, len(settings))
			for _, setting := range settings {
				if setting.Enabled {
					enabled = append(enabled, setting.ID)
				}
			}
			resp["enabled_plugins"] = enabled
		}
	} else {
		resp["user"] = nil
		resp["mailboxes"] = []apiMailbox{}
		resp["active_sync_runs"] = []apiSyncRun{}
		resp["account_needs_password"] = false
		resp["account_notice"] = ""
		resp["enabled_plugins"] = []string{}
	}
	writeJSON(w, resp)
}

func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.usersExist(r.Context()) {
		writeAPIError(w, http.StatusConflict, "setup is already complete")
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	var in struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Password) < 12 {
		writeAPIError(w, http.StatusBadRequest, "Password must be at least 12 characters.")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	user, err := s.store.CreateUser(r.Context(), in.Email, in.Name, hash, true)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Could not create admin user.")
		return
	}
	if _, err := s.store.EnsureMeContactForEmail(r.Context(), user.ID, user.Email, firstNonEmpty(user.Name, user.Email)); err != nil && !store.IsNotFound(err) {
		s.serverError(w, err)
		return
	}
	if err := s.loginUser(w, r, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.usersExist(r.Context()) {
		writeAPIError(w, http.StatusPreconditionRequired, "setup is required")
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	user, err := s.store.GetUserByEmail(r.Context(), in.Email)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}
	ok, err := auth.VerifyPassword(user.PasswordHash, in.Password)
	if err != nil || !ok {
		writeAPIError(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}
	if err := s.loginUser(w, r, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), mmcrypto.TokenHash(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cookieSecure})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiProfile(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"user": safeUser(cu.User)})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		in := struct {
			DateLocale             string `json:"date_locale"`
			DateFormat             string `json:"date_format"`
			Theme                  string `json:"theme"`
			SearchPreset           string `json:"search_preset"`
			SearchRecencyBias      string `json:"search_recency_bias"`
			SearchFuzzy            string `json:"search_fuzzy"`
			SearchSenderBoost      bool   `json:"search_sender_boost"`
			SearchSenderHistory    string `json:"search_sender_history"`
			SearchContactBoost     string `json:"search_contact_boost"`
			SearchAttachmentWeight string `json:"search_attachment_weight"`
			SearchCompactSplitting bool   `json:"search_compact_splitting"`
		}{
			DateLocale:             cu.User.DateLocale,
			DateFormat:             cu.User.DateFormat,
			Theme:                  cu.User.Theme,
			SearchPreset:           cu.User.SearchPreset,
			SearchRecencyBias:      cu.User.SearchRecencyBias,
			SearchFuzzy:            cu.User.SearchFuzzy,
			SearchSenderBoost:      cu.User.SearchSenderBoost,
			SearchSenderHistory:    cu.User.SearchSenderHistory,
			SearchContactBoost:     cu.User.SearchContactBoost,
			SearchAttachmentWeight: cu.User.SearchAttachmentWeight,
			SearchCompactSplitting: cu.User.SearchCompactSplitting,
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		user, err := s.store.UpdateUserPreferences(r.Context(), cu.User.ID, in.DateLocale, in.DateFormat, in.Theme, in.SearchPreset, in.SearchRecencyBias, in.SearchFuzzy, in.SearchSenderHistory, in.SearchContactBoost, in.SearchAttachmentWeight, in.SearchSenderBoost, in.SearchCompactSplitting)
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.notifyUserChanged(cu.User.ID)
		writeJSON(w, map[string]any{"user": safeUser(user)})
	default:
		methodNotAllowed(w)
	}
}
