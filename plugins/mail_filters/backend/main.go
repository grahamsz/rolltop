// File overview: Runtime backend plugin for search-driven mail filters.

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const (
	apiPath          = "plugins/mail_filters"
	retentionWindow  = 30 * 24 * time.Hour
	scheduledBatch   = 100
	backfillBatch    = 2000
	forwarderHeader  = "X-Rolltop-Forwarded-By"
	statusScheduled  = "scheduled"
	statusMatched    = "matched"
	statusNotMatched = "not_matched"
	statusSkipped    = "skipped_scope"
	statusFailed     = "action_failed"
	statusLoop       = "loop_prevented"
	pluginID         = "mail_filters"
)

type mailFiltersBackend struct {
	mu     sync.Mutex
	routes []plugins.ProtectedAPIRouteHandle
	cancel context.CancelFunc
}

// RolltopPlugin is the symbol loaded by plugin.Open.
func RolltopPlugin() plugins.BackendPlugin {
	return &mailFiltersBackend{}
}

func (mailFiltersBackend) ID() string { return pluginID }

func (p *mailFiltersBackend) Start(host plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unregisterRoutesLocked()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	route := plugins.ProtectedAPIRoute{Path: apiPath, Prefix: true, Handle: p.handleAPI}
	handle, err := host.RegisterProtectedAPI(p.ID(), route)
	if err != nil {
		return err
	}
	p.routes = append(p.routes, handle)
	if filterHost, ok := host.(plugins.StoredMessageHost); ok {
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		go scheduledLoop(ctx, filterHost)
	}
	return nil
}

func (p *mailFiltersBackend) Stop(plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.unregisterRoutesLocked()
	return nil
}

func (p *mailFiltersBackend) unregisterRoutesLocked() {
	for _, handle := range p.routes {
		handle.Unregister()
	}
	p.routes = nil
}

func scheduledLoop(ctx context.Context, host plugins.StoredMessageHost) {
	runAll := func() {
		st, ok := host.Store().(*store.Store)
		if !ok || st == nil {
			return
		}
		userIDs, err := st.ListUserIDsWithAccounts(ctx)
		if err != nil {
			return
		}
		for _, userID := range userIDs {
			db, err := st.UserDB(ctx, userID)
			if err != nil {
				continue
			}
			_, _ = runScheduled(ctx, host, db, userID, time.Now().UTC())
		}
	}
	runAll()
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runAll()
		}
	}
}

func (p *mailFiltersBackend) ImportStoredMessage(ctx context.Context, host plugins.StoredMessageHost, msg plugins.StoredMessageContext) error {
	db, err := userDB(ctx, host, msg.UserID)
	if err != nil {
		return err
	}
	if err := purgeOldEvaluations(ctx, db); err != nil {
		return err
	}
	rules, err := listRules(ctx, db, msg.UserID, true)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		moved, err := evaluateRule(ctx, host, db, rule, msg, "inbound", 0)
		if err != nil {
			return err
		}
		if moved {
			break
		}
	}
	return nil
}

func (p *mailFiltersBackend) handleAPI(host plugins.APIHost, path string, w http.ResponseWriter, r *http.Request) {
	cu, ok := host.RequireAPIAuth(w, r)
	if !ok {
		return
	}
	db, err := userDB(r.Context(), host, cu.UserID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(path, apiPath), "/")
	switch {
	case rest == "rules" && r.Method == http.MethodGet:
		p.apiListRules(host, db, cu.UserID, w, r)
	case rest == "rules" && r.Method == http.MethodPost:
		p.apiSaveRule(host, db, cu.UserID, w, r)
	case strings.HasPrefix(rest, "rules/"):
		p.apiRuleAction(host, db, cu.UserID, rest, w, r)
	case strings.HasPrefix(rest, "messages/"):
		p.apiMessageAction(host, db, cu.UserID, rest, w, r)
	case rest == "scheduled/run" && r.Method == http.MethodPost:
		if !host.VerifyCSRF(w, r) {
			return
		}
		filterHost, ok := host.(plugins.StoredMessageHost)
		if !ok {
			host.WriteAPIError(w, http.StatusServiceUnavailable, "mail filter actions are not available")
			return
		}
		n, err := runScheduled(r.Context(), filterHost, db, cu.UserID, time.Now().UTC())
		if err != nil {
			host.ServerError(w, err)
			return
		}
		host.WriteJSON(w, map[string]any{"ok": true, "processed": n})
	default:
		host.WriteAPIError(w, http.StatusNotFound, "mail filter route not found")
	}
}

func (p *mailFiltersBackend) apiListRules(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	rules, err := listRules(r.Context(), db, userID, false)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	host.WriteJSON(w, map[string]any{"rules": rules})
}

func (p *mailFiltersBackend) apiSaveRule(host plugins.APIHost, db *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in Rule
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	rule, err := saveRule(r.Context(), db, userID, in)
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	host.WriteJSON(w, map[string]any{"ok": true, "rule": rule})
}

func (p *mailFiltersBackend) apiRuleAction(host plugins.APIHost, db *sql.DB, userID int64, rest string, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		host.WriteAPIError(w, http.StatusNotFound, "mail filter rule route not found")
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		host.WriteAPIError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	if len(parts) == 2 && r.Method == http.MethodDelete {
		if !host.VerifyCSRF(w, r) {
			return
		}
		if err := deleteRule(r.Context(), db, userID, id); err != nil {
			host.ServerError(w, err)
			return
		}
		host.WriteJSON(w, map[string]any{"ok": true})
		return
	}
	if len(parts) == 3 && parts[2] == "backfill" && r.Method == http.MethodPost {
		if !host.VerifyCSRF(w, r) {
			return
		}
		rule, err := getRule(r.Context(), db, userID, id)
		if err != nil {
			host.WriteAPIError(w, http.StatusNotFound, "filter rule not found")
			return
		}
		filterHost, ok := host.(plugins.StoredMessageHost)
		if !ok {
			host.WriteAPIError(w, http.StatusServiceUnavailable, "mail filter actions are not available")
			return
		}
		n, err := backfillRule(r.Context(), filterHost, db, rule)
		if err != nil {
			host.ServerError(w, err)
			return
		}
		host.WriteJSON(w, map[string]any{"ok": true, "processed": n})
		return
	}
	host.WriteAPIError(w, http.StatusNotFound, "mail filter rule route not found")
}

func (p *mailFiltersBackend) apiMessageAction(host plugins.APIHost, db *sql.DB, userID int64, rest string, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 3 || parts[0] != "messages" || parts[2] != "evaluations" || r.Method != http.MethodGet {
		host.WriteAPIError(w, http.StatusNotFound, "mail filter message route not found")
		return
	}
	messageID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || messageID <= 0 {
		host.WriteAPIError(w, http.StatusBadRequest, "invalid message id")
		return
	}
	evals, err := listMessageEvaluations(r.Context(), db, userID, messageID, 200)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	host.WriteJSON(w, map[string]any{"evaluations": evals})
}

type Actions struct {
	Star          bool   `json:"star"`
	MoveMailboxID int64  `json:"move_mailbox_id"`
	MoveRole      string `json:"move_role"`
	ForwardTo     string `json:"forward_to"`
}

type Rule struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Query      string  `json:"query"`
	Enabled    bool    `json:"enabled"`
	ScopeMode  string  `json:"scope_mode"`
	AccountIDs []int64 `json:"account_ids"`
	Actions    Actions `json:"actions"`
	Position   int64   `json:"position"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
}

type Evaluation struct {
	ID        int64    `json:"id"`
	UserID    int64    `json:"user_id"`
	RuleID    int64    `json:"rule_id"`
	MessageID int64    `json:"message_id"`
	AccountID int64    `json:"account_id"`
	MailboxID int64    `json:"mailbox_id"`
	Phase     string   `json:"phase"`
	Status    string   `json:"status"`
	Matched   bool     `json:"matched"`
	DueAt     int64    `json:"due_at"`
	Evaluated int64    `json:"evaluated_at"`
	Terms     []string `json:"terms"`
	Fields    []string `json:"fields"`
	Actions   string   `json:"actions_json"`
	Error     string   `json:"error"`
	CreatedAt int64    `json:"created_at"`
	RuleName  string   `json:"rule_name"`
	Subject   string   `json:"subject"`
	From      string   `json:"from_addr"`
}

func userDB(ctx context.Context, host plugins.BackendHost, userID int64) (*sql.DB, error) {
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		return nil, errors.New("plugin host store is unavailable")
	}
	return st.UserDB(ctx, userID)
}

func listRules(ctx context.Context, db *sql.DB, userID int64, enabledOnly bool) ([]Rule, error) {
	query := `SELECT id, user_id, name, query, enabled, scope_mode, actions_json, position, created_at, updated_at
		FROM plugin_mail_filter_rules WHERE user_id = ?`
	if enabledOnly {
		query += ` AND enabled = 1`
	}
	query += ` ORDER BY position, id`
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range rules {
		accounts, err := ruleAccounts(ctx, db, userID, rules[i].ID)
		if err != nil {
			return nil, err
		}
		rules[i].AccountIDs = accounts
	}
	return rules, nil
}

func getRule(ctx context.Context, db *sql.DB, userID, id int64) (Rule, error) {
	row := db.QueryRowContext(ctx, `SELECT id, user_id, name, query, enabled, scope_mode, actions_json, position, created_at, updated_at
		FROM plugin_mail_filter_rules WHERE user_id = ? AND id = ?`, userID, id)
	rule, err := scanRule(row)
	if err != nil {
		return Rule{}, err
	}
	rule.AccountIDs, err = ruleAccounts(ctx, db, userID, rule.ID)
	return rule, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRule(row rowScanner) (Rule, error) {
	var rule Rule
	var enabled int
	var actionsJSON string
	if err := row.Scan(&rule.ID, &rule.UserID, &rule.Name, &rule.Query, &enabled, &rule.ScopeMode, &actionsJSON, &rule.Position, &rule.CreatedAt, &rule.UpdatedAt); err != nil {
		return Rule{}, err
	}
	rule.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(actionsJSON), &rule.Actions)
	return rule, nil
}

func ruleAccounts(ctx context.Context, db *sql.DB, userID, ruleID int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT account_id FROM plugin_mail_filter_rule_accounts WHERE user_id = ? AND rule_id = ? ORDER BY account_id`, userID, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func saveRule(ctx context.Context, db *sql.DB, userID int64, in Rule) (Rule, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Query = strings.TrimSpace(in.Query)
	in.ScopeMode = strings.TrimSpace(in.ScopeMode)
	if in.ScopeMode == "" {
		in.ScopeMode = "all_accounts"
	}
	if in.ScopeMode != "all_accounts" && in.ScopeMode != "selected_accounts" {
		return Rule{}, errors.New("invalid account scope")
	}
	if in.Name == "" {
		in.Name = in.Query
	}
	if in.Query == "" {
		return Rule{}, errors.New("search query is required")
	}
	actionsJSON, err := json.Marshal(in.Actions)
	if err != nil {
		return Rule{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Unix()
	if in.ID > 0 {
		res, err := tx.ExecContext(ctx, `UPDATE plugin_mail_filter_rules SET name = ?, query = ?, enabled = ?, scope_mode = ?, actions_json = ?, position = ?, updated_at = ? WHERE user_id = ? AND id = ?`,
			in.Name, in.Query, boolInt(in.Enabled), in.ScopeMode, string(actionsJSON), in.Position, now, userID, in.ID)
		if err != nil {
			return Rule{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return Rule{}, err
		}
		if n == 0 {
			return Rule{}, store.ErrNotFound
		}
	} else {
		res, err := tx.ExecContext(ctx, `INSERT INTO plugin_mail_filter_rules (user_id, name, query, enabled, scope_mode, actions_json, position, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, userID, in.Name, in.Query, boolInt(in.Enabled), in.ScopeMode, string(actionsJSON), in.Position, now, now)
		if err != nil {
			return Rule{}, err
		}
		in.ID, err = res.LastInsertId()
		if err != nil {
			return Rule{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_mail_filter_rule_accounts WHERE user_id = ? AND rule_id = ?`, userID, in.ID); err != nil {
		return Rule{}, err
	}
	if in.ScopeMode == "selected_accounts" {
		for _, accountID := range uniqueIDs(in.AccountIDs) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_mail_filter_rule_accounts (rule_id, user_id, account_id) VALUES (?, ?, ?)`, in.ID, userID, accountID); err != nil {
				return Rule{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, err
	}
	return getRule(ctx, db, userID, in.ID)
}

func deleteRule(ctx context.Context, db *sql.DB, userID, id int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM plugin_mail_filter_rules WHERE user_id = ? AND id = ?`, userID, id)
	return err
}

func evaluateRule(ctx context.Context, host plugins.StoredMessageHost, db *sql.DB, rule Rule, msg plugins.StoredMessageContext, phase string, evalID int64) (bool, error) {
	if rule.ScopeMode == "selected_accounts" && !containsID(rule.AccountIDs, msg.AccountID) {
		return false, recordEvaluation(ctx, db, evalID, rule, msg, phase, statusSkipped, false, time.Time{}, nil, nil, "{}", "")
	}
	query := rule.Query
	if age, ok := olderThanClause(query); ok && phase == "inbound" && !msg.Date.IsZero() {
		dueAt := msg.Date.Add(age.Duration)
		if time.Now().UTC().Before(dueAt) {
			matchQuery := strings.TrimSpace(age.QueryWithoutClause)
			if matchQuery == "" {
				matchQuery = ""
			}
			result, err := host.MatchMessageSearch(ctx, msg.UserID, msg.MessageID, matchQuery)
			if err != nil {
				return false, recordEvaluation(ctx, db, evalID, rule, msg, phase, statusFailed, false, time.Time{}, nil, nil, "{}", err.Error())
			}
			if result.Matched {
				return false, recordEvaluation(ctx, db, evalID, rule, msg, phase, statusScheduled, false, dueAt, result.Terms, result.Fields, "{}", "")
			}
			return false, recordEvaluation(ctx, db, evalID, rule, msg, phase, statusNotMatched, false, time.Time{}, result.Terms, result.Fields, "{}", "")
		}
	}
	result, err := host.MatchMessageSearch(ctx, msg.UserID, msg.MessageID, query)
	if err != nil {
		return false, recordEvaluation(ctx, db, evalID, rule, msg, phase, statusFailed, false, time.Time{}, nil, nil, "{}", err.Error())
	}
	if !result.Matched {
		return false, recordEvaluation(ctx, db, evalID, rule, msg, phase, statusNotMatched, false, time.Time{}, result.Terms, result.Fields, "{}", "")
	}
	actionJSON, moved, status, errText := applyActions(ctx, host, db, rule, msg)
	if errText != "" {
		status = statusFailed
	}
	if status == "" {
		status = statusMatched
	}
	if err := recordEvaluation(ctx, db, evalID, rule, msg, phase, status, true, time.Time{}, result.Terms, result.Fields, actionJSON, errText); err != nil {
		return moved, err
	}
	return moved, nil
}

func applyActions(ctx context.Context, host plugins.StoredMessageHost, db *sql.DB, rule Rule, msg plugins.StoredMessageContext) (string, bool, string, string) {
	results := map[string]string{}
	if rule.Actions.Star {
		if err := host.StarMessage(ctx, msg.UserID, msg.MessageID, true); err != nil {
			results["star"] = "failed"
			return mustJSON(results), false, statusFailed, err.Error()
		}
		results["star"] = "ok"
	}
	if strings.TrimSpace(rule.Actions.ForwardTo) != "" {
		forwarderID, err := ensureForwarderID(ctx, db, msg.UserID, msg.AccountID)
		if err != nil {
			results["forward"] = "failed"
			return mustJSON(results), false, statusFailed, err.Error()
		}
		err = host.ForwardMessage(ctx, msg.UserID, msg.MessageID, rule.Actions.ForwardTo, []plugins.MailHeader{{Name: forwarderHeader, Value: forwarderID}})
		if err != nil {
			results["forward"] = "failed"
			if strings.Contains(strings.ToLower(err.Error()), "already forwarded") {
				return mustJSON(results), false, statusLoop, err.Error()
			}
			return mustJSON(results), false, statusFailed, err.Error()
		}
		results["forward"] = "ok"
	}
	destID := rule.Actions.MoveMailboxID
	if destID == 0 && strings.TrimSpace(rule.Actions.MoveRole) != "" {
		destID = mailboxIDByRole(ctx, db, msg.UserID, msg.AccountID, rule.Actions.MoveRole)
	}
	if destID > 0 {
		if err := host.MoveMessage(ctx, msg.UserID, msg.MessageID, destID); err != nil {
			results["move"] = "failed"
			return mustJSON(results), false, statusFailed, err.Error()
		}
		results["move"] = "ok"
		return mustJSON(results), true, statusMatched, ""
	}
	return mustJSON(results), false, statusMatched, ""
}

func recordEvaluation(ctx context.Context, db *sql.DB, evalID int64, rule Rule, msg plugins.StoredMessageContext, phase, status string, matched bool, dueAt time.Time, terms, fields []string, actionJSON, errText string) error {
	termsJSON := mustJSON(terms)
	fieldsJSON := mustJSON(fields)
	now := time.Now().UTC().Unix()
	if evalID > 0 {
		_, err := db.ExecContext(ctx, `UPDATE plugin_mail_filter_evaluations SET phase = ?, status = ?, matched = ?, due_at = ?, evaluated_at = ?, terms_json = ?, fields_json = ?, actions_json = ?, error = ? WHERE user_id = ? AND id = ?`,
			phase, status, boolInt(matched), unixOrZero(dueAt), now, termsJSON, fieldsJSON, actionJSON, errText, msg.UserID, evalID)
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_mail_filter_evaluations
		(user_id, rule_id, message_id, account_id, mailbox_id, phase, status, matched, due_at, evaluated_at, terms_json, fields_json, actions_json, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.UserID, rule.ID, msg.MessageID, msg.AccountID, msg.MailboxID, phase, status, boolInt(matched), unixOrZero(dueAt), now, termsJSON, fieldsJSON, actionJSON, errText, now)
	return err
}

func listRecentEvaluations(ctx context.Context, db *sql.DB, userID int64, limit int) ([]Evaluation, error) {
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.user_id, e.rule_id, e.message_id, e.account_id, e.mailbox_id, e.phase, e.status, e.matched, e.due_at, e.evaluated_at, e.terms_json, e.fields_json, e.actions_json, e.error, e.created_at, r.name, COALESCE(m.subject, ''), COALESCE(m.from_addr, '')
		FROM plugin_mail_filter_evaluations e
		JOIN plugin_mail_filter_rules r ON r.id = e.rule_id AND r.user_id = e.user_id
		LEFT JOIN messages m ON m.id = e.message_id AND m.user_id = e.user_id
		WHERE e.user_id = ? AND e.matched = 1
		ORDER BY e.id DESC LIMIT ?`, userID, limit)
	return scanEvaluations(rows, err)
}

func listMessageEvaluations(ctx context.Context, db *sql.DB, userID, messageID int64, limit int) ([]Evaluation, error) {
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.user_id, e.rule_id, e.message_id, e.account_id, e.mailbox_id, e.phase, e.status, e.matched, e.due_at, e.evaluated_at, e.terms_json, e.fields_json, e.actions_json, e.error, e.created_at, r.name, COALESCE(m.subject, ''), COALESCE(m.from_addr, '')
		FROM plugin_mail_filter_evaluations e
		JOIN plugin_mail_filter_rules r ON r.id = e.rule_id AND r.user_id = e.user_id
		LEFT JOIN messages m ON m.id = e.message_id AND m.user_id = e.user_id
		WHERE e.user_id = ? AND e.message_id = ?
		ORDER BY e.evaluated_at DESC, e.id DESC LIMIT ?`, userID, messageID, limit)
	return scanEvaluations(rows, err)
}

func scanEvaluations(rows *sql.Rows, err error) ([]Evaluation, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evaluation
	for rows.Next() {
		var ev Evaluation
		var matched int
		var termsJSON, fieldsJSON string
		if err := rows.Scan(&ev.ID, &ev.UserID, &ev.RuleID, &ev.MessageID, &ev.AccountID, &ev.MailboxID, &ev.Phase, &ev.Status, &matched, &ev.DueAt, &ev.Evaluated, &termsJSON, &fieldsJSON, &ev.Actions, &ev.Error, &ev.CreatedAt, &ev.RuleName, &ev.Subject, &ev.From); err != nil {
			return nil, err
		}
		ev.Matched = matched != 0
		_ = json.Unmarshal([]byte(termsJSON), &ev.Terms)
		_ = json.Unmarshal([]byte(fieldsJSON), &ev.Fields)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func backfillRule(ctx context.Context, host plugins.StoredMessageHost, db *sql.DB, rule Rule) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, subject, from_addr, to_addr, cc_addr, date_unix, uid, is_read, is_starred FROM messages WHERE user_id = ? ORDER BY date_unix DESC, id DESC LIMIT ?`, rule.UserID, backfillBatch)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var messages []plugins.StoredMessageContext
	for rows.Next() {
		var msg plugins.StoredMessageContext
		var dateUnix int64
		var read, starred int
		if err := rows.Scan(&msg.MessageID, &msg.UserID, &msg.AccountID, &msg.MailboxID, &msg.Subject, &msg.From, &msg.To, &msg.CC, &dateUnix, &msg.UID, &read, &starred); err != nil {
			return 0, err
		}
		msg.Date = time.Unix(dateUnix, 0).UTC()
		msg.IsRead = read != 0
		msg.IsStarred = starred != 0
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	processed := 0
	for _, msg := range messages {
		moved, err := evaluateRule(ctx, host, db, rule, msg, "backfill", 0)
		if err != nil {
			return processed, err
		}
		processed++
		if moved {
			continue
		}
	}
	return processed, nil
}

func runScheduled(ctx context.Context, host plugins.StoredMessageHost, db *sql.DB, userID int64, now time.Time) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.rule_id, m.id, m.user_id, m.account_id, m.mailbox_id, m.subject, m.from_addr, m.to_addr, m.cc_addr, m.date_unix, m.uid, m.is_read, m.is_starred
		FROM plugin_mail_filter_evaluations e
		JOIN plugin_mail_filter_rules r ON r.id = e.rule_id AND r.user_id = e.user_id
		JOIN messages m ON m.id = e.message_id AND m.user_id = e.user_id
		WHERE e.user_id = ? AND e.status = ? AND e.due_at > 0 AND e.due_at <= ? AND r.enabled = 1
		ORDER BY e.due_at, e.id LIMIT ?`, userID, statusScheduled, now.Unix(), scheduledBatch)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type due struct {
		evalID int64
		ruleID int64
		msg    plugins.StoredMessageContext
	}
	var dueRows []due
	for rows.Next() {
		var item due
		var dateUnix int64
		var read, starred int
		if err := rows.Scan(&item.evalID, &item.ruleID, &item.msg.MessageID, &item.msg.UserID, &item.msg.AccountID, &item.msg.MailboxID, &item.msg.Subject, &item.msg.From, &item.msg.To, &item.msg.CC, &dateUnix, &item.msg.UID, &read, &starred); err != nil {
			return 0, err
		}
		item.msg.Date = time.Unix(dateUnix, 0).UTC()
		item.msg.IsRead = read != 0
		item.msg.IsStarred = starred != 0
		dueRows = append(dueRows, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	processed := 0
	for _, item := range dueRows {
		rule, err := getRule(ctx, db, userID, item.ruleID)
		if err != nil {
			return processed, err
		}
		if _, err := evaluateRule(ctx, host, db, rule, item.msg, "scheduled", item.evalID); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, purgeOldEvaluations(ctx, db)
}

func purgeOldEvaluations(ctx context.Context, db *sql.DB) error {
	cutoff := time.Now().UTC().Add(-retentionWindow).Unix()
	_, err := db.ExecContext(ctx, `DELETE FROM plugin_mail_filter_evaluations WHERE status <> ? AND evaluated_at > 0 AND evaluated_at < ?`, statusScheduled, cutoff)
	return err
}

type ageClause struct {
	Duration           time.Duration
	QueryWithoutClause string
}

func olderThanClause(query string) (ageClause, bool) {
	fields := strings.Fields(query)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		lower := strings.ToLower(strings.Trim(field, `"`))
		if strings.HasPrefix(lower, "older_than:") {
			if d, ok := parseAgeDuration(strings.TrimPrefix(lower, "older_than:")); ok {
				return ageClause{Duration: d, QueryWithoutClause: strings.Join(append(out, fields[len(out)+1:]...), " ")}, true
			}
		}
		out = append(out, field)
	}
	return ageClause{}, false
}

func parseAgeDuration(value string) (time.Duration, bool) {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if len(value) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch value[len(value)-1] {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, true
	case 'm':
		return time.Duration(n) * 30 * 24 * time.Hour, true
	case 'y':
		return time.Duration(n) * 365 * 24 * time.Hour, true
	}
	return 0, false
}

func ensureForwarderID(ctx context.Context, db *sql.DB, userID, accountID int64) (string, error) {
	var existing string
	err := db.QueryRowContext(ctx, `SELECT forwarder_id FROM plugin_mail_filter_forwarders WHERE user_id = ? AND account_id = ?`, userID, accountID).Scan(&existing)
	if err == nil && strings.TrimSpace(existing) != "" {
		return existing, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	id := "rtf-" + hex.EncodeToString(random)
	_, err = db.ExecContext(ctx, `INSERT INTO plugin_mail_filter_forwarders (user_id, account_id, forwarder_id, created_at) VALUES (?, ?, ?, ?)`, userID, accountID, id, time.Now().UTC().Unix())
	return id, err
}

func mailboxIDByRole(ctx context.Context, db *sql.DB, userID, accountID int64, role string) int64 {
	var id int64
	_ = db.QueryRowContext(ctx, `SELECT id FROM mailboxes WHERE user_id = ? AND account_id = ? AND role = ? ORDER BY id LIMIT 1`, userID, accountID, strings.TrimSpace(role)).Scan(&id)
	return id
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func containsID(ids []int64, needle int64) bool {
	for _, id := range ids {
		if id == needle {
			return true
		}
	}
	return false
}

func uniqueIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}
