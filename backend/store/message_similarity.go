// File overview: Tenant-scoped, envelope-only candidate queries used by sparse
// message similarity. SQLite remains authoritative for ownership and read state.

package store

import (
	"context"
	"time"
)

const maxMessageSimilarityCandidates = 5000

// MessageSimilarityCandidatesForUser ownership-filters an untrusted ID list
// and returns only the lightweight envelopes needed to hydrate search hits. The
// returned order follows the first occurrence of each owned input ID.
func (s *Store) MessageSimilarityCandidatesForUser(ctx context.Context, userID int64, messageIDs []int64) ([]MessageSimilarityCandidate, error) {
	ids := uniquePositiveIDs(messageIDs, maxMessageSimilarityCandidates)
	if userID <= 0 || len(ids) == 0 {
		return nil, nil
	}
	byID := make(map[int64]MessageSimilarityCandidate, len(ids))
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, userID)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, thread_key,
			CASE WHEN internal_date_unix > 0 THEN internal_date_unix ELSE created_at END,
			from_addr, is_read
			FROM messages WHERE user_id = ? AND id IN (`+sqlPlaceholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var candidate MessageSimilarityCandidate
			var dateUnix int64
			if err := rows.Scan(&candidate.ID, &candidate.UserID, &candidate.ThreadKey, &dateUnix, &candidate.FromAddr, &candidate.IsRead); err != nil {
				rows.Close()
				return nil, err
			}
			candidate.Date = unixTime(dateUnix)
			byID[candidate.ID] = candidate
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	out := make([]MessageSimilarityCandidate, 0, len(byID))
	for _, id := range ids {
		if candidate, ok := byID[id]; ok {
			out = append(out, candidate)
		}
	}
	return out, nil
}

// RecentReadMessageSimilarityCandidatesForUser resolves currently read mail
// received on or after since. It intentionally reads SQLite's current is_read
// flag instead of Bleve's eventually updated copy. INTERNALDATE is supplied by
// the IMAP server and is therefore preferable to the sender-controlled Date
// header; locally recorded creation time is the safe fallback for legacy rows.
func (s *Store) RecentReadMessageSimilarityCandidatesForUser(ctx context.Context, userID int64, since time.Time, limit int) ([]MessageSimilarityCandidate, error) {
	if userID <= 0 {
		return nil, nil
	}
	if since.IsZero() {
		since = time.Now().UTC().Add(-90 * 24 * time.Hour)
	}
	if limit <= 0 || limit > maxMessageSimilarityCandidates {
		limit = maxMessageSimilarityCandidates
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, thread_key,
		CASE WHEN internal_date_unix > 0 THEN internal_date_unix ELSE created_at END AS received_unix,
		from_addr, is_read
		FROM messages
		WHERE user_id = ? AND is_read != 0
			AND CASE WHEN internal_date_unix > 0 THEN internal_date_unix ELSE created_at END >= ?
		ORDER BY received_unix DESC, id DESC
		LIMIT ?`, userID, since.UTC().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MessageSimilarityCandidate, 0, limit)
	for rows.Next() {
		var candidate MessageSimilarityCandidate
		var dateUnix int64
		if err := rows.Scan(&candidate.ID, &candidate.UserID, &candidate.ThreadKey, &dateUnix, &candidate.FromAddr, &candidate.IsRead); err != nil {
			return nil, err
		}
		candidate.Date = unixTime(dateUnix)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func uniquePositiveIDs(ids []int64, limit int) []int64 {
	if limit <= 0 {
		return nil
	}
	out := make([]int64, 0, min(len(ids), limit))
	seen := make(map[int64]bool, cap(out))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) == limit {
			break
		}
	}
	return out
}
