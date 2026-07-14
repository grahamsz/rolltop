// File overview: Durable per-user new-mail notification event cursors and envelopes.

package store

import (
	"context"
	"fmt"
)

// NewMailEvent is a minimal message envelope retained for notification clients.
// It intentionally excludes message bodies and credentials.
type NewMailEvent struct {
	ID        int64
	UserID    int64
	MessageID int64
	FromAddr  string
	Subject   string
}

// RecordNewMailEvent records one inbox arrival exactly once for its owning user.
func (s *Store) RecordNewMailEvent(ctx context.Context, userID int64, msg MessageRecord) (NewMailEvent, bool, error) {
	if userID <= 0 || msg.ID <= 0 || msg.UserID != userID {
		return NewMailEvent{}, false, fmt.Errorf("new mail event user/message mismatch")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return NewMailEvent{}, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return NewMailEvent{}, false, err
	}
	defer tx.Rollback()
	now := nowUnix()
	suppressed, err := consumePendingMoveNotification(ctx, tx, userID, msg.ID, now)
	if err != nil {
		return NewMailEvent{}, false, err
	}
	if suppressed {
		if err := tx.Commit(); err != nil {
			return NewMailEvent{}, false, err
		}
		return NewMailEvent{}, false, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO new_mail_events
		(user_id, message_id, from_addr, subject, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, msg.ID, msg.FromAddr, msg.Subject, now)
	if err != nil {
		return NewMailEvent{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return NewMailEvent{}, false, err
	}
	created := rows > 0
	if created {
		if _, err := cancelSnoozeForNewMessage(ctx, tx, userID, msg); err != nil {
			return NewMailEvent{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return NewMailEvent{}, false, err
	}
	event, err := s.newMailEventForMessage(ctx, userID, msg.ID)
	return event, created, err
}

func (s *Store) newMailEventForMessage(ctx context.Context, userID, messageID int64) (NewMailEvent, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return NewMailEvent{}, err
	}
	var event NewMailEvent
	err = db.QueryRowContext(ctx, `SELECT id, user_id, message_id, from_addr, subject
		FROM new_mail_events WHERE user_id = ? AND message_id = ?`, userID, messageID).
		Scan(&event.ID, &event.UserID, &event.MessageID, &event.FromAddr, &event.Subject)
	return event, err
}

// NewMailEventsAfter returns the newest retained envelopes after a client cursor,
// the full event count after that cursor, and the user's current cursor.
func (s *Store) NewMailEventsAfter(ctx context.Context, userID, afterID int64, limit int) ([]NewMailEvent, int, int64, error) {
	if userID <= 0 || afterID < 0 {
		return nil, 0, 0, fmt.Errorf("invalid new mail event cursor")
	}
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, 0, 0, err
	}
	var count int
	var cursor int64
	if err := db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM new_mail_events WHERE user_id = ? AND id > ?),
			(SELECT COALESCE(MAX(id), 0) FROM new_mail_events WHERE user_id = ?)`,
		userID, afterID, userID).Scan(&count, &cursor); err != nil {
		return nil, 0, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, message_id, from_addr, subject
		FROM new_mail_events WHERE user_id = ? AND id > ? AND id <= ? ORDER BY id DESC LIMIT ?`, userID, afterID, cursor, limit)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	events := make([]NewMailEvent, 0, limit)
	for rows.Next() {
		var event NewMailEvent
		if err := rows.Scan(&event.ID, &event.UserID, &event.MessageID, &event.FromAddr, &event.Subject); err != nil {
			return nil, 0, 0, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, count, cursor, nil
}

// LatestNewMailEventID establishes a silent notification baseline for a user.
func (s *Store) LatestNewMailEventID(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, fmt.Errorf("invalid user id")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	var cursor int64
	err = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0)
		FROM new_mail_events WHERE user_id = ?`, userID).Scan(&cursor)
	return cursor, err
}
