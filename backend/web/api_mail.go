// File overview: Mailbox listing, search, pagination, and message flag API handlers.

package web

import (
	"net/http"
	"strconv"
	"strings"

	"mailmirror/backend/plugins"
	"mailmirror/backend/search"
	"mailmirror/backend/store"
)

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
	writeJSONCached(w, r, map[string]any{
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
	searchQuery, mailboxFilter, err := s.searchMailboxFilter(r.Context(), cu.User.ID, q)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if searchQuery != "" && strings.Contains(strings.ToLower(searchQuery), "lang:") && !s.pluginEnabled(r.Context(), plugins.LanguageSearch) {
		writeAPIError(w, http.StatusBadRequest, "Language search is disabled.")
		return
	}
	sortMode := search.SortMode(r.URL.Query().Get("sort"))
	if sortMode != search.SortRecent {
		sortMode = search.SortBest
	}
	if searchQuery == "" && mailboxFilter.enabled {
		sortMode = search.SortRecent
	}
	page := pageFromRequest(r)
	offset := (page - 1) * pageSize
	own := s.ownAddresses(r.Context(), cu.User)
	var seeds []conversationSeed
	if searchQuery == "" && !mailboxFilter.enabled {
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
		seeds, err = s.searchConversationSeedHits(r.Context(), cu.User.ID, searchQuery, sortMode, page, pageSize, opts, own, mailboxFilter)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	conversations, err := s.conversationViewsWithSearchDetails(r.Context(), cu.User.ID, seeds, own, searchQuery)
	if err != nil {
		s.serverError(w, err)
		return
	}
	hasNext := len(conversations) > pageSize
	if hasNext {
		conversations = conversations[:pageSize]
	}
	writeJSONCached(w, r, map[string]any{
		"conversations": apiConversations(conversations),
		"page":          page,
		"has_prev":      page > 1,
		"has_next":      hasNext,
		"query":         q,
		"sort":          string(sortMode),
	})
}
