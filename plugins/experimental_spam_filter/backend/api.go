package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"rolltop/backend/mailparse"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
	spammodel "rolltop/plugins/experimental_spam_filter/model"
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
	case rest == "bootstrap/preview" && r.Method == http.MethodPost:
		p.apiPreviewBootstrap(host, st, db, current.UserID, w, r)
	case rest == "bootstrap/start" && r.Method == http.MethodPost:
		p.apiStartBootstrap(host, st, db, current.UserID, w, r)
	case rest == "bootstrap/cancel" && r.Method == http.MethodPost:
		p.apiCancelBootstrap(host, db, current.UserID, w, r)
	case rest == "bootstrap/reset" && r.Method == http.MethodPost:
		p.apiResetBootstrap(host, db, current.UserID, w, r)
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
	bayes, err := GetPersonalBayesCounts(r.Context(), db, userID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	status.BayesReady = bayes.Ready
	status.BayesSpamLearned = bayes.Effective.Spam
	status.BayesHamLearned = bayes.Effective.Ham
	status.BayesExplicitSpam = bayes.Explicit.Spam
	status.BayesExplicitHam = bayes.Explicit.Ham
	status.BayesAutomaticSpam = bayes.Automatic.Spam
	status.BayesAutomaticHam = bayes.Automatic.Ham
	status.Bootstrap, err = getBootstrap(r.Context(), db, userID)
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
	if status.Bootstrap.Status == "running" && !p.bootstrapActive(userID) {
		if err := finishBootstrapRecord(r.Context(), db, userID, "cancelled", "bootstrap was interrupted before completion"); err != nil {
			host.ServerError(w, err)
			return
		}
		status.Bootstrap, err = getBootstrap(r.Context(), db, userID)
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
		status.ModelName = classifier.ModelName()
		status.TrainingCorpus = classifier.TrainingCorpus()
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
			p.apiSetFeedback(host, st, db, userID, messageID, w, r)
		case http.MethodDelete:
			p.apiClearFeedback(host, st, db, userID, messageID, w, r)
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
	classifier, version, _ := p.model()
	record.Stale = version == "" || record.ModelVersion != version
	if !record.Stale && classifier != nil {
		record.ModelName = classifier.ModelName()
		record.TrainingCorpus = classifier.TrainingCorpus()
	}
	host.WriteJSON(w, map[string]any{"classification": record, "feedback": record.Feedback})
}

func (p *spamFilterPlugin) apiSetFeedback(host plugins.APIHost, st *store.Store, db *sql.DB, userID, messageID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var input struct {
		Label string `json:"label"`
	}
	if !host.DecodeJSON(w, r, &input) {
		return
	}
	message, err := feedbackModelMessage(r.Context(), host, st, userID, messageID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err := LearnPersonalBayesTx(r.Context(), tx, userID, messageID, input.Label, message); err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := setFeedbackTx(r.Context(), tx, userID, messageID, input.Label); err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		host.ServerError(w, err)
		return
	}
	notifyUserChanged(host, userID)
	host.WriteJSON(w, map[string]any{"ok": true, "feedback": strings.ToLower(strings.TrimSpace(input.Label))})
}

func (p *spamFilterPlugin) apiClearFeedback(host plugins.APIHost, _ *store.Store, db *sql.DB, userID, messageID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	defer tx.Rollback()
	label, err := getFeedbackTx(r.Context(), tx, userID, messageID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	if label != "" {
		if _, err := UnlearnPersonalBayesTx(r.Context(), tx, userID, messageID, label, spammodel.Message{}); err != nil {
			host.WriteAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := clearFeedbackTx(r.Context(), tx, userID, messageID); err != nil {
		host.ServerError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		host.ServerError(w, err)
		return
	}
	notifyUserChanged(host, userID)
	host.WriteJSON(w, map[string]any{"ok": true, "feedback": ""})
}

func feedbackModelMessage(ctx context.Context, host plugins.BackendHost, st *store.Store, userID, messageID int64) (spammodel.Message, error) {
	message, err := st.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return spammodel.Message{}, err
	}
	attachments, err := st.ListAttachmentsForMessage(ctx, userID, messageID)
	if err != nil {
		return spammodel.Message{}, err
	}
	fallback := modelMessage(classificationInputFromStored(message, attachments))
	rawHost, ok := host.(plugins.RawMessageFetchHost)
	if !ok {
		return fallback, nil
	}
	raw, err := rawHost.FetchRawMessage(ctx, userID, messageID)
	if err != nil || len(raw) == 0 {
		return fallback, nil
	}
	full, err := feedbackModelMessageFromRaw(raw, message)
	if err != nil {
		return fallback, nil
	}
	return full, nil
}

func feedbackModelMessageFromRaw(raw []byte, stored store.MessageRecord) (spammodel.Message, error) {
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		return spammodel.Message{}, err
	}
	body := parsed.Text
	hasHTML := strings.TrimSpace(parsed.HTML) != ""
	attachmentTypes := make([]string, 0, len(parsed.Files))
	if parsed.IsEncrypted {
		body = ""
		hasHTML = false
	} else {
		for _, attachment := range parsed.Files {
			if contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType)); contentType != "" {
				attachmentTypes = append(attachmentTypes, contentType)
			}
		}
	}
	mimeType := "text/plain"
	if hasHTML {
		mimeType = "text/html"
	}
	return spammodel.Message{
		Subject:         firstFeedbackValue(parsed.Subject, stored.Subject),
		Body:            boundedText(body, maxBodyBytes),
		From:            firstFeedbackValue(parsed.From, stored.FromAddr),
		To:              recipientAddresses(firstFeedbackValue(parsed.To, stored.ToAddr), firstFeedbackValue(parsed.CC, stored.CCAddr)),
		MIMEType:        mimeType,
		AttachmentTypes: attachmentTypes,
		HTML:            hasHTML,
	}, nil
}

func firstFeedbackValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
