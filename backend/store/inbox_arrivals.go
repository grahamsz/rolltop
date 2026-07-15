// File overview: Transactional Inbox-arrival classification, delayed delivery finalization, and recovery scheduling.

package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const (
	// The finalizer may spend up to five seconds proving an external move.
	// Holding for ten seconds keeps end-to-end notification latency within the
	// fifteen-second correlation ceiling.
	inboxArrivalHoldDuration  = 10 * time.Second
	maxDueInboxArrivalBatch   = 100
	finalizedArrivalRetention = 7 * 24 * time.Hour
)

// ArrivalClassification explains why an Inbox UID did or did not become a notification.
type ArrivalClassification string

const (
	ArrivalPending      ArrivalClassification = "pending"
	ArrivalDelivery     ArrivalClassification = "delivery"
	ArrivalLocalMove    ArrivalClassification = "local_move"
	ArrivalLocalCopy    ArrivalClassification = "local_copy"
	ArrivalExternalMove ArrivalClassification = "external_move"
)

// PendingInboxArrival is the durable, content-free record used by recovery and scheduling.
type PendingInboxArrival struct {
	ID             int64
	UserID         int64
	AccountID      int64
	MailboxID      int64
	MessageID      int64
	SyncRunID      int64
	Classification ArrivalClassification
	Fingerprint    ArrivalFingerprint
	AvailableAt    time.Time
	FinalizedAt    time.Time
}

// InboxArrivalDecision is returned when an arrival is first held or immediately suppressed.
type InboxArrivalDecision struct {
	Arrival      PendingInboxArrival
	Event        NewMailEvent
	EventCreated bool
}

// PendingInboxArrivalSchedule is the earliest pending deadline for one tenant/account.
type PendingInboxArrivalSchedule struct {
	UserID    int64
	AccountID int64
	DueAt     time.Time
}

// FingerprintMatchStrength describes the non-content correlation a potential source matched on.
type FingerprintMatchStrength string

const (
	FingerprintMatchExact     FingerprintMatchStrength = "exact"
	FingerprintMatchCanonical FingerprintMatchStrength = "canonical"
	FingerprintMatchMessageID FingerprintMatchStrength = "message_id"
)

// PotentialMoveSource is a bounded UID-existence candidate for an unmatched Inbox arrival.
type PotentialMoveSource struct {
	Message           MessageRecord
	SourceUIDValidity int64
	MatchStrength     FingerprintMatchStrength
}

type inboxArrivalMessage struct {
	Message     MessageRecord
	Fingerprint ArrivalFingerprint
	UIDValidity int64
}

// HoldOrClassifyInboxArrival consumes a completed local transfer or expunged
// source immediately. Otherwise it durably holds the arrival for 10 seconds.
func (s *Store) HoldOrClassifyInboxArrival(ctx context.Context, userID, syncRunID int64, msg MessageRecord, fingerprint ArrivalFingerprint, now time.Time) (InboxArrivalDecision, error) {
	if userID <= 0 || msg.ID <= 0 || msg.UserID != userID || syncRunID < 0 {
		return InboxArrivalDecision{}, errors.New("invalid inbox arrival scope")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return InboxArrivalDecision{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return InboxArrivalDecision{}, err
	}
	defer tx.Rollback()
	arrivalMessage, err := loadInboxArrivalMessage(ctx, tx, userID, msg.ID, fingerprint)
	if err != nil {
		return InboxArrivalDecision{}, err
	}
	if arrivalMessage.Message.AccountID != msg.AccountID || arrivalMessage.Message.MailboxID != msg.MailboxID {
		return InboxArrivalDecision{}, errors.New("inbox arrival message mismatch")
	}
	if syncRunID > 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM sync_runs
			WHERE user_id = ? AND account_id = ? AND id = ?`, userID,
			arrivalMessage.Message.AccountID, syncRunID).Scan(&exists); err != nil {
			return InboxArrivalDecision{}, err
		}
	}
	nowUnixValue := now.UTC().Unix()
	if err := pruneArrivalCorrelationRows(ctx, tx, userID, nowUnixValue); err != nil {
		return InboxArrivalDecision{}, err
	}
	existing, err := pendingInboxArrivalForMessageTx(ctx, tx, userID, msg.ID)
	if err == nil && existing.Classification != ArrivalPending {
		decision := InboxArrivalDecision{Arrival: existing}
		if existing.Classification == ArrivalDelivery {
			decision.Event, decision.EventCreated, err = insertNewMailEventTx(ctx, tx, userID, arrivalMessage.Message, nowUnixValue)
			if err != nil {
				return InboxArrivalDecision{}, err
			}
			effectiveRunID := existing.SyncRunID
			if syncRunID > 0 {
				effectiveRunID = syncRunID
				decision.Arrival.SyncRunID = syncRunID
			}
			if decision.EventCreated && effectiveRunID > 0 {
				if err := incrementSyncRunForArrivalTx(ctx, tx, userID, arrivalMessage.Message.AccountID,
					effectiveRunID, arrivalMessage.Message, nowUnixValue); err != nil {
					return InboxArrivalDecision{}, err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE pending_inbox_arrivals
				SET sync_run_id = COALESCE(NULLIF(?, 0), sync_run_id), event_id = ?, updated_at = ?
				WHERE user_id = ? AND id = ?`, syncRunID, decision.Event.ID, nowUnixValue,
				userID, existing.ID); err != nil {
				return InboxArrivalDecision{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return InboxArrivalDecision{}, err
		}
		return decision, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return InboxArrivalDecision{}, err
	}
	classification, transferID, expungedID, err := classifyArrivalWithoutDelivery(ctx, tx, arrivalMessage, nowUnixValue)
	if err != nil {
		return InboxArrivalDecision{}, err
	}
	availableAt := nowUnixValue + int64(inboxArrivalHoldDuration/time.Second)
	finalizedAt := int64(0)
	if classification != ArrivalPending {
		availableAt = nowUnixValue
		finalizedAt = nowUnixValue
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pending_inbox_arrivals
		(user_id, account_id, mailbox_id, message_id, sync_run_id, classification,
		 raw_sha256, canonical_sha256, message_id_hash, internal_date_unix, message_size,
		 matched_transfer_id, matched_expunged_id, created_at, available_at, finalized_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, message_id) DO UPDATE SET
			sync_run_id = COALESCE(excluded.sync_run_id, pending_inbox_arrivals.sync_run_id),
			classification = CASE WHEN pending_inbox_arrivals.classification = 'pending' THEN excluded.classification ELSE pending_inbox_arrivals.classification END,
			matched_transfer_id = CASE WHEN pending_inbox_arrivals.classification = 'pending' THEN excluded.matched_transfer_id ELSE pending_inbox_arrivals.matched_transfer_id END,
			matched_expunged_id = CASE WHEN pending_inbox_arrivals.classification = 'pending' THEN excluded.matched_expunged_id ELSE pending_inbox_arrivals.matched_expunged_id END,
			finalized_at = CASE WHEN pending_inbox_arrivals.classification = 'pending' THEN excluded.finalized_at ELSE pending_inbox_arrivals.finalized_at END,
			updated_at = excluded.updated_at`,
		userID, arrivalMessage.Message.AccountID, arrivalMessage.Message.MailboxID,
		arrivalMessage.Message.ID, syncRunID, classification, arrivalMessage.Fingerprint.RawSHA256,
		arrivalMessage.Fingerprint.CanonicalSHA256, arrivalMessage.Fingerprint.MessageIDHash,
		arrivalMessage.Fingerprint.InternalDate.Unix(), arrivalMessage.Fingerprint.Size,
		transferID, expungedID, nowUnixValue, availableAt, finalizedAt, nowUnixValue)
	if err != nil {
		return InboxArrivalDecision{}, err
	}
	stored, err := pendingInboxArrivalForMessageTx(ctx, tx, userID, msg.ID)
	if err != nil {
		return InboxArrivalDecision{}, err
	}
	if err := tx.Commit(); err != nil {
		return InboxArrivalDecision{}, err
	}
	return InboxArrivalDecision{Arrival: stored}, nil
}

// FinalizeDueInboxArrivals rechecks correlation and atomically converts every
// still-unmatched due row into one delivery event and sync-run increment.
func (s *Store) FinalizeDueInboxArrivals(ctx context.Context, userID, accountID int64, now time.Time) (int, error) {
	for attempt := 0; ; attempt++ {
		created, err := s.finalizeDueInboxArrivalsOnce(ctx, userID, accountID, now)
		if err == nil || attempt >= 4 || !isSQLiteBusyError(err) {
			return created, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) finalizeDueInboxArrivalsOnce(ctx context.Context, userID, accountID int64, now time.Time) (int, error) {
	if userID <= 0 || accountID <= 0 {
		return 0, errors.New("invalid inbox arrival finalization scope")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	nowUnixValue := now.UTC().Unix()
	if err := pruneArrivalCorrelationRows(ctx, tx, userID, nowUnixValue); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, message_id FROM pending_inbox_arrivals arrival
		WHERE arrival.user_id = ? AND arrival.account_id = ?
		AND arrival.classification = 'pending' AND arrival.available_at <= ?`+
		pendingInboxArrivalRebuildEligibility+`
		ORDER BY available_at, id LIMIT ?`, userID, accountID, nowUnixValue, maxDueInboxArrivalBatch)
	if err != nil {
		return 0, err
	}
	type dueArrival struct{ id, messageID int64 }
	var due []dueArrival
	for rows.Next() {
		var item dueArrival
		if err := rows.Scan(&item.id, &item.messageID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		due = append(due, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	createdCount := 0
	for _, dueItem := range due {
		pending, err := pendingInboxArrivalForMessageTx(ctx, tx, userID, dueItem.messageID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return 0, err
		}
		message, err := loadInboxArrivalMessage(ctx, tx, userID, dueItem.messageID, pending.Fingerprint)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return 0, err
		}
		classification, transferID, expungedID, err := classifyArrivalWithoutDelivery(ctx, tx, message, nowUnixValue)
		if err != nil {
			return 0, err
		}
		if classification != ArrivalPending {
			if _, err := tx.ExecContext(ctx, `UPDATE pending_inbox_arrivals
				SET classification = ?, matched_transfer_id = ?, matched_expunged_id = ?, finalized_at = ?, updated_at = ?
				WHERE user_id = ? AND id = ? AND classification = 'pending'`, classification,
				transferID, expungedID, nowUnixValue, nowUnixValue, userID, dueItem.id); err != nil {
				return 0, err
			}
			continue
		}
		event, created, err := insertNewMailEventTx(ctx, tx, userID, message.Message, nowUnixValue)
		if err != nil {
			return 0, err
		}
		if created {
			createdCount++
			if pending.SyncRunID > 0 {
				if err := incrementSyncRunForArrivalTx(ctx, tx, userID, accountID, pending.SyncRunID,
					message.Message, nowUnixValue); err != nil {
					return 0, err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE pending_inbox_arrivals
			SET classification = 'delivery', event_id = NULLIF(?, 0), finalized_at = ?, updated_at = ?
			WHERE user_id = ? AND id = ? AND classification = 'pending'`, event.ID,
			nowUnixValue, nowUnixValue, userID, dueItem.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return createdCount, nil
}

func isSQLiteBusyError(err error) bool {
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked)
}

// ListDueInboxArrivals returns a bounded batch so the syncer can perform
// targeted UID-existence checks before finalizing deliveries.
func (s *Store) ListDueInboxArrivals(ctx context.Context, userID, accountID int64, now time.Time, limit int) ([]PendingInboxArrival, error) {
	if userID <= 0 || accountID <= 0 {
		return nil, errors.New("invalid inbox arrival scope")
	}
	if limit <= 0 || limit > maxDueInboxArrivalBatch {
		limit = maxDueInboxArrivalBatch
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, pendingInboxArrivalSelect+`
		WHERE arrival.user_id = ? AND arrival.account_id = ?
			AND arrival.classification = 'pending' AND arrival.available_at <= ?`+
		pendingInboxArrivalRebuildEligibility+`
		ORDER BY available_at, id LIMIT ?`, userID, accountID, now.UTC().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingInboxArrival
	for rows.Next() {
		item, err := scanPendingInboxArrival(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListPendingInboxArrivalSchedules supports startup recovery in combined and split stores.
func (s *Store) ListPendingInboxArrivalSchedules(ctx context.Context) ([]PendingInboxArrivalSchedule, error) {
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		var out []PendingInboxArrivalSchedule
		for _, user := range users {
			userStore, err := s.UserStore(ctx, user.ID)
			if err != nil {
				return nil, err
			}
			items, err := userStore.ListPendingInboxArrivalSchedules(ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].DueAt.Equal(out[j].DueAt) {
				if out[i].UserID == out[j].UserID {
					return out[i].AccountID < out[j].AccountID
				}
				return out[i].UserID < out[j].UserID
			}
			return out[i].DueAt.Before(out[j].DueAt)
		})
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT arrival.user_id, arrival.account_id, MIN(arrival.available_at)
		FROM pending_inbox_arrivals arrival WHERE arrival.classification = 'pending'`+
		pendingInboxArrivalRebuildEligibility+`
		GROUP BY arrival.user_id, arrival.account_id
		ORDER BY MIN(arrival.available_at), arrival.user_id, arrival.account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingInboxArrivalSchedule
	for rows.Next() {
		var item PendingInboxArrivalSchedule
		var due int64
		if err := rows.Scan(&item.UserID, &item.AccountID, &due); err != nil {
			return nil, err
		}
		item.DueAt = unixTime(due)
		out = append(out, item)
	}
	return out, rows.Err()
}

// NextPendingInboxArrivalDue returns the next deadline for one tenant/account.
// ErrNotFound means no pending arrival remains for that key.
func (s *Store) NextPendingInboxArrivalDue(ctx context.Context, userID, accountID int64) (time.Time, error) {
	if userID <= 0 || accountID <= 0 {
		return time.Time{}, errors.New("invalid inbox arrival scope")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return time.Time{}, err
	}
	var due sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT MIN(arrival.available_at) FROM pending_inbox_arrivals arrival
		WHERE arrival.user_id = ? AND arrival.account_id = ? AND arrival.classification = 'pending'`+
		pendingInboxArrivalRebuildEligibility, userID, accountID).Scan(&due)
	if err != nil {
		return time.Time{}, err
	}
	if !due.Valid || due.Int64 <= 0 {
		return time.Time{}, ErrNotFound
	}
	return unixTime(due.Int64), nil
}

// DeferPendingInboxArrivalProbes moves only uncertain due arrivals to a later
// probe deadline. Other due arrivals remain eligible for immediate delivery.
func (s *Store) DeferPendingInboxArrivalProbes(ctx context.Context, userID, accountID int64, messageIDs []int64, retryAt time.Time) error {
	if userID <= 0 || accountID <= 0 || retryAt.IsZero() {
		return errors.New("invalid inbox arrival deferral scope")
	}
	if len(messageIDs) == 0 {
		return nil
	}
	if len(messageIDs) > maxDueInboxArrivalBatch {
		return errors.New("too many inbox arrivals to defer")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `UPDATE pending_inbox_arrivals
		SET available_at = ?, updated_at = ?
		WHERE user_id = ? AND account_id = ? AND message_id = ? AND classification = 'pending'`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	retryUnix := retryAt.UTC().Unix()
	now := nowUnix()
	seen := make(map[int64]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		if messageID <= 0 {
			return errors.New("invalid inbox arrival message")
		}
		if _, duplicate := seen[messageID]; duplicate {
			continue
		}
		seen[messageID] = struct{}{}
		if _, err := stmt.ExecContext(ctx, retryUnix, now, userID, accountID, messageID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListPotentialMoveSources returns same-account, non-Inbox messages that may
// explain an unmatched Inbox arrival. The caller must verify UID existence remotely.
func (s *Store) ListPotentialMoveSources(ctx context.Context, userID, arrivalMessageID int64, limit int) ([]PotentialMoveSource, error) {
	if userID <= 0 || arrivalMessageID <= 0 {
		return nil, errors.New("invalid move source scope")
	}
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	queryLimit := limit + 1
	if queryLimit > 21 {
		queryLimit = 21
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT source.id, source.uid_validity,
		CASE
			WHEN arrival_blob.sha256 <> '' AND source_blob.sha256 = arrival_blob.sha256 COLLATE BINARY THEN 'exact'
			WHEN arrival.canonical_sha256 <> '' AND source.canonical_sha256 = arrival.canonical_sha256 COLLATE BINARY THEN 'canonical'
			ELSE 'message_id'
		END AS match_strength
		FROM messages arrival
		JOIN blobs arrival_blob ON arrival_blob.user_id = arrival.user_id AND arrival_blob.id = arrival.blob_id
		JOIN messages source ON source.user_id = arrival.user_id AND source.account_id = arrival.account_id
			AND source.mailbox_id <> arrival.mailbox_id AND source.id <> arrival.id
		JOIN blobs source_blob ON source_blob.user_id = source.user_id AND source_blob.id = source.blob_id
		JOIN mailboxes source_mailbox ON source_mailbox.user_id = source.user_id AND source_mailbox.id = source.mailbox_id
		WHERE arrival.user_id = ? AND arrival.id = ?
		AND lower(trim(COALESCE(source_mailbox.role, ''))) <> 'inbox'
		AND lower(trim(COALESCE(source_mailbox.role, ''))) <> 'all'
		AND lower(trim(source_mailbox.name)) <> 'inbox'
		AND lower(trim(source_mailbox.name)) NOT IN ('all mail', '[gmail]/all mail', 'allmail')
		AND (
			(arrival_blob.sha256 <> '' AND source_blob.sha256 = arrival_blob.sha256 COLLATE BINARY)
			OR (arrival.canonical_sha256 <> '' AND source.canonical_sha256 = arrival.canonical_sha256 COLLATE BINARY)
			OR (arrival.message_id_hash <> '' AND source.message_id_hash = arrival.message_id_hash COLLATE BINARY
				AND source.internal_date_unix = arrival.internal_date_unix AND source.size = arrival.size)
		)
		ORDER BY CASE match_strength WHEN 'exact' THEN 1 WHEN 'canonical' THEN 2 ELSE 3 END,
			source.internal_date_unix DESC, source.id DESC LIMIT ?`, userID, arrivalMessageID, queryLimit)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id          int64
		uidValidity int64
		strength    FingerprintMatchStrength
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.uidValidity, &item.strength); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		best := candidates[0].strength
		end := 1
		for end < len(candidates) && candidates[end].strength == best {
			end++
		}
		candidates = candidates[:end]
	}
	out := make([]PotentialMoveSource, 0, len(candidates))
	for _, candidate := range candidates {
		message, err := s.GetMessageForUser(ctx, userID, candidate.id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, PotentialMoveSource{Message: message, SourceUIDValidity: candidate.uidValidity, MatchStrength: candidate.strength})
	}
	return out, nil
}

const pendingInboxArrivalSelect = `SELECT arrival.id, arrival.user_id, arrival.account_id,
	arrival.mailbox_id, arrival.message_id, COALESCE(arrival.sync_run_id, 0),
	arrival.classification, arrival.raw_sha256, arrival.canonical_sha256,
	arrival.message_id_hash, arrival.internal_date_unix, arrival.message_size,
	arrival.available_at, arrival.finalized_at FROM pending_inbox_arrivals arrival `

// A rebuild normally gates restored historical arrivals. Only a live Inbox row
// proven to belong to the marker's exact current generation and durable
// post-reset UID range may become notification-eligible before the marker is
// removed.
const pendingInboxArrivalRebuildEligibility = `
	AND (
		NOT EXISTS (
			SELECT 1 FROM mailbox_generation_rebuilds rebuild
			WHERE rebuild.user_id = arrival.user_id
				AND rebuild.account_id = arrival.account_id
		)
		OR EXISTS (
			SELECT 1
			FROM mailbox_generation_rebuilds rebuild
			JOIN messages message ON message.user_id = arrival.user_id
				AND message.account_id = arrival.account_id
				AND message.mailbox_id = arrival.mailbox_id
				AND message.id = arrival.message_id
			JOIN mailboxes mailbox ON mailbox.user_id = arrival.user_id
				AND mailbox.account_id = arrival.account_id
				AND mailbox.id = arrival.mailbox_id
			WHERE rebuild.user_id = arrival.user_id
				AND rebuild.account_id = arrival.account_id
				AND rebuild.mailbox_id = arrival.mailbox_id
				AND rebuild.arrival_uid_floor > 0
				AND mailbox.uidvalidity = rebuild.target_uid_validity
				AND message.uid_validity = rebuild.target_uid_validity
				AND message.uid >= rebuild.arrival_uid_floor
				AND (lower(trim(COALESCE(mailbox.role, ''))) = 'inbox'
					OR lower(trim(mailbox.name)) = 'inbox')
				AND NOT EXISTS (
					SELECT 1 FROM mailbox_generation_rebuilds source_rebuild
					WHERE source_rebuild.user_id = arrival.user_id
						AND source_rebuild.account_id = arrival.account_id
						AND source_rebuild.mailbox_id <> arrival.mailbox_id
				)
		)
	)`

func pendingInboxArrivalForMessageTx(ctx context.Context, tx *sql.Tx, userID, messageID int64) (PendingInboxArrival, error) {
	return scanPendingInboxArrival(tx.QueryRowContext(ctx, pendingInboxArrivalSelect+
		`WHERE arrival.user_id = ? AND arrival.message_id = ?`, userID, messageID))
}

func scanPendingInboxArrival(row scanDest) (PendingInboxArrival, error) {
	var item PendingInboxArrival
	var internalDate, available, finalized int64
	err := row.Scan(&item.ID, &item.UserID, &item.AccountID, &item.MailboxID,
		&item.MessageID, &item.SyncRunID, &item.Classification,
		&item.Fingerprint.RawSHA256, &item.Fingerprint.CanonicalSHA256,
		&item.Fingerprint.MessageIDHash, &internalDate, &item.Fingerprint.Size,
		&available, &finalized)
	item.Fingerprint.InternalDate = unixTime(internalDate)
	item.AvailableAt = unixTime(available)
	item.FinalizedAt = unixTime(finalized)
	return item, err
}

func loadInboxArrivalMessage(ctx context.Context, tx *sql.Tx, userID, messageID int64, supplied ArrivalFingerprint) (inboxArrivalMessage, error) {
	var out inboxArrivalMessage
	var messageIDHeader, storedCanonical, storedMessageIDHash string
	var internalDate int64
	err := tx.QueryRowContext(ctx, `SELECT message.id, message.user_id, message.account_id,
		message.mailbox_id, message.message_id_header, message.thread_key, message.from_addr,
		message.subject, message.internal_date_unix, message.uid, message.size, blob.sha256,
		message.canonical_sha256, message.message_id_hash, message.uid_validity
		FROM messages message
		JOIN blobs blob ON blob.user_id = message.user_id AND blob.id = message.blob_id
		WHERE message.user_id = ? AND message.id = ?`, userID, messageID).
		Scan(&out.Message.ID, &out.Message.UserID, &out.Message.AccountID,
			&out.Message.MailboxID, &messageIDHeader, &out.Message.ThreadKey,
			&out.Message.FromAddr, &out.Message.Subject, &internalDate, &out.Message.UID,
			&out.Message.Size, &out.Fingerprint.RawSHA256, &storedCanonical,
			&storedMessageIDHash, &out.UIDValidity)
	if err != nil {
		return inboxArrivalMessage{}, err
	}
	if supplied.RawSHA256 != "" && supplied.RawSHA256 != out.Fingerprint.RawSHA256 {
		return inboxArrivalMessage{}, errors.New("inbox arrival fingerprint mismatch")
	}
	canonical := strings.TrimSpace(supplied.CanonicalSHA256)
	if storedCanonical != "" && canonical != "" && storedCanonical != canonical {
		return inboxArrivalMessage{}, errors.New("inbox arrival fingerprint mismatch")
	}
	if canonical == "" {
		canonical = storedCanonical
	}
	messageIDHash := HashedMessageID(messageIDHeader)
	if storedMessageIDHash != "" {
		messageIDHash = storedMessageIDHash
	}
	if supplied.MessageIDHash != "" && messageIDHash != supplied.MessageIDHash {
		return inboxArrivalMessage{}, errors.New("inbox arrival fingerprint mismatch")
	}
	if err := validateHexFingerprint(canonical); err != nil {
		return inboxArrivalMessage{}, err
	}
	if err := validateHexFingerprint(messageIDHash); err != nil {
		return inboxArrivalMessage{}, err
	}
	out.Message.MessageIDHeader = messageIDHeader
	out.Message.InternalDate = unixTime(internalDate)
	out.Fingerprint.CanonicalSHA256 = canonical
	out.Fingerprint.MessageIDHash = messageIDHash
	out.Fingerprint.InternalDate = unixTime(internalDate)
	out.Fingerprint.Size = out.Message.Size
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET canonical_sha256 = ?, message_id_hash = ?
		WHERE user_id = ? AND id = ?`, canonical, messageIDHash, userID, messageID); err != nil {
		return inboxArrivalMessage{}, err
	}
	return out, nil
}

func classifyArrivalWithoutDelivery(ctx context.Context, tx *sql.Tx, arrival inboxArrivalMessage, now int64) (ArrivalClassification, int64, int64, error) {
	transfer, err := matchMessageTransferTx(ctx, tx, arrival, now)
	if err != nil {
		return ArrivalPending, 0, 0, err
	}
	if transfer.id > 0 {
		classification := ArrivalLocalMove
		if transfer.kind == "copy" {
			classification = ArrivalLocalCopy
		}
		var expungedID int64
		if transfer.kind == "move" {
			// Reconciliation can race with a local MOVE and record the same
			// source as expunged. Consume only evidence tied to this transfer's
			// exact source; a same-content COPY must not steal another move's
			// tombstone.
			expungedID, err = consumeTransferSourceExpungeTx(ctx, tx, arrival, transfer, now)
			if err != nil {
				return ArrivalPending, 0, 0, err
			}
		}
		return classification, transfer.id, expungedID, nil
	}
	expungedID, err := matchExpungedFingerprintTx(ctx, tx, arrival, now)
	if err != nil {
		return ArrivalPending, 0, 0, err
	}
	if expungedID > 0 {
		transferID, err := consumePendingMoveForExpungeTx(ctx, tx, arrival, expungedID, now)
		if err != nil {
			return ArrivalPending, 0, 0, err
		}
		if transferID > 0 {
			return ArrivalLocalMove, transferID, expungedID, nil
		}
		return ArrivalExternalMove, 0, expungedID, nil
	}
	return ArrivalPending, 0, 0, nil
}

func consumePendingMoveForExpungeTx(ctx context.Context, tx *sql.Tx, arrival inboxArrivalMessage, expungedID, now int64) (int64, error) {
	var transferID int64
	err := tx.QueryRowContext(ctx, `SELECT transfer.id
		FROM message_transfers transfer
		JOIN expunged_message_fingerprints fingerprint
			ON fingerprint.user_id = transfer.user_id AND fingerprint.id = ?
			AND fingerprint.account_id = transfer.source_account_id
			AND fingerprint.source_mailbox_id = transfer.source_mailbox_id
			AND fingerprint.source_uid = transfer.source_uid
			AND fingerprint.source_uid_validity = transfer.source_uid_validity
		WHERE transfer.user_id = ? AND transfer.destination_account_id = ?
			AND transfer.destination_mailbox_id = ? AND transfer.operation_kind = 'move'
			AND transfer.state = 'pending' AND transfer.dispatched_at > 0
			AND ((transfer.raw_sha256 <> '' AND transfer.raw_sha256 = ? COLLATE BINARY)
				OR (transfer.canonical_sha256 <> '' AND transfer.canonical_sha256 = ? COLLATE BINARY)
				OR (transfer.message_id_hash <> '' AND transfer.message_id_hash = ? COLLATE BINARY
					AND transfer.internal_date_unix = ? AND transfer.message_size = ?))
		ORDER BY transfer.id LIMIT 1`, expungedID, arrival.Message.UserID,
		arrival.Message.AccountID, arrival.Message.MailboxID,
		arrival.Fingerprint.RawSHA256, arrival.Fingerprint.CanonicalSHA256,
		arrival.Fingerprint.MessageIDHash, arrival.Fingerprint.InternalDate.Unix(),
		arrival.Fingerprint.Size).Scan(&transferID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE message_transfers
		SET state = 'consumed', consumed_message_id = ?, consumed_at = ?, updated_at = ?
		WHERE user_id = ? AND id = ? AND state = 'pending' AND dispatched_at > 0`, arrival.Message.ID,
		now, now, arrival.Message.UserID, transferID)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rowsAffected != 1 {
		return 0, nil
	}
	return transferID, nil
}

type matchedMessageTransfer struct {
	id                int64
	kind              string
	sourceMailboxID   int64
	sourceUID         uint32
	sourceUIDValidity int64
}

func matchMessageTransferTx(ctx context.Context, tx *sql.Tx, arrival inboxArrivalMessage, now int64) (matchedMessageTransfer, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, operation_kind,
		COALESCE(source_mailbox_id, 0), source_uid, source_uid_validity,
		CASE
			WHEN destination_uid > 0 AND destination_uid = ? AND destination_uid_validity > 0
				AND ? > 0 AND destination_uid_validity = ? THEN 0
			WHEN (destination_uid = 0 OR destination_uid_validity = 0)
				AND raw_sha256 <> '' AND raw_sha256 = ? COLLATE BINARY THEN 1
			WHEN (destination_uid = 0 OR destination_uid_validity = 0)
				AND canonical_sha256 <> '' AND canonical_sha256 = ? COLLATE BINARY THEN 2
			ELSE 3
		END AS strength
		FROM message_transfers
		WHERE user_id = ? AND destination_account_id = ? AND destination_mailbox_id = ?
		AND state = 'succeeded'
		AND (
			(destination_uid > 0 AND destination_uid = ? AND destination_uid_validity > 0
				AND ? > 0 AND destination_uid_validity = ?)
			OR ((destination_uid = 0 OR destination_uid_validity = 0) AND (
				(raw_sha256 <> '' AND raw_sha256 = ? COLLATE BINARY)
				OR (canonical_sha256 <> '' AND canonical_sha256 = ? COLLATE BINARY)
				OR (message_id_hash <> '' AND message_id_hash = ? COLLATE BINARY
					AND internal_date_unix = ? AND message_size = ?)))
		)
		ORDER BY strength, id LIMIT 2`,
		arrival.Message.UID, arrival.UIDValidity, arrival.UIDValidity,
		arrival.Fingerprint.RawSHA256, arrival.Fingerprint.CanonicalSHA256,
		arrival.Message.UserID, arrival.Message.AccountID, arrival.Message.MailboxID,
		arrival.Message.UID, arrival.UIDValidity, arrival.UIDValidity,
		arrival.Fingerprint.RawSHA256, arrival.Fingerprint.CanonicalSHA256,
		arrival.Fingerprint.MessageIDHash, arrival.Fingerprint.InternalDate.Unix(), arrival.Fingerprint.Size)
	if err != nil {
		return matchedMessageTransfer{}, err
	}
	type candidate struct {
		matchedMessageTransfer
		strength int
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.kind, &item.sourceMailboxID, &item.sourceUID,
			&item.sourceUIDValidity, &item.strength); err != nil {
			_ = rows.Close()
			return matchedMessageTransfer{}, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return matchedMessageTransfer{}, err
	}
	if err := rows.Err(); err != nil {
		return matchedMessageTransfer{}, err
	}
	if len(candidates) == 0 || (candidates[0].strength == 3 && len(candidates) > 1 && candidates[1].strength == 3) {
		return matchedMessageTransfer{}, nil
	}
	chosen := candidates[0]
	result, err := tx.ExecContext(ctx, `UPDATE message_transfers
		SET state = 'consumed', consumed_message_id = ?, consumed_at = ?, updated_at = ?
		WHERE user_id = ? AND id = ? AND state = 'succeeded'`, arrival.Message.ID,
		now, now, arrival.Message.UserID, chosen.id)
	if err != nil {
		return matchedMessageTransfer{}, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return matchedMessageTransfer{}, err
	}
	if rowsAffected != 1 {
		return matchedMessageTransfer{}, nil
	}
	return chosen.matchedMessageTransfer, nil
}

func consumeTransferSourceExpungeTx(ctx context.Context, tx *sql.Tx, arrival inboxArrivalMessage, transfer matchedMessageTransfer, now int64) (int64, error) {
	if transfer.sourceMailboxID <= 0 || transfer.sourceUID == 0 || transfer.sourceUIDValidity <= 0 {
		return 0, nil
	}
	matchClause := `user_id = ? AND account_id = ? AND source_mailbox_id = ?
		AND source_uid = ? AND source_uid_validity = ? AND consumed_at = 0 AND expires_at > ?
		AND ((raw_sha256 <> '' AND raw_sha256 = ? COLLATE BINARY)
			OR (canonical_sha256 <> '' AND canonical_sha256 = ? COLLATE BINARY)
			OR (message_id_hash <> '' AND message_id_hash = ? COLLATE BINARY
				AND internal_date_unix = ? AND message_size = ?))`
	args := []any{arrival.Message.UserID, arrival.Message.AccountID, transfer.sourceMailboxID,
		transfer.sourceUID, transfer.sourceUIDValidity, now, arrival.Fingerprint.RawSHA256,
		arrival.Fingerprint.CanonicalSHA256, arrival.Fingerprint.MessageIDHash,
		arrival.Fingerprint.InternalDate.Unix(), arrival.Fingerprint.Size}
	var firstID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM expunged_message_fingerprints WHERE `+
		matchClause+` ORDER BY id LIMIT 1`, args...).Scan(&firstID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE expunged_message_fingerprints
		SET consumed_message_id = ?, consumed_at = ? WHERE `+matchClause,
		append([]any{arrival.Message.ID, now}, args...)...)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rowsAffected == 0 {
		return 0, nil
	}
	return firstID, nil
}

func matchExpungedFingerprintTx(ctx context.Context, tx *sql.Tx, arrival inboxArrivalMessage, now int64) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT fingerprint.id,
		CASE
			WHEN raw_sha256 <> '' AND raw_sha256 = ? COLLATE BINARY THEN 1
			WHEN canonical_sha256 <> '' AND canonical_sha256 = ? COLLATE BINARY THEN 2
			ELSE 3
		END AS strength
		FROM expunged_message_fingerprints fingerprint
		JOIN mailboxes source_mailbox ON source_mailbox.user_id = fingerprint.user_id
			AND source_mailbox.id = fingerprint.source_mailbox_id
		WHERE fingerprint.user_id = ? AND fingerprint.account_id = ? AND fingerprint.source_mailbox_id <> ?
		AND lower(trim(COALESCE(source_mailbox.role, ''))) <> 'all'
		AND lower(trim(source_mailbox.name)) NOT IN ('all mail', '[gmail]/all mail', 'allmail')
		AND fingerprint.consumed_at = 0
		AND (fingerprint.expires_at > ? OR EXISTS (
			SELECT 1 FROM message_transfers transfer
			WHERE transfer.user_id = fingerprint.user_id
				AND transfer.source_account_id = fingerprint.account_id
				AND transfer.source_mailbox_id = fingerprint.source_mailbox_id
				AND transfer.source_uid = fingerprint.source_uid
				AND transfer.source_uid_validity = fingerprint.source_uid_validity
				AND transfer.destination_account_id = ? AND transfer.destination_mailbox_id = ?
				AND transfer.operation_kind = 'move' AND transfer.state = 'pending'
				AND transfer.dispatched_at > 0
				AND ((transfer.raw_sha256 <> '' AND transfer.raw_sha256 = fingerprint.raw_sha256 COLLATE BINARY)
					OR (transfer.canonical_sha256 <> '' AND transfer.canonical_sha256 = fingerprint.canonical_sha256 COLLATE BINARY)
					OR (transfer.message_id_hash <> '' AND transfer.message_id_hash = fingerprint.message_id_hash COLLATE BINARY
						AND transfer.internal_date_unix = fingerprint.internal_date_unix
						AND transfer.message_size = fingerprint.message_size))
		) OR EXISTS (
			SELECT 1 FROM pending_inbox_arrivals pending
			WHERE pending.user_id = fingerprint.user_id
				AND pending.account_id = fingerprint.account_id
				AND pending.mailbox_id <> fingerprint.source_mailbox_id
				AND pending.message_id = ? AND pending.classification = 'pending'
				AND ABS(pending.created_at - fingerprint.created_at) <= ?
				AND ((pending.raw_sha256 <> '' AND pending.raw_sha256 = fingerprint.raw_sha256 COLLATE BINARY)
					OR (pending.canonical_sha256 <> '' AND pending.canonical_sha256 = fingerprint.canonical_sha256 COLLATE BINARY)
					OR (pending.message_id_hash <> '' AND pending.message_id_hash = fingerprint.message_id_hash COLLATE BINARY
						AND pending.internal_date_unix = fingerprint.internal_date_unix
						AND pending.message_size = fingerprint.message_size))
		))
		AND (
			(raw_sha256 <> '' AND raw_sha256 = ? COLLATE BINARY)
			OR (canonical_sha256 <> '' AND canonical_sha256 = ? COLLATE BINARY)
			OR (message_id_hash <> '' AND message_id_hash = ? COLLATE BINARY
				AND internal_date_unix = ? AND message_size = ?)
		)
		ORDER BY strength, fingerprint.id LIMIT 2`, arrival.Fingerprint.RawSHA256,
		arrival.Fingerprint.CanonicalSHA256, arrival.Message.UserID, arrival.Message.AccountID,
		arrival.Message.MailboxID, now, arrival.Message.AccountID, arrival.Message.MailboxID,
		arrival.Message.ID, int64(expungedFingerprintTTL/time.Second),
		arrival.Fingerprint.RawSHA256,
		arrival.Fingerprint.CanonicalSHA256, arrival.Fingerprint.MessageIDHash,
		arrival.Fingerprint.InternalDate.Unix(), arrival.Fingerprint.Size)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id       int64
		strength int
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.strength); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(candidates) == 0 || (len(candidates) > 1 && candidates[0].strength == candidates[1].strength) {
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE expunged_message_fingerprints
		SET consumed_message_id = ?, consumed_at = ?
		WHERE user_id = ? AND id = ? AND consumed_at = 0`, arrival.Message.ID,
		now, arrival.Message.UserID, candidates[0].id)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rowsAffected != 1 {
		return 0, nil
	}
	return candidates[0].id, nil
}

func insertNewMailEventTx(ctx context.Context, tx *sql.Tx, userID int64, msg MessageRecord, now int64) (NewMailEvent, bool, error) {
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
	event, err := newMailEventForMessageTx(ctx, tx, userID, msg.ID)
	return event, created, err
}

func newMailEventForMessageTx(ctx context.Context, tx *sql.Tx, userID, messageID int64) (NewMailEvent, error) {
	var event NewMailEvent
	err := tx.QueryRowContext(ctx, `SELECT id, user_id, message_id, from_addr, subject
		FROM new_mail_events WHERE user_id = ? AND message_id = ?`, userID, messageID).
		Scan(&event.ID, &event.UserID, &event.MessageID, &event.FromAddr, &event.Subject)
	return event, err
}

func incrementSyncRunForArrivalTx(ctx context.Context, tx *sql.Tx, userID, accountID, syncRunID int64, message MessageRecord, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE sync_runs
		SET new_messages = new_messages + 1, latest_new_from = ?, latest_new_subject = ?,
			latest_new_message_id = ?, updated_at = MAX(updated_at, ?)
		WHERE user_id = ? AND account_id = ? AND id = ?`, message.FromAddr,
		message.Subject, message.ID, now, userID, accountID, syncRunID)
	return err
}

func pruneArrivalCorrelationRows(ctx context.Context, tx *sql.Tx, userID, now int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_transfers
		WHERE user_id = ? AND state IN ('failed', 'consumed') AND expires_at <= ?`, userID, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM expunged_message_fingerprints
		WHERE user_id = ? AND expires_at <= ? AND NOT EXISTS (
			SELECT 1 FROM message_transfers transfer
			WHERE transfer.user_id = expunged_message_fingerprints.user_id
				AND transfer.source_account_id = expunged_message_fingerprints.account_id
				AND transfer.source_mailbox_id = expunged_message_fingerprints.source_mailbox_id
				AND transfer.source_uid = expunged_message_fingerprints.source_uid
				AND transfer.source_uid_validity = expunged_message_fingerprints.source_uid_validity
				AND transfer.operation_kind = 'move' AND transfer.state = 'pending'
				AND transfer.dispatched_at > 0
				AND ((transfer.raw_sha256 <> '' AND transfer.raw_sha256 = expunged_message_fingerprints.raw_sha256 COLLATE BINARY)
					OR (transfer.canonical_sha256 <> '' AND transfer.canonical_sha256 = expunged_message_fingerprints.canonical_sha256 COLLATE BINARY)
					OR (transfer.message_id_hash <> '' AND transfer.message_id_hash = expunged_message_fingerprints.message_id_hash COLLATE BINARY
						AND transfer.internal_date_unix = expunged_message_fingerprints.internal_date_unix
						AND transfer.message_size = expunged_message_fingerprints.message_size))
		) AND NOT EXISTS (
			SELECT 1 FROM pending_inbox_arrivals pending
			WHERE pending.user_id = expunged_message_fingerprints.user_id
				AND pending.account_id = expunged_message_fingerprints.account_id
				AND pending.mailbox_id <> expunged_message_fingerprints.source_mailbox_id
				AND pending.classification = 'pending'
				AND ABS(pending.created_at - expunged_message_fingerprints.created_at) <= ?
				AND ((pending.raw_sha256 <> '' AND pending.raw_sha256 = expunged_message_fingerprints.raw_sha256 COLLATE BINARY)
					OR (pending.canonical_sha256 <> '' AND pending.canonical_sha256 = expunged_message_fingerprints.canonical_sha256 COLLATE BINARY)
					OR (pending.message_id_hash <> '' AND pending.message_id_hash = expunged_message_fingerprints.message_id_hash COLLATE BINARY
						AND pending.internal_date_unix = expunged_message_fingerprints.internal_date_unix
						AND pending.message_size = expunged_message_fingerprints.message_size))
		)`, userID, now, int64(expungedFingerprintTTL/time.Second))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM pending_inbox_arrivals
		WHERE user_id = ? AND finalized_at > 0 AND finalized_at <= ?`, userID,
		now-int64(finalizedArrivalRetention/time.Second))
	return err
}
