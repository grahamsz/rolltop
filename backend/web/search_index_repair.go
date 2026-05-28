// File overview: Lightweight search-index self-healing for request paths that
// discover SQLite messages missing from the user-scoped Bleve index.

package web

import (
	"context"

	"rolltop/backend/search"
	"rolltop/backend/store"
)

const recentSearchRepairLimit = 250

// ensureSearchDocuments indexes local message records that are missing from
// Bleve. This deliberately uses only SQLite-held headers/body preview and does
// not fetch raw IMAP bodies or save attachment blobs; the heavier attachment
// repair path can still reindex full raw .eml content later.
func (s *Server) ensureSearchDocuments(ctx context.Context, userID int64, messages []store.MessageRecord) (int, error) {
	if s == nil || s.search == nil || len(messages) == 0 {
		return 0, nil
	}
	ids := make([]int64, 0, len(messages))
	byID := make(map[int64]store.MessageRecord, len(messages))
	mailboxSearchVisible := map[int64]bool{}
	for _, msg := range messages {
		if msg.UserID != userID || msg.ID == 0 {
			continue
		}
		visible, ok := mailboxSearchVisible[msg.MailboxID]
		if !ok {
			mailbox, err := s.store.GetMailboxForUser(ctx, userID, msg.MailboxID)
			if err != nil {
				return 0, err
			}
			visible = mailbox.IncludeInSearch
			mailboxSearchVisible[msg.MailboxID] = visible
		}
		if !visible {
			continue
		}
		if _, ok := byID[msg.ID]; ok {
			continue
		}
		byID[msg.ID] = msg
		ids = append(ids, msg.ID)
	}
	indexed, err := s.search.MessageIDsIndexed(ctx, userID, ids)
	if err != nil {
		return 0, err
	}
	docs := make([]search.MessageIndexDocument, 0, len(ids))
	for _, id := range ids {
		if indexed[id] {
			continue
		}
		docs = append(docs, search.MessageIndexDocument{Message: byID[id]})
	}
	if len(docs) == 0 {
		return 0, nil
	}
	if err := s.search.IndexMessages(ctx, docs); err != nil {
		return 0, err
	}
	s.noteMailListChanged(userID)
	return len(docs), nil
}

// ensureRecentSearchDocuments performs a bounded repair of the newest local
// search-visible messages before a search request can reuse a stale missing-doc
// result set. It is intentionally small so normal searches do not turn into a
// full mailbox rebuild.
func (s *Server) ensureRecentSearchDocuments(ctx context.Context, userID int64) (int, error) {
	if s == nil || s.search == nil {
		return 0, nil
	}
	messages, err := s.store.ListRecentSearchEnabledMessagesForUser(ctx, userID, recentSearchRepairLimit)
	if err != nil {
		return 0, err
	}
	return s.ensureSearchDocuments(ctx, userID, messages)
}
