// File overview: Read/star flag persistence and IMAP sync-pending tracking.

package store

import "context"

// UpdateMessageReadByUID updates local read state for a UID discovered during IMAP flag reconciliation.
func (s *Store) UpdateMessageReadByUID(ctx context.Context, userID, accountID, mailboxID int64, uid uint32, isRead bool, pending bool) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET is_read = ?, read_sync_pending = ?, updated_at = ?
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ?`,
		boolInt(isRead), boolInt(pending), nowUnix(), userID, accountID, mailboxID, uid)
	return err
}

// MarkMessageReadForUser changes local read state and optionally marks it for IMAP push.
func (s *Store) MarkMessageReadForUser(ctx context.Context, userID, messageID int64, isRead bool, pending bool) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET is_read = ?, read_sync_pending = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(isRead), boolInt(pending), nowUnix(), userID, messageID)
	return err
}

// ClearReadSyncPending clears the pending read-state push flag after IMAP accepts it.
func (s *Store) ClearReadSyncPending(ctx context.Context, userID, messageID int64) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET read_sync_pending = 0, updated_at = ? WHERE user_id = ? AND id = ?`,
		nowUnix(), userID, messageID)
	return err
}

// MarkMessageStarredForUser changes local star state and optionally marks it for IMAP push.
func (s *Store) MarkMessageStarredForUser(ctx context.Context, userID, messageID int64, isStarred bool, pending bool) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET is_starred = ?, star_sync_pending = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(isStarred), boolInt(pending), nowUnix(), userID, messageID)
	return err
}

// ClearStarSyncPending clears the pending star-state push flag after IMAP accepts it.
func (s *Store) ClearStarSyncPending(ctx context.Context, userID, messageID int64) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET star_sync_pending = 0, updated_at = ? WHERE user_id = ? AND id = ?`,
		nowUnix(), userID, messageID)
	return err
}

// UpdateMailboxStarFlags reconciles local star state from the remote set of flagged UIDs.
func (s *Store) UpdateMailboxStarFlags(ctx context.Context, userID, accountID, mailboxID int64, flaggedUIDs []uint32) ([]int64, error) {
	flagged := make(map[uint32]bool, len(flaggedUIDs))
	for _, uid := range flaggedUIDs {
		flagged[uid] = true
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, uid, is_starred FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND star_sync_pending = 0`,
		userID, accountID, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var updates []struct {
		ID        int64
		UID       uint32
		IsStarred bool
	}
	for rows.Next() {
		var id int64
		var uid uint32
		var current bool
		if err := rows.Scan(&id, &uid, &current); err != nil {
			return nil, err
		}
		next := flagged[uid]
		if current != next {
			updates = append(updates, struct {
				ID        int64
				UID       uint32
				IsStarred bool
			}{ID: id, UID: uid, IsStarred: next})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tx, err := s.mustDataDB(ctx, userID).BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowUnix()
	changed := make([]int64, 0, len(updates))
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET is_starred = ?, updated_at = ?
			WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ? AND star_sync_pending = 0`,
			boolInt(update.IsStarred), now, userID, accountID, mailboxID, update.UID); err != nil {
			return nil, err
		}
		changed = append(changed, update.ID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}

// UpdateMailboxReadFlags reconciles local read state from the remote set of seen UIDs.
func (s *Store) UpdateMailboxReadFlags(ctx context.Context, userID, accountID, mailboxID int64, seenUIDs []uint32) ([]int64, error) {
	seen := make(map[uint32]bool, len(seenUIDs))
	for _, uid := range seenUIDs {
		seen[uid] = true
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, uid, is_read FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND read_sync_pending = 0`,
		userID, accountID, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var updates []struct {
		ID     int64
		UID    uint32
		IsRead bool
	}
	for rows.Next() {
		var id int64
		var uid uint32
		var current bool
		if err := rows.Scan(&id, &uid, &current); err != nil {
			return nil, err
		}
		next := seen[uid]
		if current != next {
			updates = append(updates, struct {
				ID     int64
				UID    uint32
				IsRead bool
			}{ID: id, UID: uid, IsRead: next})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tx, err := s.mustDataDB(ctx, userID).BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowUnix()
	changed := make([]int64, 0, len(updates))
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET is_read = ?, updated_at = ?
			WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ? AND read_sync_pending = 0`,
			boolInt(update.IsRead), now, userID, accountID, mailboxID, update.UID); err != nil {
			return nil, err
		}
		changed = append(changed, update.ID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}
