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

func (s *Store) ListReadSenderStatsForUser(ctx context.Context, userID int64, limit int) ([]SenderReadStat, error) {
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT from_addr, is_read FROM messages WHERE user_id = ? AND from_addr != ''`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statsBySender := map[string]*SenderReadStat{}
	for rows.Next() {
		var from string
		var isRead bool
		if err := rows.Scan(&from, &isRead); err != nil {
			return nil, err
		}
		sender := SenderIdentity(from)
		if sender == "" {
			continue
		}
		stat := statsBySender[sender]
		if stat == nil {
			stat = &SenderReadStat{Sender: sender}
			statsBySender[sender] = stat
		}
		stat.TotalCount++
		if isRead {
			stat.ReadCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stats := make([]SenderReadStat, 0, len(statsBySender))
	for _, stat := range statsBySender {
		if stat.ReadCount == 0 {
			continue
		}
		ratio := float64(stat.ReadCount) / float64(stat.TotalCount)
		boost := 0.6 + ratio*1.4 + float64(stat.ReadCount)/8
		if boost > 8 {
			boost = 8
		}
		stat.Boost = boost
		stats = append(stats, *stat)
	}
	sortSenderStats(stats)
	if len(stats) > limit {
		stats = stats[:limit]
	}
	return stats, nil
}
