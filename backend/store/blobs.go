package store

import "context"

func (s *Store) CreateBlob(ctx context.Context, b BlobRecord) (BlobRecord, error) {
	ts := nowUnix()
	_, err := s.mustDataDB(ctx, b.UserID).ExecContext(ctx, `INSERT INTO blobs (user_id, kind, path, sha256, size, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, path) DO UPDATE SET
			kind = excluded.kind,
			sha256 = excluded.sha256,
			size = excluded.size,
			created_at = excluded.created_at`,
		b.UserID, b.Kind, b.Path, b.SHA256, b.Size, ts)
	if err != nil {
		return BlobRecord{}, err
	}
	return s.GetBlobByPathForUser(ctx, b.UserID, b.Path)
}

func (s *Store) GetBlobForUser(ctx context.Context, userID, id int64) (BlobRecord, error) {
	var b BlobRecord
	var created int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, user_id, kind, path, sha256, size, created_at FROM blobs WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&b.ID, &b.UserID, &b.Kind, &b.Path, &b.SHA256, &b.Size, &created)
	b.CreatedAt = unixTime(created)
	return b, err
}

func (s *Store) GetBlobByPathForUser(ctx context.Context, userID int64, blobPath string) (BlobRecord, error) {
	var b BlobRecord
	var created int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, user_id, kind, path, sha256, size, created_at FROM blobs WHERE user_id = ? AND path = ?`, userID, blobPath).
		Scan(&b.ID, &b.UserID, &b.Kind, &b.Path, &b.SHA256, &b.Size, &created)
	b.CreatedAt = unixTime(created)
	return b, err
}

func (s *Store) DeleteBlobForUser(ctx context.Context, userID, id int64) error {
	res, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `DELETE FROM blobs WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateAttachment(ctx context.Context, a Attachment) (Attachment, error) {
	ts := nowUnix()
	res, err := s.mustDataDB(ctx, a.UserID).ExecContext(ctx, `INSERT INTO attachments (user_id, message_id, blob_id, filename, content_type, content_id, is_inline, size, blob_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.UserID, a.MessageID, a.BlobID, a.Filename, a.ContentType, a.ContentID, boolInt(a.IsInline), a.Size, a.BlobPath, ts)
	if err != nil {
		return Attachment{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Attachment{}, err
	}
	return s.GetAttachmentForUser(ctx, a.UserID, id)
}

func (s *Store) DeleteAttachmentsForMessage(ctx context.Context, userID, messageID int64) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `DELETE FROM attachments WHERE user_id = ? AND message_id = ?`, userID, messageID)
	return err
}

func (s *Store) GetAttachmentForUser(ctx context.Context, userID, id int64) (Attachment, error) {
	var a Attachment
	var created int64
	var isInline int
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, user_id, message_id, blob_id, filename, content_type, content_id, is_inline, size, blob_path, created_at
		FROM attachments WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&a.ID, &a.UserID, &a.MessageID, &a.BlobID, &a.Filename, &a.ContentType, &a.ContentID, &isInline, &a.Size, &a.BlobPath, &created)
	a.IsInline = isInline != 0
	a.CreatedAt = unixTime(created)
	return a, err
}

func (s *Store) ListAttachmentsForMessage(ctx context.Context, userID, messageID int64) ([]Attachment, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, message_id, blob_id, filename, content_type, content_id, is_inline, size, blob_path, created_at
		FROM attachments WHERE user_id = ? AND message_id = ? ORDER BY id`, userID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		var created int64
		var isInline int
		if err := rows.Scan(&a.ID, &a.UserID, &a.MessageID, &a.BlobID, &a.Filename, &a.ContentType, &a.ContentID, &isInline, &a.Size, &a.BlobPath, &created); err != nil {
			return nil, err
		}
		a.IsInline = isInline != 0
		a.CreatedAt = unixTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}
