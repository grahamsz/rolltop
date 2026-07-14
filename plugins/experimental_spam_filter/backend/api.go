package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func (p *spamFilterPlugin) handleAPI(host plugins.APIHost, path string, w http.ResponseWriter, r *http.Request) {
	current, ok := host.RequireAPIAuth(w, r)
	if !ok {
		return
	}
	st, db, err := pluginUserDB(r.Context(), host, current.UserID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(path, apiPath), "/")
	switch {
	case rest == "status" && r.Method == http.MethodGet:
		p.apiStatus(host, db, current.UserID, w, r)
	case rest == "backfill" && r.Method == http.MethodPost:
		p.apiStartBackfill(host, db, current.UserID, w, r)
	case strings.HasPrefix(rest, "messages/"):
		p.apiMessage(host, st, db, current.UserID, rest, w, r)
	default:
		host.WriteAPIError(w, http.StatusNotFound, "spam filter route not found")
	}
}

func (p *spamFilterPlugin) apiStatus(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	status, err := statusCounts(r.Context(), db, userID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	if status.Backfill.Status == "running" && !p.backfillActive(userID) {
		if err := finishBackfillRecord(r.Context(), db, userID, "cancelled", "backfill was interrupted before completion"); err != nil {
			host.ServerError(w, err)
			return
		}
		status.Backfill, err = getBackfill(r.Context(), db, userID)
		if err != nil {
			host.ServerError(w, err)
			return
		}
	}
	classifier, version, modelError := p.model()
	status.ModelAvailable = classifier != nil
	status.ModelVersion = version
	status.ModelError = modelError
	if classifier != nil {
		status.Stale, err = staleClassificationCount(r.Context(), db, userID, version)
		if err != nil {
			host.ServerError(w, err)
			return
		}
	}
	host.WriteJSON(w, status)
}

func (p *spamFilterPlugin) apiMessage(host plugins.APIHost, st *store.Store, db *sql.DB, userID int64, rest string, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 || parts[0] != "messages" {
		host.WriteAPIError(w, http.StatusNotFound, "spam filter message route not found")
		return
	}
	messageID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || messageID <= 0 {
		host.WriteAPIError(w, http.StatusBadRequest, "invalid message id")
		return
	}
	if _, err := st.GetMessageEnvelopeForUser(r.Context(), userID, messageID); err != nil {
		if store.IsNotFound(err) {
			host.WriteAPIError(w, http.StatusNotFound, "message not found")
			return
		}
		host.ServerError(w, err)
		return
	}

	if len(parts) == 2 && r.Method == http.MethodGet {
		p.apiClassificationDetail(host, db, userID, messageID, w, r)
		return
	}
	if len(parts) == 3 && parts[2] == "feedback" {
		switch r.Method {
		case http.MethodPost:
			p.apiSetFeedback(host, db, userID, messageID, w, r)
		case http.MethodDelete:
			p.apiClearFeedback(host, db, userID, messageID, w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	host.WriteAPIError(w, http.StatusNotFound, "spam filter message route not found")
}

func (p *spamFilterPlugin) apiClassificationDetail(host plugins.APIHost, db *sql.DB, userID, messageID int64, w http.ResponseWriter, r *http.Request) {
	record, err := getClassification(r.Context(), db, userID, messageID)
	if errors.Is(err, sql.ErrNoRows) {
		feedback, feedbackErr := getFeedback(r.Context(), db, userID, messageID)
		if feedbackErr != nil {
			host.ServerError(w, feedbackErr)
			return
		}
		host.WriteJSON(w, map[string]any{"classification": nil, "feedback": feedback})
		return
	}
	if err != nil {
		host.ServerError(w, err)
		return
	}
	_, version, _ := p.model()
	record.Stale = version == "" || record.ModelVersion != version
	host.WriteJSON(w, map[string]any{"classification": record, "feedback": record.Feedback})
}

func (p *spamFilterPlugin) apiSetFeedback(host plugins.APIHost, db *sql.DB, userID, messageID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var input struct {
		Label string `json:"label"`
	}
	if !host.DecodeJSON(w, r, &input) {
		return
	}
	if err := setFeedback(r.Context(), db, userID, messageID, input.Label); err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	notifyUserChanged(host, userID)
	host.WriteJSON(w, map[string]any{"ok": true, "feedback": strings.ToLower(strings.TrimSpace(input.Label))})
}

func (p *spamFilterPlugin) apiClearFeedback(host plugins.APIHost, db *sql.DB, userID, messageID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	if err := clearFeedback(r.Context(), db, userID, messageID); err != nil {
		host.ServerError(w, err)
		return
	}
	notifyUserChanged(host, userID)
	host.WriteJSON(w, map[string]any{"ok": true, "feedback": ""})
}

func (p *spamFilterPlugin) apiStartBackfill(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	classificationHost, ok := host.(plugins.MessageClassificationHost)
	if !ok {
		host.WriteAPIError(w, http.StatusServiceUnavailable, "spam classification is not available")
		return
	}
	classifier, version, _ := p.model()
	if classifier == nil {
		host.WriteAPIError(w, http.StatusServiceUnavailable, "the checked-in spam model is not available")
		return
	}
	var input struct {
		Limit int `json:"limit"`
	}
	if !host.DecodeJSON(w, r, &input) {
		return
	}
	if input.Limit <= 0 {
		input.Limit = 500
	}
	if input.Limit > 2000 {
		input.Limit = 2000
	}
	if !p.reserveBackfill(userID) {
		host.WriteAPIError(w, http.StatusConflict, "a spam-filter backfill is already running")
		return
	}
	if err := startBackfillRecord(r.Context(), db, userID, version, input.Limit); err != nil {
		p.releaseBackfill(userID)
		host.ServerError(w, err)
		return
	}
	p.launchBackfill(classificationHost, userID, input.Limit)
	host.WriteJSON(w, map[string]any{"ok": true, "status": "running", "requested": input.Limit})
}

func notifyUserChanged(host plugins.BackendHost, userID int64) {
	if changeHost, ok := host.(plugins.UserChangeHost); ok {
		changeHost.NotifyUserChanged(userID)
	}
}
