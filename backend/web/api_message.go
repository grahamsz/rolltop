// File overview: Message detail, attachment, remote image, unsubscribe, and metadata API handlers.

package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mailmirror/backend/plugins"
	oneclickunsubscribe "mailmirror/backend/plugins/one_click_unsubscribe"
	remoteimageblocklist "mailmirror/backend/plugins/remote_image_blocklist"
	trustedimagesources "mailmirror/backend/plugins/trusted_image_sources"
	"mailmirror/backend/store"
)

// apiMessagePath is the subrouter for /api/messages/:id. It keeps per-message
// actions in one place while each action still performs auth, CSRF, and user-scoped
// store checks independently.
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
	if len(parts) == 2 && parts[1] == "load-status" {
		s.apiMessageLoadStatus(w, r, id)
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
		if !s.pluginEnabled(r.Context(), plugins.OneClickUnsubscribe) {
			http.NotFound(w, r)
			return
		}
		s.apiOneClickUnsubscribe(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "contacts" && parts[2] == "add-sender" {
		s.apiAddSenderContact(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "images" && parts[2] == "trust" {
		if !s.pluginEnabled(r.Context(), plugins.TrustedImageSources) {
			http.NotFound(w, r)
			return
		}
		s.apiTrustImages(w, r, id)
		return
	}
	http.NotFound(w, r)
}

// apiMessage returns the full thread around one message. threadViewsForMessage may
// hydrate pruned bodies from local blobs or IMAP before this method serializes the
// render-ready body docs for React.
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
	writeJSONCached(w, r, map[string]any{
		"message":         apiMessageFromRecord(msg, msg.BodyText),
		"thread":          s.apiThreadMessages(r.Context(), cu.User.ID, views),
		"compose_from":    s.composeFromLabel(r.Context(), cu),
		"from_identities": s.composeIdentities(r.Context(), cu),
		"mailbox_id":      msg.MailboxID,
		"raw_blob_url":    fmt.Sprintf("/blobs/%d", msg.BlobID),
		"conversation":    len(views),
		"showing_images":  r.URL.Query().Get("images") == "1",
	})
}

// apiMessageLoadStatus is a cheap preflight used by ThreadView to decide whether
// to show the "fetching from IMAP/local blob" dialog before the heavier message
// request finishes.
func (s *Server) apiMessageLoadStatus(w http.ResponseWriter, r *http.Request, id int64) {
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
	threadMessages, err := s.store.ListThreadMessagesForUser(r.Context(), cu.User.ID, msg)
	if err != nil {
		s.serverError(w, err)
		return
	}
	imapFetchCount := 0
	localBlobCount := 0
	indexedCount := 0
	unavailableCount := 0
	for _, threadMsg := range threadMessages {
		switch s.messageDisplayLoadSource(cu.User.ID, threadMsg) {
		case "imap":
			imapFetchCount++
		case "local_blob":
			localBlobCount++
		case "indexed":
			indexedCount++
		default:
			unavailableCount++
		}
	}
	source := "indexed"
	switch {
	case imapFetchCount > 0:
		source = "imap"
	case localBlobCount == len(threadMessages) && localBlobCount > 0:
		source = "local_blob"
	case localBlobCount > 0:
		source = "local"
	case unavailableCount > 0:
		source = "preview"
	}
	writeJSONCached(w, r, map[string]any{
		"conversation":      len(threadMessages),
		"imap_fetch_count":  imapFetchCount,
		"local_blob_count":  localBlobCount,
		"indexed_count":     indexedCount,
		"unavailable_count": unavailableCount,
		"source":            source,
	})
}

// messageDisplayLoadSource classifies the cheapest available source for one
// message body: already indexed HTML, existing blob, IMAP fetch, or preview only.
func (s *Server) messageDisplayLoadSource(userID int64, msg store.MessageRecord) string {
	if strings.TrimSpace(msg.BodyHTML) != "" {
		return "indexed"
	}
	if strings.TrimSpace(msg.BlobPath) != "" && s.blobs != nil {
		f, err := s.blobs.OpenUserBlob(userID, msg.BlobPath)
		if err == nil {
			_ = f.Close()
			return "local_blob"
		}
	}
	if s.syncer != nil {
		return "imap"
	}
	return "preview"
}

// apiMoveMessage moves one message through the syncer and then schedules mailbox
// refreshes so local state catches up with the remote IMAP server.
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

// apiBulkMoveMessages does small batches inline and large batches as background
// syncer jobs so drag/drop remains responsive for bulk selections.
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
		s.notifyUserChanged(userID)
	}(cu.User.ID, msg.ID)
	s.notifyUserChanged(cu.User.ID)
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
	userDB, err := s.store.UserDB(r.Context(), cu.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if previous, prevErr := oneclickunsubscribe.LatestSend(r.Context(), userDB, cu.User.ID, msg.ID, target.String(), time.Now().Add(-oneClickUnsubscribeRecentWindow)); prevErr == nil {
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
	if err := oneclickunsubscribe.RecordSend(r.Context(), userDB, cu.User.ID, msg.ID, msg.FromAddr, target.String(), sentAt); err != nil {
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
	userDB, err := s.store.UserDB(r.Context(), cu.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := trustedimagesources.TrustSender(r.Context(), userDB, cu.User.ID, msg.FromAddr); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
func (s *Server) threadViewsForMessage(ctx context.Context, cu currentUser, msg store.MessageRecord, showImages bool, query string) ([]threadMessageView, store.MessageRecord, error) {
	threadMessages, err := s.store.ListThreadMessagesForUser(ctx, cu.User.ID, msg)
	if err != nil {
		return nil, msg, err
	}
	threadViews := make([]threadMessageView, 0, len(threadMessages))
	previousBodies := make([]string, 0, len(threadMessages))
	own := s.ownAddresses(ctx, cu.User)
	remoteImageBlockingEnabled := s.pluginEnabled(ctx, plugins.RemoteImageBlocklist)
	trustedImageSourcesEnabled := s.pluginEnabled(ctx, plugins.TrustedImageSources)
	trustedDB, trustedDBErr := s.store.UserDB(ctx, cu.User.ID)
	if trustedImageSourcesEnabled && trustedDBErr != nil {
		return nil, msg, trustedDBErr
	}
	var imageBlockRules []string
	if remoteImageBlockingEnabled {
		imageBlockRules, err = remoteimageblocklist.ListPatterns(ctx, s.store.DB())
		if err != nil {
			return nil, msg, err
		}
	}
	matchDetails := s.threadSearchMatchDetails(ctx, cu.User.ID, threadMessages, query)
	markedRead := false
	defer func() {
		if markedRead {
			s.notifyUserChanged(cu.User.ID)
		}
	}()
	for idx, threadMsg := range threadMessages {
		if !threadMsg.IsRead {
			if err := s.store.MarkMessageReadForUser(ctx, cu.User.ID, threadMsg.ID, true, true); err == nil {
				markedRead = true
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
		remoteImages := remoteImageBlockingEnabled && hasRemoteImages(sourceHTML)
		imagesAllowed := showImages || !remoteImageBlockingEnabled
		if remoteImageBlockingEnabled && trustedImageSourcesEnabled && remoteImages && !imagesAllowed {
			if trusted, trustErr := trustedimagesources.IsSenderTrusted(ctx, trustedDB, cu.User.ID, threadMsg.FromAddr); trustErr == nil && trusted {
				imagesAllowed = true
			}
		}
		oneClickTarget, oneClickUnsub := s.oneClickUnsubscribeTarget(ctx, cu.User.ID, threadMsg)
		if !s.pluginEnabled(ctx, plugins.OneClickUnsubscribe) {
			oneClickUnsub = false
		}
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
			CanReplyAll:              canReplyAll(threadMsg, threadMessages, own),
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
	cleanQuery, _, err := s.searchMailboxFilter(ctx, userID, query)
	if err != nil {
		return out
	}
	query = cleanQuery
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
