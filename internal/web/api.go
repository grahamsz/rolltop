package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mailmirror/internal/auth"
	mmcrypto "mailmirror/internal/crypto"
	"mailmirror/internal/search"
	"mailmirror/internal/smtpclient"
	"mailmirror/internal/store"
)

type apiUser struct {
	ID         int64  `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	IsAdmin    bool   `json:"is_admin"`
	DateLocale string `json:"date_locale"`
	DateFormat string `json:"date_format"`
}

type apiMailbox struct {
	ID                 int64  `json:"id"`
	AccountID          int64  `json:"account_id"`
	AccountEmail       string `json:"account_email"`
	Name               string `json:"name"`
	MessageCount       int    `json:"message_count"`
	UnreadCount        int    `json:"unread_count"`
	SyncMode           string `json:"sync_mode"`
	Role               string `json:"role"`
	Icon               string `json:"icon"`
	ShowInSidebar      bool   `json:"show_in_sidebar"`
	ShowInAllMail      bool   `json:"show_in_all_mail"`
	IncludeInSearch    bool   `json:"include_in_search"`
	LastUID            uint32 `json:"last_uid"`
	RemoteMessageCount int    `json:"remote_message_count"`
	RemoteUnreadCount  int    `json:"remote_unread_count"`
	RemoteUIDNext      uint32 `json:"remote_uid_next"`
	SyncPercent        int    `json:"sync_percent"`
}

type apiAccount struct {
	ID                  int64  `json:"id"`
	Email               string `json:"email"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	UseTLS              bool   `json:"use_tls"`
	SMTPHost            string `json:"smtp_host"`
	SMTPPort            int    `json:"smtp_port"`
	SMTPUsername        string `json:"smtp_username"`
	SMTPUseTLS          bool   `json:"smtp_use_tls"`
	SMTPSameAsIMAP      bool   `json:"smtp_same_as_imap"`
	Mailbox             string `json:"mailbox"`
	SyncIntervalMinutes int    `json:"sync_interval_minutes"`
}

type apiMessage struct {
	ID             int64  `json:"id"`
	MailboxID      int64  `json:"mailbox_id"`
	Subject        string `json:"subject"`
	FromAddr       string `json:"from_addr"`
	ToAddr         string `json:"to_addr"`
	CCAddr         string `json:"cc_addr"`
	Date           string `json:"date"`
	DateShort      string `json:"date_short"`
	IsRead         bool   `json:"is_read"`
	IsStarred      bool   `json:"is_starred"`
	HasAttachments bool   `json:"has_attachments"`
	Snippet        string `json:"snippet"`
}

type apiConversation struct {
	Message                  apiMessage `json:"message"`
	StarredMessageID         int64      `json:"starred_message_id"`
	Participants             string     `json:"participants"`
	Count                    int        `json:"count"`
	IsRead                   bool       `json:"is_read"`
	HasAttachments           bool       `json:"has_attachments"`
	AttachmentNames          []string   `json:"attachment_names,omitempty"`
	AttachmentMatches        []string   `json:"attachment_matches,omitempty"`
	AttachmentContentMatched bool       `json:"attachment_content_matched,omitempty"`
	Snippet                  string     `json:"snippet"`
	MatchTerms               []string   `json:"match_terms,omitempty"`
}

type apiAttachment struct {
	ID             int64  `json:"id"`
	Filename       string `json:"filename"`
	ContentType    string `json:"content_type"`
	Size           int64  `json:"size"`
	DownloadURL    string `json:"download_url"`
	Matched        bool   `json:"matched,omitempty"`
	ContentMatched bool   `json:"content_matched,omitempty"`
}

type apiThreadMessage struct {
	Message         apiMessage      `json:"message"`
	Attachments     []apiAttachment `json:"attachments"`
	HeaderDetails   []apiHeaderItem `json:"header_details"`
	OneClickUnsub   bool            `json:"one_click_unsubscribe"`
	OneClickSentAt  string          `json:"one_click_unsubscribe_sent_at"`
	SenderName      string          `json:"sender_name"`
	SenderEmail     string          `json:"sender_email"`
	SenderInitial   string          `json:"sender_initial"`
	RecipientLine   string          `json:"recipient_line"`
	Snippet         string          `json:"snippet"`
	BodyDoc         string          `json:"body_doc"`
	FullBodyDoc     string          `json:"full_body_doc"`
	HasHiddenQuoted bool            `json:"has_hidden_quoted"`
	HasDisplayBody  bool            `json:"has_display_body"`
	BodyPreviewOnly bool            `json:"body_preview_only"`
	HasRemoteImages bool            `json:"has_remote_images"`
	ImagesAllowed   bool            `json:"images_allowed"`
	Expanded        bool            `json:"expanded"`
	ReplySubject    string          `json:"reply_subject"`
}

type apiHeaderItem struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type apiSyncRun struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
	UpdatedAt       string `json:"updated_at"`
	MessagesSeen    int    `json:"messages_seen"`
	MessagesStored  int    `json:"messages_stored"`
	MessagesSkipped int    `json:"messages_skipped"`
	NewMessages     int    `json:"new_messages"`
	MessagesTotal   int    `json:"messages_total"`
	MailboxesDone   int    `json:"mailboxes_done"`
	MailboxesTotal  int    `json:"mailboxes_total"`
	CurrentMailbox  string `json:"current_mailbox"`
	CurrentUID      uint32 `json:"current_uid"`
	Error           string `json:"error"`
}

type apiSyncFolder struct {
	Mailbox    apiMailbox  `json:"mailbox"`
	IsRunning  bool        `json:"is_running"`
	LastRun    *apiSyncRun `json:"last_run"`
	CanSyncNow bool        `json:"can_sync_now"`
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	switch {
	case path == "bootstrap":
		s.apiBootstrap(w, r)
	case path == "setup":
		s.apiSetup(w, r)
	case path == "login":
		s.apiLogin(w, r)
	case path == "logout":
		s.apiLogout(w, r)
	case path == "profile":
		s.apiProfile(w, r)
	case path == "mail":
		s.apiMail(w, r)
	case path == "search":
		s.apiSearch(w, r)
	case path == "compose":
		s.apiCompose(w, r)
	case path == "sync/status":
		s.apiSyncStatus(w, r)
	case path == "events":
		s.apiEvents(w, r)
	case path == "storage":
		s.apiStorage(w, r)
	case path == "brand-icons":
		s.apiBrandIcons(w, r)
	case path == "account":
		s.apiAccount(w, r)
	case path == "account/sync":
		s.apiAccountSync(w, r)
	case strings.HasPrefix(path, "account/folders/"):
		s.apiAccountFolder(w, r, strings.TrimPrefix(path, "account/folders/"))
	case path == "admin/users":
		s.apiAdminUsers(w, r)
	case path == "admin/remote-image-blocklist":
		s.apiAdminRemoteImageBlocklist(w, r)
	case path == "messages/bulk-move":
		s.apiBulkMoveMessages(w, r)
	case strings.HasPrefix(path, "messages/"):
		s.apiMessagePath(w, r, strings.TrimPrefix(path, "messages/"))
	case strings.HasPrefix(path, "sync-runs/"):
		s.apiSyncRun(w, r, strings.TrimPrefix(path, "sync-runs/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) apiBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	resp := map[string]any{
		"users_exist": s.usersExist(r.Context()),
		"csrf":        s.csrfToken(w, r),
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
	} else {
		resp["user"] = nil
		resp["mailboxes"] = []apiMailbox{}
		resp["active_sync_runs"] = []apiSyncRun{}
		resp["account_needs_password"] = false
		resp["account_notice"] = ""
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
		var in struct {
			DateLocale string `json:"date_locale"`
			DateFormat string `json:"date_format"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		user, err := s.store.UpdateUserDisplayPreferences(r.Context(), cu.User.ID, in.DateLocale, in.DateFormat)
		if err != nil {
			s.serverError(w, err)
			return
		}
		writeJSON(w, map[string]any{"user": safeUser(user)})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) apiMail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	const pageSize = 50
	page := pageFromRequest(r)
	offset := (page - 1) * pageSize
	fetchLimit := pageSize*3 + 1
	var messages []store.MessageRecord
	var activeMailbox *apiMailbox
	var err error
	if raw := strings.TrimSpace(r.URL.Query().Get("mailbox")); raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			http.NotFound(w, r)
			return
		}
		mb, mbErr := s.store.GetMailboxForUser(r.Context(), cu.User.ID, id)
		if store.IsNotFound(mbErr) {
			http.NotFound(w, r)
			return
		}
		if mbErr != nil {
			s.serverError(w, mbErr)
			return
		}
		active := apiMailboxFromStore(mb)
		activeMailbox = &active
		messages, err = s.store.ListLatestThreadMessagesForMailbox(r.Context(), cu.User.ID, mb.ID, fetchLimit, offset)
	} else {
		messages, err = s.store.ListLatestThreadMessagesForUser(r.Context(), cu.User.ID, fetchLimit, offset)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	own := s.ownAddresses(r.Context(), cu.User)
	conversations, err := s.conversationViews(r.Context(), cu.User.ID, messages, own)
	if err != nil {
		s.serverError(w, err)
		return
	}
	hasNext := len(conversations) > pageSize
	if hasNext {
		conversations = conversations[:pageSize]
	}
	writeJSON(w, map[string]any{
		"conversations":  apiConversations(conversations),
		"page":           page,
		"has_prev":       page > 1,
		"has_next":       hasNext,
		"active_mailbox": activeMailbox,
	})
}

func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	const pageSize = 50
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	sortMode := search.SortMode(r.URL.Query().Get("sort"))
	if sortMode != search.SortRecent {
		sortMode = search.SortBest
	}
	page := pageFromRequest(r)
	offset := (page - 1) * pageSize
	own := s.ownAddresses(r.Context(), cu.User)
	var seeds []conversationSeed
	var err error
	if q == "" {
		var messages []store.MessageRecord
		messages, err = s.store.ListLatestThreadMessagesForUser(r.Context(), cu.User.ID, pageSize*3+1, offset)
		seeds = conversationSeedsFromMessages(messages)
	} else {
		opts := search.SearchOptions{}
		if sortMode == search.SortBest {
			if stats, statsErr := s.store.ListReadSenderStatsForUser(r.Context(), cu.User.ID, 40); statsErr == nil {
				for _, stat := range stats {
					opts.SenderBoosts = append(opts.SenderBoosts, search.SenderBoost{Sender: stat.Sender, Boost: stat.Boost})
				}
			}
		}
		seeds, err = s.searchConversationSeedHits(r.Context(), cu.User.ID, q, sortMode, page, pageSize, opts, own)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	conversations, err := s.conversationViewsWithSearchDetails(r.Context(), cu.User.ID, seeds, own, q)
	if err != nil {
		s.serverError(w, err)
		return
	}
	hasNext := len(conversations) > pageSize
	if hasNext {
		conversations = conversations[:pageSize]
	}
	writeJSON(w, map[string]any{
		"conversations": apiConversations(conversations),
		"page":          page,
		"has_prev":      page > 1,
		"has_next":      hasNext,
		"query":         q,
		"sort":          string(sortMode),
	})
}

func (s *Server) apiMessagePath(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		s.apiMessage(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "move" {
		s.apiMoveMessage(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "star" {
		s.apiSetMessageStarred(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "unsubscribe" {
		s.apiOneClickUnsubscribe(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "images" && parts[2] == "trust" {
		s.apiTrustImages(w, r, id)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) apiMessage(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	msg, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, id)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	views, msg, err := s.threadViewsForMessage(r.Context(), cu, msg, r.URL.Query().Get("images") == "1", r.URL.Query().Get("q"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"message":        apiMessageFromRecord(msg, msg.BodyText),
		"thread":         apiThreadMessages(views),
		"compose_from":   s.composeFromLabel(r.Context(), cu),
		"mailbox_id":     msg.MailboxID,
		"raw_blob_url":   fmt.Sprintf("/blobs/%d", msg.BlobID),
		"conversation":   len(views),
		"showing_images": r.URL.Query().Get("images") == "1",
	})
}

func (s *Server) apiMoveMessage(w http.ResponseWriter, r *http.Request, id int64) {
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
		MailboxID int64 `json:"mailbox_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if s.syncer == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "IMAP sync is not configured")
		return
	}
	dest, err := s.store.GetMailboxForUser(r.Context(), cu.User.ID, in.MailboxID)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	refreshMailboxes, err := s.moveRefreshMailboxNames(r.Context(), cu.User.ID, []int64{id}, dest)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.syncer.MoveMessage(r.Context(), cu.User.ID, id, in.MailboxID); err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		writeAPIError(w, http.StatusBadGateway, "could not move message")
		return
	}
	if s.syncRunner != nil {
		s.syncRunner.StartMailboxes(cu.User.ID, refreshMailboxes)
	}
	writeJSON(w, map[string]any{"ok": true, "mailbox": dest.Name})
}

func (s *Server) moveRefreshMailboxNames(ctx context.Context, userID int64, messageIDs []int64, dest store.Mailbox) ([]string, error) {
	seen := map[string]bool{}
	names := make([]string, 0, 2)
	add := func(name string) {
		name = strings.TrimSpace(name)
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		names = append(names, name)
	}
	messages, err := s.store.ListMessagesByIDsForUser(ctx, userID, messageIDs)
	if err != nil {
		return nil, err
	}
	mailboxes := map[int64]store.Mailbox{}
	for _, msg := range messages {
		if msg.MailboxID == 0 {
			continue
		}
		mb, ok := mailboxes[msg.MailboxID]
		if !ok {
			var err error
			mb, err = s.store.GetMailboxForUser(ctx, userID, msg.MailboxID)
			if store.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			mailboxes[msg.MailboxID] = mb
		}
		add(mb.Name)
	}
	add(dest.Name)
	return names, nil
}

func (s *Server) apiBulkMoveMessages(w http.ResponseWriter, r *http.Request) {
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
		writeAPIError(w, http.StatusServiceUnavailable, "IMAP sync is not configured")
		return
	}
	var in struct {
		MessageIDs []int64 `json:"message_ids"`
		MailboxID  int64   `json:"mailbox_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.MessageIDs) == 0 || len(in.MessageIDs) > 1000 || in.MailboxID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "select messages and a destination folder")
		return
	}
	dest, err := s.store.GetMailboxForUser(r.Context(), cu.User.ID, in.MailboxID)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	refreshMailboxes, err := s.moveRefreshMailboxNames(r.Context(), cu.User.ID, in.MessageIDs, dest)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(in.MessageIDs) > 5 {
		run, err := s.syncer.StartMoveMessages(r.Context(), cu.User.ID, in.MessageIDs, in.MailboxID, func() {
			if s.syncRunner != nil {
				s.syncRunner.StartMailboxes(cu.User.ID, refreshMailboxes)
			}
		})
		if err != nil {
			if store.IsNotFound(err) {
				http.NotFound(w, r)
				return
			}
			writeAPIError(w, http.StatusBadGateway, "could not start bulk move")
			return
		}
		writeJSON(w, map[string]any{"ok": true, "queued": true, "run_id": run.ID, "mailbox": dest.Name})
		return
	}
	moved, err := s.syncer.MoveMessages(r.Context(), cu.User.ID, in.MessageIDs, in.MailboxID)
	if err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		writeAPIError(w, http.StatusBadGateway, "could not move messages")
		return
	}
	if s.syncRunner != nil {
		s.syncRunner.StartMailboxes(cu.User.ID, refreshMailboxes)
	}
	writeJSON(w, map[string]any{"ok": true, "queued": false, "moved": moved, "mailbox": dest.Name})
}

func (s *Server) apiSetMessageStarred(w http.ResponseWriter, r *http.Request, id int64) {
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
		writeAPIError(w, http.StatusServiceUnavailable, "IMAP sync is not configured")
		return
	}
	var in struct {
		Starred bool `json:"starred"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	msg, err := s.syncer.SetStarredForMessage(r.Context(), cu.User.ID, id, in.Starred)
	if err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	go func(userID, messageID int64) {
		if err := s.syncer.SyncStarStateForMessage(context.Background(), userID, messageID); err != nil {
			log.Printf("sync starred flag user_id=%d message_id=%d: %v", userID, messageID, err)
		}
		s.events.Notify(userID)
	}(cu.User.ID, msg.ID)
	s.events.Notify(cu.User.ID)
	writeJSON(w, map[string]any{"ok": true, "message": apiMessageFromRecord(msg, msg.BodyText)})
}

func (s *Server) apiOneClickUnsubscribe(w http.ResponseWriter, r *http.Request, id int64) {
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
	msg, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, id)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	target, ok := s.oneClickUnsubscribeTarget(r.Context(), cu.User.ID, msg)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "This message does not advertise RFC 8058 one-click unsubscribe.")
		return
	}
	if previous, prevErr := s.store.LatestOneClickUnsubscribeSend(r.Context(), cu.User.ID, msg.ID, target.String(), time.Now().Add(-oneClickUnsubscribeRecentWindow)); prevErr == nil {
		writeJSON(w, map[string]any{"ok": true, "already_sent": true, "sent_at": timeString(previous.SentAt)})
		return
	}
	if err := s.performOneClickUnsubscribe(r.Context(), target); err != nil {
		if errors.Is(err, errOneClickUnavailable) {
			writeAPIError(w, http.StatusBadRequest, "This message does not advertise RFC 8058 one-click unsubscribe.")
			return
		}
		writeAPIError(w, http.StatusBadGateway, "Unsubscribe request failed.")
		return
	}
	sentAt := time.Now()
	if err := s.store.RecordOneClickUnsubscribeSend(r.Context(), cu.User.ID, msg.ID, msg.FromAddr, target.String(), sentAt); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "already_sent": false, "sent_at": timeString(sentAt)})
}

func (s *Server) apiTrustImages(w http.ResponseWriter, r *http.Request, id int64) {
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
	msg, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, id)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.store.TrustImageSender(r.Context(), cu.User.ID, msg.FromAddr); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiCompose(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		form, err := s.composeFormForRequest(r)
		if err != nil {
			if store.IsNotFound(err) {
				http.NotFound(w, r)
				return
			}
			s.serverError(w, err)
			return
		}
		writeJSON(w, map[string]any{"compose": form, "compose_from": s.composeFromLabel(r.Context(), cu)})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var form composeForm
		if !decodeJSON(w, r, &form) {
			return
		}
		sent, err := s.sendCompose(r.Context(), cu, form)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "Could not send message.")
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message_id": sent.ID})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) apiSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	var data viewData
	s.loadMailboxChrome(r.Context(), cu.User.ID, &data)
	writeJSON(w, map[string]any{
		"running":          data.SyncRunning,
		"latest":           apiSyncRunPtr(data.LatestSyncRun),
		"active_sync_runs": apiSyncRuns(data.ActiveSyncRuns),
	})
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "event streaming is not available")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsubscribe := s.events.Subscribe(cu.User.ID)
	defer unsubscribe()

	writeEvent := func() bool {
		payload, err := s.syncEventPayload(r.Context(), cu.User.ID)
		if err != nil {
			return false
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: chrome\ndata: %s\n\n", raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !writeEvent() {
		return
	}
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-ch:
			if !ok || !writeEvent() {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) syncEventPayload(ctx context.Context, userID int64) (map[string]any, error) {
	var data viewData
	s.loadMailboxChrome(ctx, userID, &data)
	return map[string]any{
		"mailboxes":        apiMailboxes(data.Mailboxes),
		"latest_sync_run":  apiSyncRunPtr(data.LatestSyncRun),
		"active_sync_runs": apiSyncRuns(data.ActiveSyncRuns),
		"sync_running":     data.SyncRunning,
		"storage_retained": true,
	}, nil
}

func (s *Server) apiAccount(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		account, _ := s.store.GetMailAccount(r.Context(), cu.User.ID)
		if account.ID != 0 && s.syncer != nil && s.syncer.Fetcher != nil {
			s.refreshMailboxStatusesAsync(cu.User.ID)
		}
		runs, err := s.store.ListSyncRunsForUser(r.Context(), cu.User.ID, 20)
		if err != nil {
			s.serverError(w, err)
			return
		}
		var accountOut any
		if account.ID != 0 {
			accountOut = apiAccountFromStore(account)
		}
		needsPassword, notice := s.accountCredentialNotice(r.Context(), cu.User.ID)
		writeJSON(w, map[string]any{
			"account":                accountOut,
			"sync_runs":              apiSyncRuns(runs),
			"sync_folders":           apiSyncFolders(s.syncFolderViews(r.Context(), cu.User.ID, runs)),
			"notice":                 notice,
			"account_needs_password": needsPassword,
		})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var in struct {
			Email               string `json:"email"`
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
		if !decodeJSON(w, r, &in) {
			return
		}
		if in.SMTPSameAsIMAP && in.SMTPPort == 0 {
			in.SMTPPort = 587
		}
		if in.Port <= 0 || in.Port > 65535 || in.SMTPPort <= 0 || in.SMTPPort > 65535 {
			writeAPIError(w, http.StatusBadRequest, "Ports must be valid TCP ports.")
			return
		}
		existing, existingErr := s.store.GetMailAccount(r.Context(), cu.User.ID)
		encrypted := ""
		if in.Password == "" && existingErr == nil {
			if _, err := mmcrypto.DecryptString(s.masterKey, existing.EncryptedPassword); err != nil {
				writeAPIError(w, http.StatusBadRequest, "Saved IMAP password cannot be decrypted with the current master key. Re-enter the IMAP password.")
				return
			}
			encrypted = existing.EncryptedPassword
		} else if in.Password != "" {
			var err error
			encrypted, err = mmcrypto.EncryptString(s.masterKey, in.Password)
			if err != nil {
				s.serverError(w, err)
				return
			}
		} else {
			writeAPIError(w, http.StatusBadRequest, "IMAP password is required for a new account.")
			return
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
				s.serverError(w, err)
				return
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
		_, err := s.store.UpsertMailAccount(r.Context(), store.MailAccount{
			UserID:                cu.User.ID,
			Email:                 in.Email,
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
		})
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "Could not save account settings.")
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) apiStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, ok := s.requireAPIAuth(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	writeJSON(w, s.cachedStorageStats())
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
	if len(parts) == 3 && parts[1] == "search-index" && parts[2] == "rebuild" {
		action = "rebuild-search-index"
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
		s.events.Notify(cu.User.ID)
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
				s.events.Notify(userID)
			}(cu.User.ID, mailboxID, in.IncludeInSearch)
		}
		s.events.Notify(cu.User.ID)
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
		if !s.syncRunner.StartMailboxes(cu.User.ID, []string{mb.Name}) {
			writeAPIError(w, http.StatusConflict, "Sync is already running for this folder.")
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case "rebuild-search-index":
		if s.syncer == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "Search indexing is not configured.")
			return
		}
		run, err := s.syncer.StartRebuildMailboxSearchIndex(r.Context(), cu.User.ID, mb.ID, func() {
			s.events.Notify(cu.User.ID)
		})
		if err != nil {
			if store.IsNotFound(err) {
				http.NotFound(w, r)
				return
			}
			writeAPIError(w, http.StatusBadGateway, "could not start index rebuild")
			return
		}
		writeJSON(w, map[string]any{"ok": true, "run_id": run.ID})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) apiAdminUsers(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAdmin(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		users, err := s.store.ListUsers(r.Context())
		if err != nil {
			s.serverError(w, err)
			return
		}
		out := make([]apiUser, 0, len(users))
		for _, user := range users {
			out = append(out, safeUser(user))
		}
		writeJSON(w, map[string]any{"users": out})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var in struct {
			Email    string `json:"email"`
			Name     string `json:"name"`
			Password string `json:"password"`
			IsAdmin  bool   `json:"is_admin"`
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
		_, err = s.store.CreateUser(r.Context(), in.Email, in.Name, hash, in.IsAdmin)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "Could not create user.")
			return
		}
		_ = cu
		writeJSON(w, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) apiAdminRemoteImageBlocklist(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		rules, err := s.store.ListRemoteImageBlockRules(r.Context())
		if err != nil {
			s.serverError(w, err)
			return
		}
		patterns := make([]string, 0, len(rules))
		for _, rule := range rules {
			if rule.Enabled {
				patterns = append(patterns, rule.Pattern)
			}
		}
		writeJSON(w, map[string]any{"patterns": patterns})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var in struct {
			Patterns []string `json:"patterns"`
			Text     string   `json:"text"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		patterns := in.Patterns
		if patterns == nil && strings.TrimSpace(in.Text) != "" {
			patterns = strings.Split(in.Text, "\n")
		}
		patterns, err := normalizeRemoteImageBlockPatterns(patterns)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.store.ReplaceRemoteImageBlockRules(r.Context(), patterns); err != nil {
			s.serverError(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "patterns": patterns})
	default:
		methodNotAllowed(w)
	}
}

func normalizeRemoteImageBlockPatterns(patterns []string) ([]string, error) {
	const maxPatterns = 100
	const maxPatternLen = 500
	out := make([]string, 0, len(patterns))
	seen := map[string]bool{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		if len(pattern) > maxPatternLen {
			return nil, fmt.Errorf("Blocklist regex is too long; keep each pattern under %d characters.", maxPatternLen)
		}
		if seen[pattern] {
			continue
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("Invalid blocklist regex: %v", err)
		}
		seen[pattern] = true
		out = append(out, pattern)
		if len(out) > maxPatterns {
			return nil, fmt.Errorf("Keep the remote image blocklist under %d patterns.", maxPatterns)
		}
	}
	return out, nil
}

func (s *Server) apiSyncRun(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(strings.Trim(rest, "/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	run, err := s.store.GetSyncRunForUser(r.Context(), cu.User.ID, id)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"sync_run": apiSyncRunFrom(run)})
}

func (s *Server) composeFormForRequest(r *http.Request) (composeForm, error) {
	if raw := strings.TrimSpace(r.URL.Query().Get("reply")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return composeForm{}, store.ErrNotFound
		}
		cu, _ := current(r)
		msg, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, id)
		if err != nil {
			return composeForm{}, err
		}
		return replyComposeForm(msg), nil
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("forward")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return composeForm{}, store.ErrNotFound
		}
		cu, _ := current(r)
		msg, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, id)
		if err != nil {
			return composeForm{}, err
		}
		return forwardComposeForm(msg), nil
	}
	return composeForm{}, nil
}

func (s *Server) sendCompose(ctx context.Context, cu currentUser, form composeForm) (store.MessageRecord, error) {
	if s.sender == nil {
		return store.MessageRecord{}, errors.New("SMTP sending is not configured")
	}
	account, err := s.store.GetMailAccount(ctx, cu.User.ID)
	if err != nil {
		return store.MessageRecord{}, err
	}
	msg := smtpclient.Message{
		From:      account.Email,
		To:        []string{form.To},
		Cc:        []string{form.Cc},
		Bcc:       []string{form.Bcc},
		Subject:   form.Subject,
		BodyText:  form.Body,
		BodyHTML:  form.BodyHTML,
		MessageID: smtpclient.NewMessageID(account.Email),
		Date:      time.Now(),
	}
	if form.InReplyToID > 0 {
		reply, err := s.store.GetMessageForUser(ctx, cu.User.ID, form.InReplyToID)
		if err != nil && !store.IsNotFound(err) {
			return store.MessageRecord{}, err
		}
		if err == nil {
			msg.InReplyTo = reply.MessageIDHeader
			msg.References = referencesForReply(reply)
		}
	}
	raw, err := s.sender.Send(ctx, account, msg)
	if err != nil {
		return store.MessageRecord{}, err
	}
	return s.storeSentMessage(ctx, cu.User.ID, account, msg, form, raw)
}

func (s *Server) threadViewsForMessage(ctx context.Context, cu currentUser, msg store.MessageRecord, showImages bool, query string) ([]threadMessageView, store.MessageRecord, error) {
	threadMessages, err := s.store.ListThreadMessagesForUser(ctx, cu.User.ID, msg)
	if err != nil {
		return nil, msg, err
	}
	threadViews := make([]threadMessageView, 0, len(threadMessages))
	previousBodies := make([]string, 0, len(threadMessages))
	imageBlockRules, err := s.store.ListRemoteImageBlockPatterns(ctx)
	if err != nil {
		return nil, msg, err
	}
	matchDetails := s.threadSearchMatchDetails(ctx, cu.User.ID, threadMessages, query)
	for idx, threadMsg := range threadMessages {
		if !threadMsg.IsRead {
			if err := s.store.MarkMessageReadForUser(ctx, cu.User.ID, threadMsg.ID, true, true); err == nil {
				threadMsg.IsRead = true
				threadMsg.ReadSyncPending = true
				if s.syncer != nil {
					go func(userID, messageID int64) {
						_ = s.syncer.SyncReadStateForMessage(context.Background(), userID, messageID)
					}(cu.User.ID, threadMsg.ID)
				}
			}
		}
		attachments, err := s.store.ListAttachmentsForMessage(ctx, cu.User.ID, threadMsg.ID)
		if err != nil {
			return nil, msg, err
		}
		attachments = visibleAttachments(attachments)
		sourceHTML, sourceText, previewOnly := s.displayBodiesForMessage(ctx, cu.User.ID, threadMsg)
		displayMsg := threadMsg
		displayMsg.BodyHTML = sourceHTML
		displayMsg.BodyText = sourceText
		displayHTML, displayText, hiddenQuoted := clippedEmailBody(sourceHTML, sourceText, previousBodies)
		remoteImages := hasRemoteImages(sourceHTML)
		imagesAllowed := showImages
		if remoteImages && !imagesAllowed {
			if trusted, trustErr := s.store.IsImageSenderTrusted(ctx, cu.User.ID, threadMsg.FromAddr); trustErr == nil && trusted {
				imagesAllowed = true
			}
		}
		oneClickTarget, oneClickUnsub := s.oneClickUnsubscribeTarget(ctx, cu.User.ID, threadMsg)
		oneClickSentAt := time.Time{}
		if oneClickUnsub {
			oneClickSentAt = s.recentOneClickUnsubscribeSentAt(ctx, cu.User.ID, threadMsg, oneClickTarget.String())
		}
		attachmentMatches, attachmentContentMatched := attachmentSearchMatches(attachments, matchDetails[threadMsg.ID], query)
		threadViews = append(threadViews, threadMessageView{
			Message:                  displayMsg,
			Attachments:              attachments,
			HeaderDetails:            s.messageHeaderDetails(ctx, cu.User.ID, threadMsg),
			OneClickUnsub:            oneClickUnsub,
			OneClickSentAt:           oneClickSentAt,
			AttachmentMatches:        attachmentMatches,
			AttachmentContentMatched: attachmentContentMatched,
			SenderName:               senderDisplayName(displayMsg.FromAddr),
			SenderEmail:              senderEmail(displayMsg.FromAddr),
			SenderInitial:            senderInitial(displayMsg.FromAddr),
			RecipientLine:            recipientLine(displayMsg),
			Snippet:                  threadSnippet(displayText, sourceText),
			DisplayBodyHTML:          displayHTML,
			DisplayBodyText:          displayText,
			HasHiddenQuoted:          hiddenQuoted,
			HasDisplayBody:           strings.TrimSpace(displayHTML) != "" || strings.TrimSpace(displayText) != "",
			BodyPreviewOnly:          previewOnly,
			HasRemoteImages:          remoteImages,
			ImagesAllowed:            imagesAllowed,
			ImageBlockRules:          imageBlockRules,
			Expanded:                 idx == len(threadMessages)-1 || threadMsg.ID == msg.ID || len(threadMessages) == 1,
		})
		previousBodies = append(previousBodies, sourceText)
		if threadMsg.ID == msg.ID {
			msg = displayMsg
		}
	}
	return threadViews, msg, nil
}

type threadSearchMatch struct {
	Terms  []string
	Fields []string
}

func (s *Server) threadSearchMatchDetails(ctx context.Context, userID int64, messages []store.MessageRecord, query string) map[int64]threadSearchMatch {
	out := map[int64]threadSearchMatch{}
	query, _ = stripStarSearchOperators(strings.TrimSpace(query))
	if query == "" || s.search == nil {
		return out
	}
	for _, msg := range messages {
		hit, ok, err := s.search.MatchMessage(ctx, userID, msg.ID, query)
		if err != nil || !ok {
			continue
		}
		out[msg.ID] = threadSearchMatch{Terms: hit.Terms, Fields: hit.Fields}
	}
	return out
}

func attachmentSearchMatches(attachments []store.Attachment, match threadSearchMatch, query string) ([]string, bool) {
	if !searchFieldsInclude(match.Fields, "attachment_names", "attachments", "attachment_types") {
		return nil, false
	}
	needles := mergeSnippetTerms(match.Terms, searchSnippetTerms(query))
	var matches []string
	if searchFieldsInclude(match.Fields, "attachment_names") {
		for _, att := range attachments {
			name := attachmentDisplayName(att)
			if name != "" && attachmentNameMatches(name, needles) {
				matches = append(matches, name)
			}
		}
	}
	return uniqueStrings(matches, len(matches)), searchFieldsInclude(match.Fields, "attachments") && len(matches) == 0
}

func visibleAttachments(attachments []store.Attachment) []store.Attachment {
	out := make([]store.Attachment, 0, len(attachments))
	for _, att := range attachments {
		if isDisplayAttachment(att) {
			out = append(out, att)
		}
	}
	return out
}

func isDisplayAttachment(att store.Attachment) bool {
	if att.IsInline {
		return false
	}
	filename := strings.ToLower(strings.TrimSpace(att.Filename))
	contentID := strings.TrimSpace(att.ContentID)
	contentType := strings.ToLower(strings.TrimSpace(att.ContentType))
	if contentID != "" && strings.HasPrefix(contentType, "image/") {
		return false
	}
	if strings.HasPrefix(contentType, "image/") && strings.HasPrefix(filename, "outlook-") && att.Size <= 256*1024 {
		return false
	}
	return true
}

func attachmentDisplayName(att store.Attachment) string {
	name := strings.TrimSpace(att.Filename)
	if name == "" {
		name = strings.TrimSpace(att.ContentType)
	}
	if name == "" {
		name = "Attachment"
	}
	return name
}

func stringInSliceFold(value string, values []string) bool {
	value = strings.TrimSpace(value)
	for _, candidate := range values {
		if strings.EqualFold(value, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func safeUser(user store.User) apiUser {
	return apiUser{
		ID:         user.ID,
		Email:      user.Email,
		Name:       user.Name,
		IsAdmin:    user.IsAdmin,
		DateLocale: user.DateLocale,
		DateFormat: user.DateFormat,
	}
}

func apiMailboxes(boxes []store.MailboxSummary) []apiMailbox {
	out := make([]apiMailbox, 0, len(boxes))
	for _, box := range boxes {
		out = append(out, apiMailboxFromSummary(box))
	}
	return out
}

func apiMailboxFromSummary(box store.MailboxSummary) apiMailbox {
	return apiMailbox{
		ID:                 box.ID,
		AccountID:          box.AccountID,
		AccountEmail:       box.AccountEmail,
		Name:               box.Name,
		MessageCount:       box.MessageCount,
		UnreadCount:        box.UnreadCount,
		SyncMode:           box.SyncMode,
		Role:               box.Role,
		Icon:               box.Icon,
		ShowInSidebar:      box.ShowInSidebar,
		ShowInAllMail:      box.ShowInAllMail,
		IncludeInSearch:    box.IncludeInSearch,
		LastUID:            box.LastUID,
		RemoteMessageCount: box.RemoteMessageCount,
		RemoteUnreadCount:  box.RemoteUnreadCount,
		RemoteUIDNext:      box.RemoteUIDNext,
		SyncPercent:        box.SyncPercent,
	}
}

func apiMailboxFromStore(box store.Mailbox) apiMailbox {
	return apiMailbox{
		ID:                 box.ID,
		AccountID:          box.AccountID,
		Name:               box.Name,
		SyncMode:           box.SyncMode,
		Role:               box.Role,
		Icon:               box.Icon,
		ShowInSidebar:      box.ShowInSidebar,
		ShowInAllMail:      box.ShowInAllMail,
		IncludeInSearch:    box.IncludeInSearch,
		LastUID:            box.LastUID,
		RemoteMessageCount: box.RemoteMessageCount,
		RemoteUnreadCount:  box.RemoteUnreadCount,
		RemoteUIDNext:      box.RemoteUIDNext,
	}
}

func apiAccountFromStore(account store.MailAccount) apiAccount {
	return apiAccount{
		ID:                  account.ID,
		Email:               account.Email,
		Host:                account.Host,
		Port:                account.Port,
		Username:            account.Username,
		UseTLS:              account.UseTLS,
		SMTPHost:            account.SMTPHost,
		SMTPPort:            account.SMTPPort,
		SMTPUsername:        account.SMTPUsername,
		SMTPUseTLS:          account.SMTPUseTLS,
		SMTPSameAsIMAP:      sameSMTPSettings(account),
		Mailbox:             account.Mailbox,
		SyncIntervalMinutes: account.SyncIntervalMinutes,
	}
}

func sameSMTPSettings(account store.MailAccount) bool {
	return strings.EqualFold(strings.TrimSpace(account.SMTPHost), strings.TrimSpace(account.Host)) &&
		strings.TrimSpace(account.SMTPUsername) == strings.TrimSpace(account.Username) &&
		account.EncryptedSMTPPassword == account.EncryptedPassword &&
		account.SMTPUseTLS == account.UseTLS
}

func (s *Server) accountCredentialNotice(ctx context.Context, userID int64) (bool, string) {
	account, err := s.store.GetMailAccount(ctx, userID)
	if err != nil || account.ID == 0 || strings.TrimSpace(account.EncryptedPassword) == "" {
		return false, ""
	}
	if _, err := mmcrypto.DecryptString(s.masterKey, account.EncryptedPassword); err != nil {
		return true, "IMAP password required: the saved password cannot be decrypted with the current MAILMIRROR_MASTER_KEY. Re-enter the IMAP password to restore sync and full-message loading."
	}
	return false, ""
}

func apiMessageFromRecord(msg store.MessageRecord, snippet string) apiMessage {
	return apiMessage{
		ID:             msg.ID,
		MailboxID:      msg.MailboxID,
		Subject:        msg.Subject,
		FromAddr:       msg.FromAddr,
		ToAddr:         msg.ToAddr,
		CCAddr:         msg.CCAddr,
		Date:           timeString(msg.Date),
		DateShort:      shortDateString(msg.Date),
		IsRead:         msg.IsRead,
		IsStarred:      msg.IsStarred,
		HasAttachments: msg.HasAttachments,
		Snippet:        snippet,
	}
}

func apiConversations(conversations []conversationView) []apiConversation {
	out := make([]apiConversation, 0, len(conversations))
	for _, conv := range conversations {
		out = append(out, apiConversation{
			Message:                  apiMessageFromRecord(conv.Message, conv.Snippet),
			StarredMessageID:         conv.StarredMessageID,
			Participants:             conv.Participants,
			Count:                    conv.Count,
			IsRead:                   conv.IsRead,
			HasAttachments:           conv.HasAttachments,
			AttachmentNames:          conv.AttachmentNames,
			AttachmentMatches:        conv.AttachmentMatches,
			AttachmentContentMatched: conv.AttachmentContentMatched,
			Snippet:                  conv.Snippet,
			MatchTerms:               conv.MatchTerms,
		})
	}
	return out
}

func apiThreadMessages(views []threadMessageView) []apiThreadMessage {
	out := make([]apiThreadMessage, 0, len(views))
	for _, view := range views {
		atts := make([]apiAttachment, 0, len(view.Attachments))
		for _, att := range view.Attachments {
			name := attachmentDisplayName(att)
			nameMatched := stringInSliceFold(name, view.AttachmentMatches)
			contentMatched := view.AttachmentContentMatched
			atts = append(atts, apiAttachment{
				ID:             att.ID,
				Filename:       att.Filename,
				ContentType:    att.ContentType,
				Size:           att.Size,
				DownloadURL:    fmt.Sprintf("/attachments/%d/download", att.ID),
				Matched:        nameMatched || contentMatched,
				ContentMatched: contentMatched,
			})
		}
		fullDoc := ""
		if view.HasHiddenQuoted {
			fullDoc = emailDocumentWithBlocklist(view.Message.BodyHTML, view.Message.BodyText, view.ImagesAllowed, view.ImageBlockRules)
		}
		out = append(out, apiThreadMessage{
			Message:         apiMessageFromRecord(view.Message, view.Snippet),
			Attachments:     atts,
			HeaderDetails:   apiHeaderDetails(view.HeaderDetails),
			OneClickUnsub:   view.OneClickUnsub,
			OneClickSentAt:  timeString(view.OneClickSentAt),
			SenderName:      view.SenderName,
			SenderEmail:     view.SenderEmail,
			SenderInitial:   view.SenderInitial,
			RecipientLine:   view.RecipientLine,
			Snippet:         view.Snippet,
			BodyDoc:         emailDocumentWithBlocklist(view.DisplayBodyHTML, view.DisplayBodyText, view.ImagesAllowed, view.ImageBlockRules),
			FullBodyDoc:     fullDoc,
			HasHiddenQuoted: view.HasHiddenQuoted,
			HasDisplayBody:  view.HasDisplayBody,
			BodyPreviewOnly: view.BodyPreviewOnly,
			HasRemoteImages: view.HasRemoteImages,
			ImagesAllowed:   view.ImagesAllowed,
			Expanded:        view.Expanded,
			ReplySubject:    replySubject(view.Message.Subject),
		})
	}
	return out
}

func apiHeaderDetails(details []messageHeaderDetail) []apiHeaderItem {
	out := make([]apiHeaderItem, 0, len(details))
	for _, detail := range details {
		out = append(out, apiHeaderItem{Label: detail.Label, Value: detail.Value})
	}
	return out
}

func apiSyncRunPtr(run *store.SyncRun) *apiSyncRun {
	if run == nil {
		return nil
	}
	out := apiSyncRunFrom(*run)
	return &out
}

func apiSyncRunFrom(run store.SyncRun) apiSyncRun {
	return apiSyncRun{
		ID:              run.ID,
		Status:          run.Status,
		StartedAt:       timeString(run.StartedAt),
		FinishedAt:      timeString(run.FinishedAt),
		UpdatedAt:       timeString(run.UpdatedAt),
		MessagesSeen:    run.MessagesSeen,
		MessagesStored:  run.MessagesStored,
		MessagesSkipped: run.MessagesSkipped,
		NewMessages:     run.NewMessages,
		MessagesTotal:   run.MessagesTotal,
		MailboxesDone:   run.MailboxesDone,
		MailboxesTotal:  run.MailboxesTotal,
		CurrentMailbox:  run.CurrentMailbox,
		CurrentUID:      run.CurrentUID,
		Error:           run.Error,
	}
}

func apiSyncRuns(runs []store.SyncRun) []apiSyncRun {
	out := make([]apiSyncRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, apiSyncRunFrom(run))
	}
	return out
}

func apiSyncFolders(views []syncFolderView) []apiSyncFolder {
	out := make([]apiSyncFolder, 0, len(views))
	for _, view := range views {
		out = append(out, apiSyncFolder{
			Mailbox:    apiMailboxFromSummary(view.Mailbox),
			IsRunning:  view.IsRunning,
			LastRun:    apiSyncRunPtr(view.LastRun),
			CanSyncNow: view.CanSyncNow,
		})
	}
	return out
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(time.RFC3339)
}

func shortDateString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	local := t.Local()
	now := time.Now().Local()
	if local.Year() == now.Year() && local.YearDay() == now.YearDay() {
		return local.Format("3:04 PM")
	}
	if local.Year() == now.Year() {
		return local.Format("Jan 2")
	}
	return local.Format("1/2/06")
}

func (s *Server) requireAPIAuth(w http.ResponseWriter, r *http.Request) (currentUser, bool) {
	cu, ok := current(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "login required")
		return currentUser{}, false
	}
	return cu, true
}

func (s *Server) requireAPIAdmin(w http.ResponseWriter, r *http.Request) (currentUser, bool) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return currentUser{}, false
	}
	if !cu.User.IsAdmin {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return currentUser{}, false
	}
	return cu, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}
