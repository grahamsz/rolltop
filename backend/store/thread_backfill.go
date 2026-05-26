// File overview: Maintenance routines for backfilling thread keys and headers from stored blobs.

package store

import (
	"bufio"
	"context"
	"errors"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
)

// BackfillThreadKeys computes missing thread keys for existing message rows in batches.
func (s *Store) BackfillThreadKeys(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return 0, err
		}
		total := 0
		remaining := limit
		for _, user := range users {
			if remaining <= 0 {
				break
			}
			us, err := s.UserStore(ctx, user.ID)
			if err != nil {
				return total, err
			}
			n, err := us.BackfillThreadKeys(ctx, remaining)
			if err != nil {
				return total, err
			}
			total += n
			remaining -= n
		}
		return total, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, message_id_header, in_reply_to, references_header, subject
		FROM messages WHERE thread_key = '' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct {
		id         int64
		messageID  string
		inReplyTo  string
		references string
		subject    string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.messageID, &r.inReplyTo, &r.references, &r.subject); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range pending {
		key := ThreadKey(r.messageID, r.inReplyTo, r.references, r.subject)
		if key == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE messages SET thread_key = ?, updated_at = ? WHERE id = ? AND thread_key = ''`, key, nowUnix(), r.id); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}

// BackfillThreadHeadersFromBlobs repairs thread headers by reading local raw message blobs.
func (s *Store) BackfillThreadHeadersFromBlobs(ctx context.Context, dataDir string, limit int) (int, int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return 0, 0, err
		}
		totalChecked, totalUpdated := 0, 0
		remaining := limit
		for _, user := range users {
			if remaining <= 0 {
				break
			}
			us, err := s.UserStore(ctx, user.ID)
			if err != nil {
				return totalChecked, totalUpdated, err
			}
			checked, updated, err := us.BackfillThreadHeadersFromBlobs(ctx, dataDir, remaining)
			if err != nil {
				return totalChecked, totalUpdated, err
			}
			totalChecked += checked
			totalUpdated += updated
			remaining -= checked
		}
		return totalChecked, totalUpdated, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, blob_path, message_id_header, in_reply_to, references_header, subject, thread_key
		FROM messages WHERE thread_headers_checked_at = 0 AND blob_path != '' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	type row struct {
		id         int64
		blobPath   string
		messageID  string
		inReplyTo  string
		references string
		subject    string
		threadKey  string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.blobPath, &r.messageID, &r.inReplyTo, &r.references, &r.subject, &r.threadKey); err != nil {
			return 0, 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	checked, updated := 0, 0
	for _, r := range pending {
		messageID, inReplyTo, references := strings.TrimSpace(r.messageID), strings.TrimSpace(r.inReplyTo), strings.TrimSpace(r.references)
		hMessageID, hInReplyTo, hReferences, err := readThreadHeaders(filepath.Join(dataDir, filepath.Clean(r.blobPath)))
		if err == nil {
			if hMessageID != "" {
				messageID = hMessageID
			}
			if hInReplyTo != "" {
				inReplyTo = hInReplyTo
			}
			if hReferences != "" {
				references = hReferences
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return checked, updated, err
		}

		key := ThreadKey(messageID, inReplyTo, references, r.subject)
		if key == "" {
			key = strings.TrimSpace(r.threadKey)
		}
		changed := messageID != r.messageID || inReplyTo != r.inReplyTo || references != r.references || key != r.threadKey
		if _, err := s.db.ExecContext(ctx, `UPDATE messages
			SET message_id_header = ?, in_reply_to = ?, references_header = ?, thread_key = ?, thread_headers_checked_at = ?, updated_at = ?
			WHERE id = ?`,
			messageID, inReplyTo, references, key, nowUnix(), nowUnix(), r.id); err != nil {
			return checked, updated, err
		}
		checked++
		if changed {
			updated++
		}
	}
	return checked, updated, nil
}

func readThreadHeaders(path string) (string, string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	if prefix, err := br.Peek(5); err == nil && string(prefix) == "From " {
		_, _ = br.ReadString('\n')
	}
	headers, err := textproto.NewReader(br).ReadMIMEHeader()
	if err != nil {
		return "", "", "", nil
	}
	return strings.TrimSpace(headers.Get("Message-Id")), strings.TrimSpace(headers.Get("In-Reply-To")), strings.TrimSpace(headers.Get("References")), nil
}

// ListReadSenderStatsForUser returns precomputed sender-history boosts for best-match search ranking.
func (s *Store) ListReadSenderStatsForUser(ctx context.Context, userID int64, limit int) ([]SenderReadStat, error) {
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `
		SELECT sender, read_count, total_count, boost
		FROM sender_read_stats
		WHERE user_id = ? AND read_count > 0
		ORDER BY boost DESC, read_count DESC, sender ASC
		LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := make([]SenderReadStat, 0, limit)
	for rows.Next() {
		var stat SenderReadStat
		if err := rows.Scan(&stat.Sender, &stat.ReadCount, &stat.TotalCount, &stat.Boost); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

// RefreshReadSenderStatsForUser rebuilds the materialized sender-history table
// from message rows. Sync and maintenance jobs run this outside search requests
// so best-match ranking can read a small precomputed result set.
func (s *Store) RefreshReadSenderStatsForUser(ctx context.Context, userID int64) error {
	if userID <= 0 {
		return nil
	}
	db := s.mustDataDB(ctx, userID)
	rows, err := db.QueryContext(ctx, `
		SELECT from_addr,
			SUM(CASE WHEN is_read != 0 THEN 1 ELSE 0 END) AS read_count,
			COUNT(*) AS total_count
		FROM messages
		WHERE user_id = ? AND from_addr != ''
		GROUP BY from_addr`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	statsBySender := map[string]*SenderReadStat{}
	for rows.Next() {
		var from string
		var readCount, totalCount int
		if err := rows.Scan(&from, &readCount, &totalCount); err != nil {
			return err
		}
		sender := SenderIdentity(from)
		if sender == "" || totalCount <= 0 {
			continue
		}
		stat := statsBySender[sender]
		if stat == nil {
			stat = &SenderReadStat{Sender: sender}
			statsBySender[sender] = stat
		}
		stat.TotalCount += totalCount
		stat.ReadCount += readCount
	}
	if err := rows.Err(); err != nil {
		return err
	}

	stats := make([]SenderReadStat, 0, len(statsBySender))
	for _, stat := range statsBySender {
		if stat.ReadCount == 0 {
			continue
		}
		stat.Boost = senderReadBoost(stat.ReadCount, stat.TotalCount)
		stats = append(stats, *stat)
	}
	sortSenderStats(stats)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sender_read_stats WHERE user_id = ?`, userID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO sender_read_stats (user_id, sender, read_count, total_count, boost, updated_at) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := nowUnix()
	for _, stat := range stats {
		if _, err := stmt.ExecContext(ctx, userID, stat.Sender, stat.ReadCount, stat.TotalCount, stat.Boost, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func senderReadBoost(readCount, totalCount int) float64 {
	if readCount <= 0 || totalCount <= 0 {
		return 0
	}
	ratio := float64(readCount) / float64(totalCount)
	boost := 0.6 + ratio*1.4 + float64(readCount)/8
	if boost > 8 {
		return 8
	}
	return boost
}
