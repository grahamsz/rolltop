// File overview: Runtime backend plugin that exposes an OAuth-protected MCP
// server with Gmail-like read-only tools over local Rolltop mail.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"rolltop/backend/auth"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const (
	pluginID = "mail_mcp"
	apiPath  = "plugins/mail_mcp"
)

type mailMCPPlugin struct {
	mu       sync.Mutex
	routes   []plugins.ProtectedAPIRouteHandle
	codes    map[string]oauthCode
	access   map[string]oauthToken
	refresh  map[string]oauthToken
	issuedAt time.Time
}

type oauthCode struct {
	UserID      int64
	ClientID    string
	RedirectURI string
	Scope       string
	ExpiresAt   time.Time
}

type oauthToken struct {
	UserID    int64
	ClientID  string
	Scope     string
	ExpiresAt time.Time
}

func RolltopPlugin() plugins.BackendPlugin {
	return &mailMCPPlugin{}
}

func (p *mailMCPPlugin) ID() string { return pluginID }

func (p *mailMCPPlugin) Start(host plugins.BackendStartHost) error {
	p.unregister()
	p.mu.Lock()
	p.codes = map[string]oauthCode{}
	p.access = map[string]oauthToken{}
	p.refresh = map[string]oauthToken{}
	p.issuedAt = time.Now()
	p.mu.Unlock()

	authorize, err := host.RegisterProtectedAPI(pluginID, plugins.ProtectedAPIRoute{Path: apiPath + "/oauth/authorize", Handle: p.authorize})
	if err != nil {
		return err
	}
	token, err := host.RegisterPublicAPI(pluginID, plugins.PublicAPIRoute{Path: apiPath + "/oauth/token", Handle: p.token})
	if err != nil {
		authorize.Unregister()
		return err
	}
	mcp, err := host.RegisterPublicAPI(pluginID, plugins.PublicAPIRoute{Path: apiPath + "/mcp", Handle: p.mcp})
	if err != nil {
		authorize.Unregister()
		token.Unregister()
		return err
	}
	p.routes = []plugins.ProtectedAPIRouteHandle{authorize, token, mcp}
	return nil
}

func (p *mailMCPPlugin) Stop(plugins.BackendStartHost) error {
	p.unregister()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes = nil
	p.access = nil
	p.refresh = nil
	return nil
}

func (p *mailMCPPlugin) unregister() {
	for _, route := range p.routes {
		route.Unregister()
	}
	p.routes = nil
}

func (p *mailMCPPlugin) authorize(host plugins.APIHost, _ string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cu, ok := host.RequireAPIAuth(w, r)
	if !ok {
		return
	}
	responseType := strings.TrimSpace(r.URL.Query().Get("response_type"))
	clientID := strings.TrimSpace(r.URL.Query().Get("client_id"))
	redirectURI := strings.TrimSpace(r.URL.Query().Get("redirect_uri"))
	if responseType != "code" || clientID == "" || redirectURI == "" {
		host.WriteAPIError(w, http.StatusBadRequest, "OAuth request must include response_type=code, client_id, and redirect_uri.")
		return
	}
	parsed, err := url.Parse(redirectURI)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		host.WriteAPIError(w, http.StatusBadRequest, "OAuth redirect_uri is invalid.")
		return
	}
	code, err := auth.NewOpaqueToken()
	if err != nil {
		host.ServerError(w, err)
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = "mail.readonly"
	}
	p.mu.Lock()
	p.pruneLocked(time.Now())
	p.codes[codeHash(code)] = oauthCode{
		UserID:      cu.UserID,
		ClientID:    clientID,
		RedirectURI: redirectURI,
		Scope:       scope,
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	}
	p.mu.Unlock()

	query := parsed.Query()
	query.Set("code", code)
	if state := strings.TrimSpace(r.URL.Query().Get("state")); state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	http.Redirect(w, r, parsed.String(), http.StatusFound)
}

func (p *mailMCPPlugin) token(host plugins.APIHost, _ string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, "OAuth token request is invalid.")
		return
	}
	switch strings.TrimSpace(r.Form.Get("grant_type")) {
	case "authorization_code":
		p.exchangeCode(host, w, r)
	case "refresh_token":
		p.exchangeRefresh(host, w, r)
	default:
		host.WriteAPIError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (p *mailMCPPlugin) exchangeCode(host plugins.APIHost, w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.Form.Get("code"))
	clientID := strings.TrimSpace(r.Form.Get("client_id"))
	redirectURI := strings.TrimSpace(r.Form.Get("redirect_uri"))
	if code == "" || clientID == "" || redirectURI == "" {
		host.WriteAPIError(w, http.StatusBadRequest, "authorization_code grant requires code, client_id, and redirect_uri.")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(time.Now())
	key := codeHash(code)
	stored, ok := p.codes[key]
	delete(p.codes, key)
	if !ok || time.Now().After(stored.ExpiresAt) || stored.ClientID != clientID || stored.RedirectURI != redirectURI {
		host.WriteAPIError(w, http.StatusUnauthorized, "invalid_grant")
		return
	}
	p.writeTokenResponseLocked(host, w, stored.UserID, stored.ClientID, stored.Scope)
}

func (p *mailMCPPlugin) exchangeRefresh(host plugins.APIHost, w http.ResponseWriter, r *http.Request) {
	refresh := strings.TrimSpace(r.Form.Get("refresh_token"))
	clientID := strings.TrimSpace(r.Form.Get("client_id"))
	if refresh == "" || clientID == "" {
		host.WriteAPIError(w, http.StatusBadRequest, "refresh_token grant requires refresh_token and client_id.")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(time.Now())
	stored, ok := p.refresh[codeHash(refresh)]
	if !ok || stored.ClientID != clientID {
		host.WriteAPIError(w, http.StatusUnauthorized, "invalid_grant")
		return
	}
	p.writeTokenResponseLocked(host, w, stored.UserID, stored.ClientID, stored.Scope)
}

func (p *mailMCPPlugin) writeTokenResponseLocked(host plugins.APIHost, w http.ResponseWriter, userID int64, clientID, scope string) {
	access, accessErr := auth.NewOpaqueToken()
	refresh, refreshErr := auth.NewOpaqueToken()
	if accessErr != nil || refreshErr != nil {
		host.ServerError(w, firstErr(accessErr, refreshErr))
		return
	}
	p.access[codeHash(access)] = oauthToken{UserID: userID, ClientID: clientID, Scope: scope, ExpiresAt: time.Now().Add(time.Hour)}
	p.refresh[codeHash(refresh)] = oauthToken{UserID: userID, ClientID: clientID, Scope: scope, ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	host.WriteJSON(w, map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         scope,
	})
}

func (p *mailMCPPlugin) mcp(host plugins.APIHost, _ string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	userID, ok := p.bearerUserID(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Rolltop Mail MCP"`)
		host.WriteAPIError(w, http.StatusUnauthorized, "MCP requests require a Mail MCP bearer token.")
		return
	}
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		host.ServerError(w, errors.New("store is not available"))
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: nil, Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if req.JSONRPC == "" {
		req.JSONRPC = "2.0"
	}
	result, rpcErr := p.handleMCP(r.Context(), st, userID, req)
	if req.ID == nil && rpcErr == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	writeRPC(w, resp)
}

func (p *mailMCPPlugin) bearerUserID(r *http.Request) (int64, bool) {
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		return 0, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	if token == "" {
		return 0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(time.Now())
	stored, ok := p.access[codeHash(token)]
	if !ok || time.Now().After(stored.ExpiresAt) {
		return 0, false
	}
	return stored.UserID, true
}

func (p *mailMCPPlugin) pruneLocked(now time.Time) {
	for key, code := range p.codes {
		if now.After(code.ExpiresAt) {
			delete(p.codes, key)
		}
	}
	for key, token := range p.access {
		if now.After(token.ExpiresAt) {
			delete(p.access, key)
		}
	}
	for key, token := range p.refresh {
		if now.After(token.ExpiresAt) {
			delete(p.refresh, key)
		}
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (p *mailMCPPlugin) handleMCP(ctx context.Context, st *store.Store, userID int64, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
			},
			"serverInfo": map[string]any{"name": "rolltop-mail-mcp", "version": "1.0.0"},
		}, nil
	case "notifications/initialized":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mailTools()}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, invalidParams("tools/call params are invalid")
		}
		value, err := callMailTool(ctx, st, userID, params.Name, params.Arguments)
		if err != nil {
			return nil, toolError(err)
		}
		raw, _ := json.MarshalIndent(value, "", "  ")
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(raw)}}}, nil
	case "resources/list":
		return listResources(ctx, st, userID)
	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, invalidParams("resources/read params are invalid")
		}
		return readResource(ctx, st, userID, params.URI)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func mailTools() []map[string]any {
	return []map[string]any{
		toolSchema("mail.users.getProfile", "Get the current Rolltop user's Gmail-like profile.", map[string]any{}),
		toolSchema("mail.users.labels.list", "List Gmail-like labels mapped from Rolltop mailboxes.", map[string]any{}),
		toolSchema("mail.users.messages.list", "List Gmail-like messages. Supports maxResults, pageToken, labelIds, and q.", map[string]any{
			"maxResults": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"pageToken":  map[string]any{"type": "string"},
			"labelIds":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"q":          map[string]any{"type": "string"},
		}),
		toolSchema("mail.users.messages.get", "Get a Gmail-like message by local Rolltop message id.", map[string]any{
			"id":     map[string]any{"type": "string"},
			"format": map[string]any{"type": "string", "enum": []string{"minimal", "metadata", "full"}},
		}),
		toolSchema("mail.users.threads.list", "List Gmail-like threads.", map[string]any{
			"maxResults": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"pageToken":  map[string]any{"type": "string"},
			"labelIds":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"q":          map[string]any{"type": "string"},
		}),
		toolSchema("mail.users.threads.get", "Get a Gmail-like thread by thread id or message id.", map[string]any{
			"id": map[string]any{"type": "string"},
		}),
	}
}

func toolSchema(name, description string, properties map[string]any) map[string]any {
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           properties,
			"additionalProperties": false,
		},
	}
}

func callMailTool(ctx context.Context, st *store.Store, userID int64, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "mail.users.getProfile":
		return mailProfile(ctx, st, userID)
	case "mail.users.labels.list":
		return mailLabels(ctx, st, userID)
	case "mail.users.messages.list":
		args := listArgs{}
		_ = json.Unmarshal(raw, &args)
		return mailMessagesList(ctx, st, userID, args)
	case "mail.users.messages.get":
		var args struct {
			ID     string `json:"id"`
			Format string `json:"format"`
		}
		if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.ID) == "" {
			return nil, errors.New("message id is required")
		}
		return mailMessageGet(ctx, st, userID, args.ID, args.Format)
	case "mail.users.threads.list":
		args := listArgs{}
		_ = json.Unmarshal(raw, &args)
		return mailThreadsList(ctx, st, userID, args)
	case "mail.users.threads.get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.ID) == "" {
			return nil, errors.New("thread id is required")
		}
		return mailThreadGet(ctx, st, userID, args.ID)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

type listArgs struct {
	MaxResults int      `json:"maxResults"`
	PageToken  string   `json:"pageToken"`
	LabelIDs   []string `json:"labelIds"`
	Q          string   `json:"q"`
}

func mailProfile(ctx context.Context, st *store.Store, userID int64) (map[string]any, error) {
	user, err := st.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	total, err := st.CountMessagesForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"emailAddress":    user.Email,
		"messagesTotal":   total,
		"threadsTotal":    total,
		"historyId":       strconv.FormatInt(time.Now().Unix(), 10),
		"rolltopUserId":   user.ID,
		"rolltopReadOnly": true,
	}, nil
}

func mailLabels(ctx context.Context, st *store.Store, userID int64) (map[string]any, error) {
	mailboxes, err := st.ListMailboxesForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	labels := make([]map[string]any, 0, len(mailboxes)+2)
	labels = append(labels,
		map[string]any{"id": "INBOX", "name": "INBOX", "type": "system"},
		map[string]any{"id": "STARRED", "name": "STARRED", "type": "system"},
	)
	for _, mailbox := range mailboxes {
		labels = append(labels, map[string]any{
			"id":                  mailLabelID(mailbox.ID),
			"name":                mailbox.Name,
			"type":                labelType(mailbox.Role),
			"messagesTotal":       mailbox.LocalMessageCount,
			"messagesUnread":      mailbox.UnreadCount,
			"rolltopMailboxId":    mailbox.ID,
			"rolltopAccountEmail": mailbox.AccountEmail,
		})
	}
	return map[string]any{"labels": labels}, nil
}

func mailMessagesList(ctx context.Context, st *store.Store, userID int64, args listArgs) (map[string]any, error) {
	messages, nextToken, err := listMessages(ctx, st, userID, args, false)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		out = append(out, map[string]any{"id": messageID(msg.ID), "threadId": threadID(msg)})
	}
	resp := map[string]any{"messages": out, "resultSizeEstimate": len(out)}
	if nextToken != "" {
		resp["nextPageToken"] = nextToken
	}
	return resp, nil
}

func mailMessageGet(ctx context.Context, st *store.Store, userID int64, id, format string) (map[string]any, error) {
	messageID, err := parseNumericID(id)
	if err != nil {
		return nil, err
	}
	msg, err := st.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return nil, err
	}
	return mailMessage(msg, format), nil
}

func mailThreadsList(ctx context.Context, st *store.Store, userID int64, args listArgs) (map[string]any, error) {
	messages, nextToken, err := listMessages(ctx, st, userID, args, true)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		out = append(out, map[string]any{"id": threadID(msg), "snippet": snippet(msg.BodyText), "historyId": strconv.FormatInt(msg.UpdatedAt.Unix(), 10)})
	}
	resp := map[string]any{"threads": out, "resultSizeEstimate": len(out)}
	if nextToken != "" {
		resp["nextPageToken"] = nextToken
	}
	return resp, nil
}

func mailThreadGet(ctx context.Context, st *store.Store, userID int64, id string) (map[string]any, error) {
	msgID, err := parseNumericID(id)
	if err != nil {
		return nil, err
	}
	msg, err := st.GetMessageForUser(ctx, userID, msgID)
	if err != nil {
		return nil, err
	}
	thread, err := st.ListThreadMessagesForUser(ctx, userID, msg)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(thread))
	for _, item := range thread {
		out = append(out, mailMessage(item, "metadata"))
	}
	return map[string]any{"id": threadID(msg), "messages": out, "historyId": strconv.FormatInt(msg.UpdatedAt.Unix(), 10)}, nil
}

func listMessages(ctx context.Context, st *store.Store, userID int64, args listArgs, threads bool) ([]store.MessageRecord, string, error) {
	limit := args.MaxResults
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset := pageOffset(args.PageToken)
	fetchLimit := limit + 1
	if strings.TrimSpace(args.Q) != "" {
		fetchLimit = 200
		offset = 0
	}
	var messages []store.MessageRecord
	var err error
	if mailboxID := firstMailboxLabel(args.LabelIDs); mailboxID > 0 {
		if _, err := st.GetMailboxForUser(ctx, userID, mailboxID); err != nil {
			return nil, "", err
		}
		if threads {
			messages, err = st.ListLatestThreadMessagesForMailbox(ctx, userID, mailboxID, fetchLimit, offset)
		} else {
			messages, err = st.ListMessagesForMailbox(ctx, userID, mailboxID, fetchLimit, offset)
		}
	} else {
		if threads {
			messages, err = st.ListLatestThreadMessagesForUser(ctx, userID, fetchLimit, offset)
		} else {
			messages, err = st.ListMessagesForUser(ctx, userID, fetchLimit, offset)
		}
	}
	if err != nil {
		return nil, "", err
	}
	if query := strings.ToLower(strings.TrimSpace(args.Q)); query != "" {
		filtered := messages[:0]
		for _, msg := range messages {
			haystack := strings.ToLower(strings.Join([]string{msg.Subject, msg.FromAddr, msg.ToAddr, msg.CCAddr, msg.BodyText}, " "))
			if strings.Contains(haystack, query) {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}
	next := ""
	if len(messages) > limit {
		messages = messages[:limit]
		next = strconv.Itoa(offset + limit)
	}
	return messages, next, nil
}

func mailMessage(msg store.MessageRecord, format string) map[string]any {
	labels := []string{mailLabelID(msg.MailboxID)}
	if !msg.IsRead {
		labels = append(labels, "UNREAD")
	}
	if msg.IsStarred {
		labels = append(labels, "STARRED")
	}
	out := map[string]any{
		"id":           messageID(msg.ID),
		"threadId":     threadID(msg),
		"labelIds":     labels,
		"snippet":      snippet(msg.BodyText),
		"historyId":    strconv.FormatInt(msg.UpdatedAt.Unix(), 10),
		"internalDate": strconv.FormatInt(msg.InternalDate.UnixMilli(), 10),
		"sizeEstimate": msg.Size,
	}
	if format == "minimal" {
		return out
	}
	headers := []map[string]string{
		{"name": "Message-ID", "value": msg.MessageIDHeader},
		{"name": "Subject", "value": msg.Subject},
		{"name": "From", "value": msg.FromAddr},
		{"name": "To", "value": msg.ToAddr},
		{"name": "Cc", "value": msg.CCAddr},
		{"name": "Date", "value": msg.Date.Format(time.RFC1123Z)},
	}
	payload := map[string]any{
		"mimeType": "text/plain",
		"headers":  headers,
	}
	if format == "" || format == "full" {
		payload["body"] = map[string]any{
			"size": len(msg.BodyText),
			"data": base64.RawURLEncoding.EncodeToString([]byte(msg.BodyText)),
		}
	}
	out["payload"] = payload
	return out
}

func listResources(ctx context.Context, st *store.Store, userID int64) (any, *rpcError) {
	messages, _, err := listMessages(ctx, st, userID, listArgs{MaxResults: 20}, false)
	if err != nil {
		return nil, toolError(err)
	}
	resources := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		resources = append(resources, map[string]any{
			"uri":         "mail://messages/" + messageID(msg.ID),
			"name":        firstNonEmpty(msg.Subject, "(no subject)"),
			"description": fmt.Sprintf("%s from %s", msg.Date.Format("2006-01-02"), msg.FromAddr),
			"mimeType":    "application/json",
		})
	}
	return map[string]any{"resources": resources}, nil
}

func readResource(ctx context.Context, st *store.Store, userID int64, uri string) (any, *rpcError) {
	const prefix = "mail://messages/"
	if !strings.HasPrefix(uri, prefix) {
		return nil, invalidParams("unsupported resource uri")
	}
	msg, err := mailMessageGet(ctx, st, userID, strings.TrimPrefix(uri, prefix), "full")
	if err != nil {
		return nil, toolError(err)
	}
	raw, _ := json.MarshalIndent(msg, "", "  ")
	return map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(raw)}}}, nil
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

func codeHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func toolError(err error) *rpcError {
	if store.IsNotFound(err) {
		return &rpcError{Code: -32004, Message: "not found"}
	}
	return &rpcError{Code: -32000, Message: err.Error()}
}

func invalidParams(message string) *rpcError {
	return &rpcError{Code: -32602, Message: message}
}

func messageID(id int64) string { return strconv.FormatInt(id, 10) }

func threadID(msg store.MessageRecord) string {
	if strings.TrimSpace(msg.ThreadKey) == "" {
		return messageID(msg.ID)
	}
	return messageID(msg.ID)
}

func mailLabelID(mailboxID int64) string { return "Label_" + strconv.FormatInt(mailboxID, 10) }

func firstMailboxLabel(labels []string) int64 {
	for _, label := range labels {
		id, err := parseMailboxLabel(label)
		if err == nil && id > 0 {
			return id
		}
	}
	return 0
}

func parseMailboxLabel(label string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(label), "Label_"), 10, 64)
}

func parseNumericID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(value, "msg_")), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func pageOffset(pageToken string) int {
	offset, err := strconv.Atoi(strings.TrimSpace(pageToken))
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

func snippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 180 {
		return text[:180]
	}
	return text
}

func labelType(role string) string {
	switch strings.TrimSpace(role) {
	case "inbox", "sent", "drafts", "trash", "spam", "archive":
		return "system"
	default:
		return "user"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
