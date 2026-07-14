package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"rolltop/backend/store"
)

const classificationColumns = `c.message_id, c.model_version, c.base_probability,
	c.labeled_neighbor_probability, c.labeled_neighbor_count, c.recent_read_support,
	c.final_probability, c.risk_band, c.content_coverage, c.explanation_json,
	c.classified_at, c.updated_at, COALESCE(f.label, '')`

func saveClassification(ctx context.Context, db *sql.DB, userID int64, record classificationRecord) error {
	if db == nil || userID <= 0 || record.MessageID <= 0 {
		return errors.New("classification requires a user and message")
	}
	if err := ensureMessageOwned(ctx, db, userID, record.MessageID); err != nil {
		return err
	}
	record.BaseProbability = clampProbability(record.BaseProbability)
	record.LabeledNeighborProbability = clampProbability(record.LabeledNeighborProbability)
	record.RecentReadSupport = clampProbability(record.RecentReadSupport)
	record.FinalProbability = clampProbability(record.FinalProbability)
	record.RiskBand = riskBand(record.FinalProbability)
	if strings.TrimSpace(record.ContentCoverage) == "" {
		record.ContentCoverage = "metadata"
	}
	now := time.Now().UTC().Unix()
	if record.ClassifiedAt <= 0 {
		record.ClassifiedAt = now
	}
	explanation, err := json.Marshal(record.Explanation)
	if err != nil {
		return fmt.Errorf("encode classification explanation: %w", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_classifications
		(user_id, message_id, model_version, base_probability, labeled_neighbor_probability,
		 labeled_neighbor_count, recent_read_support, final_probability, risk_band,
		 content_coverage, explanation_json, classified_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, message_id) DO UPDATE SET
		 model_version = excluded.model_version,
		 base_probability = excluded.base_probability,
		 labeled_neighbor_probability = excluded.labeled_neighbor_probability,
		 labeled_neighbor_count = excluded.labeled_neighbor_count,
		 recent_read_support = excluded.recent_read_support,
		 final_probability = excluded.final_probability,
		 risk_band = excluded.risk_band,
		 content_coverage = excluded.content_coverage,
		 explanation_json = excluded.explanation_json,
		 classified_at = excluded.classified_at,
		 updated_at = excluded.updated_at`,
		userID, record.MessageID, strings.TrimSpace(record.ModelVersion), record.BaseProbability,
		record.LabeledNeighborProbability, record.LabeledNeighborCount, record.RecentReadSupport,
		record.FinalProbability, record.RiskBand, record.ContentCoverage, string(explanation),
		record.ClassifiedAt, now)
	return err
}

func getClassification(ctx context.Context, db *sql.DB, userID, messageID int64) (classificationRecord, error) {
	if db == nil || userID <= 0 || messageID <= 0 {
		return classificationRecord{}, sql.ErrNoRows
	}
	row := db.QueryRowContext(ctx, `SELECT `+classificationColumns+`
		FROM plugin_experimental_spam_classifications c
		LEFT JOIN plugin_experimental_spam_feedback f
		 ON f.user_id = c.user_id AND f.message_id = c.message_id
		WHERE c.user_id = ? AND c.message_id = ?`, userID, messageID)
	return scanClassification(row)
}

type rowScanner interface {
	Scan(...any) error
}

func scanClassification(row rowScanner) (classificationRecord, error) {
	var record classificationRecord
	var explanation string
	err := row.Scan(
		&record.MessageID,
		&record.ModelVersion,
		&record.BaseProbability,
		&record.LabeledNeighborProbability,
		&record.LabeledNeighborCount,
		&record.RecentReadSupport,
		&record.FinalProbability,
		&record.RiskBand,
		&record.ContentCoverage,
		&explanation,
		&record.ClassifiedAt,
		&record.UpdatedAt,
		&record.Feedback,
	)
	if err != nil {
		return classificationRecord{}, err
	}
	if strings.TrimSpace(explanation) != "" {
		if err := json.Unmarshal([]byte(explanation), &record.Explanation); err != nil {
			return classificationRecord{}, fmt.Errorf("decode classification explanation: %w", err)
		}
	}
	record.DisplayBand = displayedBand(record)
	return record, nil
}

func listClassifications(ctx context.Context, db *sql.DB, userID int64, messageIDs []int64) (map[int64]classificationRecord, error) {
	out := make(map[int64]classificationRecord)
	if db == nil || userID <= 0 || len(messageIDs) == 0 {
		return out, nil
	}
	ids := uniquePositiveIDs(messageIDs, 500)
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `SELECT `+classificationColumns+`
		FROM plugin_experimental_spam_classifications c
		LEFT JOIN plugin_experimental_spam_feedback f
		 ON f.user_id = c.user_id AND f.message_id = c.message_id
		WHERE c.user_id = ? AND c.message_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		record, err := scanClassification(rows)
		if err != nil {
			return nil, err
		}
		out[record.MessageID] = record
	}
	return out, rows.Err()
}

func setFeedback(ctx context.Context, db *sql.DB, userID, messageID int64, label string) error {
	return setFeedbackTx(ctx, db, userID, messageID, label)
}

type spamSQLRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func setFeedbackTx(ctx context.Context, runner spamSQLRunner, userID, messageID int64, label string) error {
	label = strings.ToLower(strings.TrimSpace(label))
	if label != feedbackSpam && label != feedbackHam {
		return errors.New("feedback must be spam or ham")
	}
	if err := ensureMessageOwnedBy(ctx, runner, userID, messageID); err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	_, err := runner.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_feedback
		(user_id, message_id, label, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, message_id) DO UPDATE SET
		 label = excluded.label, updated_at = excluded.updated_at`,
		userID, messageID, label, now, now)
	return err
}

func ensureMessageOwned(ctx context.Context, db *sql.DB, userID, messageID int64) error {
	return ensureMessageOwnedBy(ctx, db, userID, messageID)
}

func ensureMessageOwnedBy(ctx context.Context, runner spamSQLRunner, userID, messageID int64) error {
	if runner == nil || userID <= 0 || messageID <= 0 {
		return store.ErrNotFound
	}
	var owned int
	err := runner.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE user_id = ? AND id = ?`, userID, messageID).Scan(&owned)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

func spamLabeledMessageIDs(ctx context.Context, db *sql.DB, userID int64, messageIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool)
	ids := uniquePositiveIDs(messageIDs, 50)
	if db == nil || userID <= 0 || len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `SELECT message_id FROM plugin_experimental_spam_feedback
		WHERE user_id = ? AND label = 'spam' AND message_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func clearFeedback(ctx context.Context, db *sql.DB, userID, messageID int64) error {
	return clearFeedbackTx(ctx, db, userID, messageID)
}

func clearFeedbackTx(ctx context.Context, runner spamSQLRunner, userID, messageID int64) error {
	_, err := runner.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_feedback
		WHERE user_id = ? AND message_id = ?`, userID, messageID)
	return err
}

func getFeedback(ctx context.Context, db *sql.DB, userID, messageID int64) (string, error) {
	return getFeedbackTx(ctx, db, userID, messageID)
}

func getFeedbackTx(ctx context.Context, runner spamSQLRunner, userID, messageID int64) (string, error) {
	var label string
	err := runner.QueryRowContext(ctx, `SELECT label FROM plugin_experimental_spam_feedback
		WHERE user_id = ? AND message_id = ?`, userID, messageID).Scan(&label)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return label, err
}

func listFeedback(ctx context.Context, db *sql.DB, userID int64, messageIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string)
	if db == nil || userID <= 0 || len(messageIDs) == 0 {
		return out, nil
	}
	ids := uniquePositiveIDs(messageIDs, 500)
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `SELECT message_id, label
		FROM plugin_experimental_spam_feedback
		WHERE user_id = ? AND message_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var messageID int64
		var label string
		if err := rows.Scan(&messageID, &label); err != nil {
			return nil, err
		}
		out[messageID] = label
	}
	return out, rows.Err()
}

func recentFeedbackIDs(ctx context.Context, db *sql.DB, userID int64, label string, limit int) ([]int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := db.QueryContext(ctx, `SELECT message_id FROM plugin_experimental_spam_feedback
		WHERE user_id = ? AND label = ? ORDER BY updated_at DESC, message_id DESC LIMIT ?`,
		userID, label, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func statusCounts(ctx context.Context, db *sql.DB, userID int64) (pluginStatus, error) {
	var status pluginStatus
	err := db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN risk_band = 'low' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN risk_band = 'medium' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN risk_band = 'high' THEN 1 ELSE 0 END), 0)
		FROM plugin_experimental_spam_classifications WHERE user_id = ?`, userID).
		Scan(&status.Classified, &status.LowRisk, &status.MediumRisk, &status.HighRisk)
	if err != nil {
		return pluginStatus{}, err
	}
	err = db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN label = 'spam' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN label = 'ham' THEN 1 ELSE 0 END), 0)
		FROM plugin_experimental_spam_feedback WHERE user_id = ?`, userID).
		Scan(&status.SpamFeedback, &status.HamFeedback)
	if err != nil {
		return pluginStatus{}, err
	}
	backfill, err := getBackfill(ctx, db, userID)
	if err != nil {
		return pluginStatus{}, err
	}
	status.Backfill = backfill
	return status, nil
}

func staleClassificationCount(ctx context.Context, db *sql.DB, userID int64, modelVersion string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_classifications
		WHERE user_id = ? AND model_version != ?`, userID, strings.TrimSpace(modelVersion)).Scan(&count)
	return count, err
}

func getBackfill(ctx context.Context, db *sql.DB, userID int64) (backfillRecord, error) {
	var record backfillRecord
	err := db.QueryRowContext(ctx, `SELECT model_version, status, requested, processed, failed,
		last_message_id, last_error, started_at, updated_at, completed_at
		FROM plugin_experimental_spam_backfills WHERE user_id = ?`, userID).
		Scan(&record.ModelVersion, &record.Status, &record.Requested, &record.Processed,
			&record.Failed, &record.LastMessageID, &record.LastError, &record.StartedAt,
			&record.UpdatedAt, &record.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return backfillRecord{Status: "idle"}, nil
	}
	return record, err
}

func startBackfillRecord(ctx context.Context, db *sql.DB, userID int64, modelVersion string, requested int) error {
	now := time.Now().UTC().Unix()
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_backfills
		(user_id, model_version, status, requested, processed, failed, last_message_id,
		 last_error, started_at, updated_at, completed_at)
		VALUES (?, ?, 'running', ?, 0, 0, 0, '', ?, ?, 0)
		ON CONFLICT(user_id) DO UPDATE SET
		 model_version = excluded.model_version,
		 status = 'running', requested = excluded.requested, processed = 0, failed = 0,
		 last_message_id = 0, last_error = '', started_at = excluded.started_at,
		 updated_at = excluded.updated_at, completed_at = 0`,
		userID, modelVersion, requested, now, now)
	return err
}

func updateBackfillRecord(ctx context.Context, db *sql.DB, userID int64, processed, failed int, lastMessageID int64, lastError string) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_experimental_spam_backfills SET
		processed = ?, failed = ?, last_message_id = ?, last_error = ?, updated_at = ?
		WHERE user_id = ?`, processed, failed, lastMessageID, safeError(lastError), time.Now().UTC().Unix(), userID)
	return err
}

func setBackfillRequested(ctx context.Context, db *sql.DB, userID int64, requested int) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_experimental_spam_backfills
		SET requested = ?, updated_at = ? WHERE user_id = ?`, requested, time.Now().UTC().Unix(), userID)
	return err
}

func finishBackfillRecord(ctx context.Context, db *sql.DB, userID int64, status, lastError string) error {
	if status != "complete" && status != "failed" && status != "cancelled" {
		status = "failed"
	}
	now := time.Now().UTC().Unix()
	_, err := db.ExecContext(ctx, `UPDATE plugin_experimental_spam_backfills SET
		status = ?, last_error = ?, updated_at = ?, completed_at = ? WHERE user_id = ?`,
		status, safeError(lastError), now, now, userID)
	return err
}

func uniquePositiveIDs(ids []int64, limit int) []int64 {
	if limit <= 0 {
		limit = len(ids)
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func safeError(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 32 {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if len(value) > 300 {
		value = value[:300]
	}
	return value
}
