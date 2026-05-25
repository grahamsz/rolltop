package store

import "database/sql"

func scanMessages(rows *sql.Rows) ([]MessageRecord, error) {
	var out []MessageRecord
	for rows.Next() {
		var m MessageRecord
		var dateUnix, internalUnix, indexedAt, created, updated int64
		if err := rows.Scan(&m.ID, &m.UserID, &m.AccountID, &m.MailboxID, &m.BlobID, &m.MessageIDHeader, &m.InReplyTo, &m.ReferencesHeader, &m.ThreadKey, &m.Subject, &m.LanguageCode, &m.FromAddr, &m.ToAddr, &m.CCAddr,
			&dateUnix, &internalUnix, &m.UID, &m.Size, &m.BlobPath, &m.BodyText, &m.BodyHTML, &m.IsRead, &m.ReadSyncPending, &m.IsStarred, &m.StarSyncPending, &m.HasAttachments, &indexedAt, &created, &updated); err != nil {
			return nil, err
		}
		m.Date = unixTime(dateUnix)
		m.InternalDate = unixTime(internalUnix)
		m.AttachmentIndexedAt = unixTime(indexedAt)
		m.CreatedAt = unixTime(created)
		m.UpdatedAt = unixTime(updated)
		out = append(out, m)
	}
	return out, rows.Err()
}
