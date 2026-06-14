// File overview: Mailbox listing, search, pagination, and message flag API handlers.

package web

import (
	"net/http"
	"strconv"
	"strings"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

// apiMail returns a paged conversation list for All Mail or one mailbox. It asks
// SQLite for extra rows because conversation grouping can collapse several message
// rows into one visible thread.
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
	timing := newSearchTiming()
	page := pageFromRequest(r)
	offset := (page - 1) * pageSize
	fetchLimit := pageSize*3 + 1
	var mailboxID int64
	var messages []store.MessageRecord
	var activeMailbox *apiMailbox
	var err error
	if raw := strings.TrimSpace(r.URL.Query().Get("mailbox")); raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			http.NotFound(w, r)
			return
		}
		mailboxID = id
	}
	cacheKey := mailListCacheKey{UserID: cu.User.ID, MailboxID: mailboxID, Page: page}
	if s.writeMailListNotModifiedIfFresh(w, r, cacheKey) {
		return
	}
	generation := s.mailListGeneration(cu.User.ID)
	if mailboxID != 0 {
		mb, mbErr := s.store.GetMailboxForUser(r.Context(), cu.User.ID, mailboxID)
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
		hydrateDone := timing.measure(&timing.hydrate)
		messages, err = s.store.ListLatestThreadMessagesForMailbox(r.Context(), cu.User.ID, mb.ID, fetchLimit, offset)
		hydrateDone()
	} else {
		hydrateDone := timing.measure(&timing.hydrate)
		messages, err = s.store.ListLatestThreadMessagesForUser(r.Context(), cu.User.ID, fetchLimit, offset)
		hydrateDone()
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	timing.seeds = len(messages)
	own := s.ownAddresses(r.Context(), cu.User)
	renderDone := timing.measure(&timing.render)
	conversations, err := s.conversationViews(r.Context(), cu.User.ID, messages, own)
	renderDone()
	if err != nil {
		s.serverError(w, err)
		return
	}
	hasNext := len(conversations) > pageSize
	if hasNext {
		conversations = conversations[:pageSize]
	}
	writeMailTimingHeaders(w, timing, page)
	etag, ok := writeJSONCachedWithETag(w, r, map[string]any{
		"conversations":  apiConversations(conversations),
		"page":           page,
		"has_prev":       page > 1,
		"has_next":       hasNext,
		"active_mailbox": activeMailbox,
	})
	if ok {
		s.rememberMailListETag(cacheKey, etag, generation)
	}
}

// apiSearch combines URL query parsing, optional mailbox filtering, sender-history
// boosts, Bleve search hits, and SQLite conversation hydration into the search
// result payload consumed by SearchView.
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
	timing := newSearchTiming()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	filterDone := timing.measure(&timing.filter)
	searchQuery, mailboxFilter, err := s.searchMailboxFilter(r.Context(), cu.User.ID, q)
	filterDone()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if searchQuery != "" && strings.Contains(strings.ToLower(searchQuery), "lang:") && !s.pluginEnabled(r.Context(), plugins.LanguageSearch) {
		writeAPIError(w, http.StatusBadRequest, "Language search is disabled.")
		return
	}
	if strings.TrimSpace(searchQuery) != "" {
		if _, err := s.ensureRecentSearchDocuments(r.Context(), cu.User.ID); err != nil {
			s.serverError(w, err)
			return
		}
	}
	page := pageFromRequest(r)
	cacheKey := mailListCacheKey{UserID: cu.User.ID, Page: page, Search: true, Query: q}
	if s.writeSearchNotModifiedIfFresh(w, r, cacheKey) {
		return
	}
	generation := s.mailListGeneration(cu.User.ID)
	offset := (page - 1) * pageSize
	own := s.ownAddresses(r.Context(), cu.User)
	var seeds []conversationSeed
	if searchQuery == "" && !mailboxFilter.enabled {
		var messages []store.MessageRecord
		hydrateDone := timing.measure(&timing.hydrate)
		messages, err = s.store.ListLatestThreadMessagesForUser(r.Context(), cu.User.ID, pageSize*3+1, offset)
		hydrateDone()
		seeds = conversationSeedsFromMessages(messages)
	} else {
		boostDone := timing.measure(&timing.sender)
		opts := s.searchOptionsWithRankingBoosts(r.Context(), cu.User)
		boostDone()
		seeds, err = s.searchConversationSeedHits(r.Context(), cu.User.ID, searchQuery, page, pageSize, opts, own, mailboxFilter, timing)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	timing.seeds = len(seeds)
	renderDone := timing.measure(&timing.render)
	conversations, err := s.conversationViewsWithSearchDetails(r.Context(), cu.User.ID, seeds, own, searchQuery)
	renderDone()
	if err != nil {
		s.serverError(w, err)
		return
	}
	hasNext := len(conversations) > pageSize
	if hasNext {
		conversations = conversations[:pageSize]
	}
	writeSearchTimingHeaders(w, timing, page)
	etag, ok := writeJSONCachedWithETag(w, r, map[string]any{
		"conversations": apiConversations(conversations),
		"page":          page,
		"has_prev":      page > 1,
		"has_next":      hasNext,
		"query":         q,
	})
	if ok {
		s.rememberMailListETag(cacheKey, etag, generation)
	}
}
