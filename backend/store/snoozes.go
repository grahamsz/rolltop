// File overview: Durable, user-scoped local snoozes and reminder event cursors.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxSnoozePageSize = 200

// SnoozeMessage locally hides a user-owned conversation until the requested time.
// It never mutates the source IMAP mailbox.
func (s *Store) SnoozeMessage(ctx context.Context, userID, messageID int64, until time.Time) (MessageSnooze, error) {
	until = until.UTC().Truncate(time.Second)
	if userID <= 0 || messageID <= 0 || until.Unix() <= nowUnix() {
		return MessageSnooze{}, errors.New("snooze time must be in the future")
	}
	msg, err := s.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return MessageSnooze{}, err
	}
	threadKey := SnoozeThreadKey(msg)
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return MessageSnooze{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return MessageSnooze{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_snoozes
		WHERE user_id = ? AND message_id = ? AND thread_key != ?`, userID, messageID, threadKey); err != nil {
		return MessageSnooze{}, err
	}
	if err := deleteSnoozeReminderEventsForThread(ctx, tx, userID, threadKey); err != nil {
		return MessageSnooze{}, err
	}
	ts := nowUnix()
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_snoozes
			(user_id, message_id, thread_key, generation, snoozed_until, reminded_at, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, 0, ?, ?)
		ON CONFLICT(user_id, thread_key) DO UPDATE SET
			message_id = excluded.message_id,
			generation = message_snoozes.generation + 1,
			snoozed_until = excluded.snoozed_until,
			reminded_at = 0,
			updated_at = excluded.updated_at`,
		userID, messageID, threadKey, until.Unix(), ts, ts); err != nil {
		return MessageSnooze{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessageSnooze{}, err
	}
	return s.messageSnoozeForThread(ctx, userID, threadKey)
}

// UnsnoozeMessage removes local snooze state for a user-owned message's conversation.
func (s *Store) UnsnoozeMessage(ctx context.Context, userID, messageID int64) (bool, error) {
	if userID <= 0 || messageID <= 0 {
		return false, ErrNotFound
	}
	msg, err := s.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return false, err
	}
	db := s.mustDataDB(ctx, userID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	threadKey := SnoozeThreadKey(msg)
	result, err := tx.ExecContext(ctx, `DELETE FROM message_snoozes
		WHERE user_id = ? AND (message_id = ? OR thread_key = ?)`, userID, messageID, SnoozeThreadKey(msg))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := deleteSnoozeReminderEventsForThread(ctx, tx, userID, threadKey); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return rows > 0, nil
}

// AcknowledgeDueSnoozeForUser clears only a resurfaced snooze. Future snoozes
// remain hidden when their message is accessed through a non-list route.
func (s *Store) AcknowledgeDueSnoozeForUser(ctx context.Context, userID, messageID int64, now time.Time) (bool, error) {
	if userID <= 0 || messageID <= 0 {
		return false, ErrNotFound
	}
	msg, err := s.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return false, err
	}
	db := s.mustDataDB(ctx, userID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	threadKey := SnoozeThreadKey(msg)
	result, err := tx.ExecContext(ctx, `DELETE FROM message_snoozes
		WHERE user_id = ? AND thread_key = ? AND snoozed_until <= ?`,
		userID, threadKey, now.UTC().Unix())
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows > 0 {
		if err := deleteSnoozeReminderEventsForThread(ctx, tx, userID, threadKey); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return rows > 0, nil
}

// CancelSnoozeForNewMessage resurfaces a conversation when a genuinely
// incremental message is stored outside the Inbox notification path.
func (s *Store) CancelSnoozeForNewMessage(ctx context.Context, userID int64, msg MessageRecord) (bool, error) {
	if userID <= 0 || msg.ID <= 0 || msg.UserID != userID {
		return false, errors.New("snooze cancellation user/message mismatch")
	}
	tx, err := s.mustDataDB(ctx, userID).BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	cancelled, err := cancelSnoozeForNewMessage(ctx, tx, userID, msg)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return cancelled, nil
}

// MessageSnoozeForUser returns local snooze state for a user-owned message.
func (s *Store) MessageSnoozeForUser(ctx context.Context, userID, messageID int64) (MessageSnooze, error) {
	msg, err := s.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return MessageSnooze{}, err
	}
	return s.messageSnoozeForThread(ctx, userID, SnoozeThreadKey(msg))
}

func (s *Store) messageSnoozeForThread(ctx context.Context, userID int64, threadKey string) (MessageSnooze, error) {
	return scanMessageSnooze(s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT
		id, user_id, message_id, thread_key, generation, snoozed_until, reminded_at, created_at, updated_at
		FROM message_snoozes WHERE user_id = ? AND thread_key = ?`, userID, threadKey))
}

// ListActiveSnoozedMessagesForUser returns future snoozes for the explicit
// Snoozed view. Due reminders are excluded because they have already resurfaced.
func (s *Store) ListActiveSnoozedMessagesForUser(ctx context.Context, userID int64, limit, offset int, now time.Time) ([]SnoozedMessage, error) {
	return s.listSnoozedMessagesForUser(ctx, userID, limit, offset, `snoozed_until > ?`, now.UTC().Unix(), "snoozed_until ASC, id ASC")
}

// ListDueSnoozedMessagesForUser returns resurfaced snoozes newest-first until
// the user opens or explicitly clears their conversation.
func (s *Store) ListDueSnoozedMessagesForUser(ctx context.Context, userID int64, limit, offset int, now time.Time) ([]SnoozedMessage, error) {
	return s.listSnoozedMessagesForUser(ctx, userID, limit, offset, `snoozed_until <= ?`, now.UTC().Unix(), "snoozed_until DESC, id DESC")
}

func (s *Store) listSnoozedMessagesForUser(ctx context.Context, userID int64, limit, offset int, timePredicate string, timestamp int64, orderBy string) ([]SnoozedMessage, error) {
	if userID <= 0 {
		return nil, errors.New("invalid user id")
	}
	if limit <= 0 || limit > maxSnoozePageSize {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT
		id, user_id, message_id, thread_key, generation, snoozed_until, reminded_at, created_at, updated_at
		FROM message_snoozes
		WHERE user_id = ? AND `+timePredicate+`
		ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, userID, timestamp, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snoozes []MessageSnooze
	for rows.Next() {
		snooze, err := scanMessageSnooze(rows)
		if err != nil {
			return nil, err
		}
		snoozes = append(snoozes, snooze)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SnoozedMessage, 0, len(snoozes))
	for _, snooze := range snoozes {
		msg, err := s.GetMessageForUser(ctx, userID, snooze.MessageID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, SnoozedMessage{Snooze: snooze, Message: msg})
	}
	return out, nil
}

// ActiveMessageSnoozesForThreads returns future snoozes keyed by their stable
// local conversation key. Callers use it to annotate conversation responses.
func (s *Store) ActiveMessageSnoozesForThreads(ctx context.Context, userID int64, threadKeys []string, now time.Time) (map[string]MessageSnooze, error) {
	return s.messageSnoozesForThreads(ctx, userID, threadKeys, `snoozed_until > ?`, now.UTC().Unix())
}

// DueMessageSnoozesForThreads returns unacknowledged resurfaced snoozes for
// ordering search results by their reminder time.
func (s *Store) DueMessageSnoozesForThreads(ctx context.Context, userID int64, threadKeys []string, now time.Time) (map[string]MessageSnooze, error) {
	return s.messageSnoozesForThreads(ctx, userID, threadKeys, `snoozed_until <= ?`, now.UTC().Unix())
}

func (s *Store) messageSnoozesForThreads(ctx context.Context, userID int64, threadKeys []string, timePredicate string, timestamp int64) (map[string]MessageSnooze, error) {
	out := map[string]MessageSnooze{}
	if userID <= 0 || len(threadKeys) == 0 {
		return out, nil
	}
	seen := map[string]bool{}
	keys := make([]string, 0, len(threadKeys))
	for _, key := range threadKeys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(keys)+2)
	args = append(args, userID, timestamp)
	for _, key := range keys {
		args = append(args, key)
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT
		id, user_id, message_id, thread_key, generation, snoozed_until, reminded_at, created_at, updated_at
		FROM message_snoozes WHERE user_id = ? AND `+timePredicate+` AND thread_key IN (`+sqlPlaceholders(len(keys))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		snooze, err := scanMessageSnooze(rows)
		if err != nil {
			return nil, err
		}
		out[snooze.ThreadKey] = snooze
	}
	return out, rows.Err()
}

// ListUnsnoozedMessagesByIDsForUser hydrates search hits while excluding every
// message in a conversation with an active future snooze.
func (s *Store) ListUnsnoozedMessagesByIDsForUser(ctx context.Context, userID int64, ids []int64, now time.Time) ([]MessageRecord, error) {
	messages, err := s.ListMessagesByIDsForUser(ctx, userID, ids)
	if err != nil || len(messages) == 0 {
		return messages, err
	}
	keys := make([]string, 0, len(messages))
	for _, msg := range messages {
		keys = append(keys, SnoozeThreadKey(msg))
	}
	active, err := s.ActiveMessageSnoozesForThreads(ctx, userID, keys, now)
	if err != nil {
		return nil, err
	}
	out := messages[:0]
	for _, msg := range messages {
		if _, hidden := active[SnoozeThreadKey(msg)]; !hidden {
			out = append(out, msg)
		}
	}
	return out, nil
}

// RecordDueSnoozeReminderEvents atomically turns due snoozes into durable feed
// events and marks their current generations processed. Re-running is idempotent.
func (s *Store) RecordDueSnoozeReminderEvents(ctx context.Context, userID int64, now time.Time, limit int) ([]SnoozeReminderEvent, error) {
	if userID <= 0 {
		return nil, errors.New("invalid user id")
	}
	if limit <= 0 || limit > maxSnoozePageSize {
		limit = 100
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	type dueSnooze struct {
		ID         int64
		MessageID  int64
		Generation int64
		Until      int64
		FromAddr   string
		Subject    string
	}
	rows, err := tx.QueryContext(ctx, `SELECT sn.id, sn.message_id, sn.generation, sn.snoozed_until, m.from_addr, m.subject
		FROM message_snoozes sn
		JOIN messages m ON m.user_id = sn.user_id AND m.id = sn.message_id
		WHERE sn.user_id = ? AND sn.reminded_at = 0 AND sn.snoozed_until <= ?
		ORDER BY sn.snoozed_until ASC, sn.id ASC LIMIT ?`, userID, now.UTC().Unix(), limit)
	if err != nil {
		return nil, err
	}
	var due []dueSnooze
	for rows.Next() {
		var item dueSnooze
		if err := rows.Scan(&item.ID, &item.MessageID, &item.Generation, &item.Until, &item.FromAddr, &item.Subject); err != nil {
			_ = rows.Close()
			return nil, err
		}
		due = append(due, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	createdAt := now.UTC().Unix()
	events := make([]SnoozeReminderEvent, 0, len(due))
	for _, item := range due {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO snooze_reminder_events
			(user_id, message_id, snooze_generation, from_addr, subject, due_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			userID, item.MessageID, item.Generation, item.FromAddr, item.Subject, item.Until, createdAt); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE message_snoozes SET reminded_at = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND generation = ? AND reminded_at = 0 AND snoozed_until <= ?`,
			createdAt, createdAt, item.ID, userID, item.Generation, now.UTC().Unix()); err != nil {
			return nil, err
		}
		event, err := scanSnoozeReminderEvent(tx.QueryRowContext(ctx, `SELECT
			id, user_id, message_id, snooze_generation, from_addr, subject, due_at, created_at
			FROM snooze_reminder_events
			WHERE user_id = ? AND message_id = ? AND snooze_generation = ?`, userID, item.MessageID, item.Generation))
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

// NextPendingSnoozeDue returns the earliest snooze generation not yet converted
// to a reminder event for this user.
func (s *Store) NextPendingSnoozeDue(ctx context.Context, userID int64) (time.Time, error) {
	if userID <= 0 {
		return time.Time{}, errors.New("invalid user id")
	}
	var unix int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT COALESCE(MIN(snoozed_until), 0)
		FROM message_snoozes WHERE user_id = ? AND reminded_at = 0`, userID).Scan(&unix)
	return unixTime(unix), err
}

// SnoozeReminderEventsAfter returns retained reminder envelopes, their count
// after a cursor, and the user's current independent reminder high-water mark.
func (s *Store) SnoozeReminderEventsAfter(ctx context.Context, userID, afterID int64, limit int) ([]SnoozeReminderEvent, int, int64, error) {
	if userID <= 0 || afterID < 0 {
		return nil, 0, 0, errors.New("invalid snooze reminder cursor")
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
		(SELECT COUNT(*) FROM snooze_reminder_events WHERE user_id = ? AND id > ?),
		(SELECT COALESCE(MAX(id), 0) FROM snooze_reminder_events WHERE user_id = ?)`,
		userID, afterID, userID).Scan(&count, &cursor); err != nil {
		return nil, 0, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, message_id, snooze_generation, from_addr, subject, due_at, created_at
		FROM snooze_reminder_events
		WHERE user_id = ? AND id > ? AND id <= ? ORDER BY id DESC LIMIT ?`, userID, afterID, cursor, limit)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	events := make([]SnoozeReminderEvent, 0, limit)
	for rows.Next() {
		event, err := scanSnoozeReminderEvent(rows)
		if err != nil {
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

// LatestSnoozeReminderEventID establishes a silent baseline for a new client.
func (s *Store) LatestSnoozeReminderEventID(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, errors.New("invalid user id")
	}
	var cursor int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0)
		FROM snooze_reminder_events WHERE user_id = ?`, userID).Scan(&cursor)
	return cursor, err
}

// SnoozeThreadKey returns the stable local conversation key used by snooze state.
func SnoozeThreadKey(msg MessageRecord) string {
	key := strings.TrimSpace(msg.ThreadKey)
	if key == "" {
		key = fmt.Sprintf("id:%d", msg.ID)
	}
	return key
}

type snoozeEventExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func deleteSnoozeReminderEventsForThread(ctx context.Context, db snoozeEventExecer, userID int64, threadKey string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM snooze_reminder_events
		WHERE user_id = ? AND message_id IN (
			SELECT id FROM messages
			WHERE user_id = ? AND COALESCE(NULLIF(thread_key, ''), 'id:' || id) = ?
		)`, userID, userID, threadKey)
	return err
}

func cancelSnoozeForNewMessage(ctx context.Context, db snoozeEventExecer, userID int64, msg MessageRecord) (bool, error) {
	threadKey := SnoozeThreadKey(msg)
	changed := false
	result, err := db.ExecContext(ctx, `DELETE FROM message_snoozes
		WHERE user_id = ? AND thread_key = ?`, userID, threadKey)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	changed = rows > 0

	result, err = db.ExecContext(ctx, `DELETE FROM snooze_reminder_events
		WHERE user_id = ? AND message_id IN (
			SELECT id FROM messages
			WHERE user_id = ? AND COALESCE(NULLIF(thread_key, ''), 'id:' || id) = ?
		)`, userID, userID, threadKey)
	if err != nil {
		return false, err
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return false, err
	}
	changed = changed || rows > 0

	result, err = db.ExecContext(ctx, `DELETE FROM mailbox_generation_rebuild_snooze_events
		WHERE user_id = ? AND rebuild_message_id IN (
			SELECT id FROM mailbox_generation_rebuild_messages
			WHERE user_id = ? AND snooze_thread_key = ?
		)`, userID, userID, threadKey)
	if err != nil {
		return false, err
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return false, err
	}
	changed = changed || rows > 0

	result, err = db.ExecContext(ctx, `UPDATE mailbox_generation_rebuild_messages
		SET has_snooze = 0, updated_at = ?
		WHERE user_id = ? AND snooze_thread_key = ? AND has_snooze <> 0`,
		nowUnix(), userID, threadKey)
	if err != nil {
		return false, err
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed || rows > 0, nil
}

func scanMessageSnooze(row scanDest) (MessageSnooze, error) {
	var snooze MessageSnooze
	var until, reminded, created, updated int64
	err := row.Scan(&snooze.ID, &snooze.UserID, &snooze.MessageID, &snooze.ThreadKey, &snooze.Generation,
		&until, &reminded, &created, &updated)
	snooze.SnoozedUntil = unixTime(until)
	snooze.RemindedAt = unixTime(reminded)
	snooze.CreatedAt = unixTime(created)
	snooze.UpdatedAt = unixTime(updated)
	return snooze, err
}

func scanSnoozeReminderEvent(row scanDest) (SnoozeReminderEvent, error) {
	var event SnoozeReminderEvent
	var due, created int64
	err := row.Scan(&event.ID, &event.UserID, &event.MessageID, &event.SnoozeGeneration,
		&event.FromAddr, &event.Subject, &due, &created)
	event.DueAt = unixTime(due)
	event.CreatedAt = unixTime(created)
	return event, err
}
