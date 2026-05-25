package store

import "context"

func (s *Store) CreateSyncRun(ctx context.Context, userID, accountID int64) (SyncRun, error) {
	started := nowUnix()
	res, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `INSERT INTO sync_runs (user_id, account_id, status, started_at, updated_at) VALUES (?, ?, 'running', ?, ?)`, userID, accountID, started, started)
	if err != nil {
		return SyncRun{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return SyncRun{}, err
	}
	return s.GetSyncRunForUser(ctx, userID, id)
}

func (s *Store) MarkRunningSyncRunsInterrupted(ctx context.Context) (int64, error) {
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return 0, err
		}
		var total int64
		for _, user := range users {
			us, err := s.UserStore(ctx, user.ID)
			if err != nil {
				return total, err
			}
			n, err := us.MarkRunningSyncRunsInterrupted(ctx)
			if err != nil {
				return total, err
			}
			total += n
		}
		return total, nil
	}
	now := nowUnix()
	res, err := s.db.ExecContext(ctx, `UPDATE sync_runs
		SET status = 'interrupted', finished_at = ?, updated_at = ?, error = CASE WHEN error = '' THEN 'Server restarted before this sync finished.' ELSE error END
		WHERE status = 'running'`, now, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type SyncProgress struct {
	MessagesSeen     int
	MessagesStored   int
	MessagesSkipped  int
	NewMessages      int
	LatestNewFrom    string
	LatestNewSubject string
	MessagesTotal    int
	MailboxesDone    int
	MailboxesTotal   int
	CurrentMailbox   string
	CurrentUID       uint32
}

func (s *Store) UpdateSyncRunProgress(ctx context.Context, userID, id int64, p SyncProgress) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE sync_runs
		SET updated_at = ?, messages_seen = ?, messages_stored = ?, messages_skipped = ?, new_messages = ?, latest_new_from = ?, latest_new_subject = ?, messages_total = ?, mailboxes_done = ?, mailboxes_total = ?, current_mailbox = ?, current_uid = ?
		WHERE user_id = ? AND id = ?`,
		nowUnix(), p.MessagesSeen, p.MessagesStored, p.MessagesSkipped, p.NewMessages, p.LatestNewFrom, p.LatestNewSubject, p.MessagesTotal, p.MailboxesDone, p.MailboxesTotal, p.CurrentMailbox, p.CurrentUID, userID, id)
	return err
}

func (s *Store) FinishSyncRun(ctx context.Context, userID, id int64, status string, p SyncProgress, errText string) error {
	if len(errText) > 1000 {
		errText = errText[:1000]
	}
	now := nowUnix()
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE sync_runs
		SET status = ?, finished_at = ?, updated_at = ?, messages_seen = ?, messages_stored = ?, messages_skipped = ?, new_messages = ?, latest_new_from = ?, latest_new_subject = ?, messages_total = ?,
			mailboxes_done = ?, mailboxes_total = ?, current_mailbox = ?, current_uid = ?, error = ?
		WHERE user_id = ? AND id = ? AND NOT (status = 'interrupted' AND finished_at != 0)`,
		status, now, now, p.MessagesSeen, p.MessagesStored, p.MessagesSkipped, p.NewMessages, p.LatestNewFrom, p.LatestNewSubject, p.MessagesTotal, p.MailboxesDone, p.MailboxesTotal,
		p.CurrentMailbox, p.CurrentUID, errText, userID, id)
	return err
}

func (s *Store) GetSyncRunForUser(ctx context.Context, userID, id int64) (SyncRun, error) {
	var r SyncRun
	var started, finished, updated int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, user_id, account_id, status, started_at, finished_at, updated_at,
			messages_seen, messages_stored, messages_skipped, new_messages, latest_new_from, latest_new_subject, messages_total, mailboxes_done, mailboxes_total, current_mailbox, current_uid, error
		FROM sync_runs WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&r.ID, &r.UserID, &r.AccountID, &r.Status, &started, &finished, &updated,
			&r.MessagesSeen, &r.MessagesStored, &r.MessagesSkipped, &r.NewMessages, &r.LatestNewFrom, &r.LatestNewSubject, &r.MessagesTotal, &r.MailboxesDone, &r.MailboxesTotal, &r.CurrentMailbox, &r.CurrentUID, &r.Error)
	r.StartedAt = unixTime(started)
	r.FinishedAt = unixTime(finished)
	r.UpdatedAt = unixTime(updated)
	return r, err
}

func (s *Store) ListSyncRunsForUser(ctx context.Context, userID int64, limit int) ([]SyncRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, account_id, status, started_at, finished_at, updated_at,
			messages_seen, messages_stored, messages_skipped, new_messages, latest_new_from, latest_new_subject, messages_total, mailboxes_done, mailboxes_total, current_mailbox, current_uid, error
		FROM sync_runs WHERE user_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncRun
	for rows.Next() {
		var r SyncRun
		var started, finished, updated int64
		if err := rows.Scan(&r.ID, &r.UserID, &r.AccountID, &r.Status, &started, &finished, &updated,
			&r.MessagesSeen, &r.MessagesStored, &r.MessagesSkipped, &r.NewMessages, &r.LatestNewFrom, &r.LatestNewSubject, &r.MessagesTotal, &r.MailboxesDone, &r.MailboxesTotal, &r.CurrentMailbox, &r.CurrentUID, &r.Error); err != nil {
			return nil, err
		}
		r.StartedAt = unixTime(started)
		r.FinishedAt = unixTime(finished)
		r.UpdatedAt = unixTime(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListUserIDsWithAccounts(ctx context.Context) ([]int64, error) {
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, user := range users {
			var count int
			if err := s.mustDataDB(ctx, user.ID).QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_accounts WHERE user_id = ?`, user.ID).Scan(&count); err != nil {
				return nil, err
			}
			if count > 0 {
				ids = append(ids, user.ID)
			}
		}
		return ids, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT user_id FROM mail_accounts ORDER BY user_id`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}
