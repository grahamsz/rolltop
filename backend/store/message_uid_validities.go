// File overview: Batched, tenant-scoped mailbox-generation metadata reads.

package store

import (
	"context"
	"fmt"
)

// MessageUIDValiditiesForUser returns the stored IMAP generation for a bounded
// set of tenant-owned messages. Missing IDs are omitted so callers can treat a
// concurrent row removal as a failed ownership/generation check.
func (s *Store) MessageUIDValiditiesForUser(ctx context.Context, userID int64, messageIDs []int64) (map[int64]int64, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("user id must be positive")
	}
	ids := make([]int64, 0, len(messageIDs))
	seen := make(map[int64]bool, len(messageIDs))
	for _, id := range messageIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	out := make(map[int64]int64, len(ids))
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		args := make([]any, 0, end-start+1)
		args = append(args, userID)
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, uid_validity FROM messages
			WHERE user_id = ? AND id IN (`+sqlPlaceholders(end-start)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, uidValidity int64
			if err := rows.Scan(&id, &uidValidity); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = uidValidity
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
