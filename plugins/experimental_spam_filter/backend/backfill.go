package main

import (
	"context"
	"database/sql"
	"strings"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func (p *spamFilterPlugin) reserveBackfill(userID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.backfills == nil {
		p.backfills = make(map[int64]context.CancelFunc)
	}
	if _, exists := p.backfills[userID]; exists {
		return false
	}
	// A placeholder closes the race between the API check and goroutine launch.
	p.backfills[userID] = func() {}
	return true
}

func (p *spamFilterPlugin) releaseBackfill(userID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.backfills, userID)
}

func (p *spamFilterPlugin) backfillActive(userID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, active := p.backfills[userID]
	return active
}

func (p *spamFilterPlugin) launchBackfill(host plugins.MessageClassificationHost, userID int64, limit int) {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	if p.backfills == nil {
		p.mu.Unlock()
		cancel()
		return
	}
	p.backfills[userID] = cancel
	p.mu.Unlock()
	go p.runBackfill(ctx, host, userID, limit)
}

func (p *spamFilterPlugin) runBackfill(ctx context.Context, host plugins.MessageClassificationHost, userID int64, limit int) {
	defer p.releaseBackfill(userID)
	changed := false
	defer func() { notifyBackfillChange(host, userID, changed) }()
	st, db, err := pluginUserDB(ctx, host, userID)
	if err != nil {
		return
	}
	_, modelVersion, _ := p.model()
	ids, err := backfillMessageIDs(ctx, db, userID, modelVersion, limit)
	if err != nil {
		_ = finishBackfillRecord(context.Background(), db, userID, "failed", "could not select local messages")
		return
	}
	if err := setBackfillRequested(ctx, db, userID, len(ids)); err != nil {
		_ = finishBackfillRecord(context.Background(), db, userID, "failed", "could not initialize backfill progress")
		return
	}
	processed := 0
	failed := 0
	lastMessageID := int64(0)
	for _, messageID := range ids {
		if err := ctx.Err(); err != nil {
			_ = finishBackfillRecord(context.Background(), db, userID, "cancelled", "backfill cancelled")
			return
		}
		lastMessageID = messageID
		message, err := st.GetMessageForUser(ctx, userID, messageID)
		if err != nil {
			failed++
			processed++
			_ = updateBackfillRecord(ctx, db, userID, processed, failed, lastMessageID, "message is no longer available")
			continue
		}
		attachments, err := st.ListAttachmentsForMessage(ctx, userID, messageID)
		if err != nil {
			failed++
			processed++
			_ = updateBackfillRecord(ctx, db, userID, processed, failed, lastMessageID, "attachment metadata is unavailable")
			continue
		}
		input := classificationInputFromStored(message, attachments)
		coverage := "preview"
		if strings.TrimSpace(input.BodyText) == "" {
			coverage = "metadata"
		}
		if message.IsEncrypted {
			coverage = "encrypted_metadata"
		}
		if _, err := p.classifyAndSave(ctx, host, db, input, coverage); err != nil {
			failed++
		} else {
			changed = true
		}
		processed++
		lastError := ""
		if failed > 0 {
			lastError = "one or more messages could not be classified"
		}
		if err := updateBackfillRecord(ctx, db, userID, processed, failed, lastMessageID, lastError); err != nil {
			_ = finishBackfillRecord(context.Background(), db, userID, "failed", "could not save backfill progress")
			return
		}
	}
	status := "complete"
	lastError := ""
	if len(ids) > 0 && failed == len(ids) {
		status = "failed"
		lastError = "no selected messages could be classified"
	} else if failed > 0 {
		lastError = "some selected messages could not be classified"
	}
	_ = finishBackfillRecord(context.Background(), db, userID, status, lastError)
}

func notifyBackfillChange(host plugins.BackendHost, userID int64, changed bool) {
	if changed {
		notifyUserChanged(host, userID)
	}
}

func backfillMessageIDs(ctx context.Context, db *sql.DB, userID int64, modelVersion string, limit int) ([]int64, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := db.QueryContext(ctx, `SELECT m.id FROM messages m
		LEFT JOIN plugin_experimental_spam_classifications c
		 ON c.user_id = m.user_id AND c.message_id = m.id
		WHERE m.user_id = ?
		ORDER BY CASE WHEN c.message_id IS NULL OR c.model_version != ? THEN 0 ELSE 1 END,
		 m.id DESC LIMIT ?`, userID, modelVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func classificationInputFromStored(message store.MessageRecord, attachments []store.Attachment) plugins.MessageClassificationInput {
	bodyText := message.BodyText
	hasHTML := message.BodyHTML != ""
	if message.IsEncrypted {
		bodyText = ""
		hasHTML = false
		attachments = nil
	}
	input := plugins.MessageClassificationInput{
		UserID:          message.UserID,
		MessageID:       message.ID,
		MessageIDHeader: message.MessageIDHeader,
		AccountID:       message.AccountID,
		MailboxID:       message.MailboxID,
		Date:            message.Date,
		From:            message.FromAddr,
		To:              message.ToAddr,
		CC:              message.CCAddr,
		Subject:         message.Subject,
		BodyText:        bodyText,
		BodyTruncated:   strings.TrimSpace(bodyText) != "",
		HasHTML:         hasHTML,
		IsEncrypted:     message.IsEncrypted,
		IsSigned:        message.IsSigned,
		Attachments:     make([]plugins.MessageClassificationAttachment, 0, len(attachments)),
	}
	for _, attachment := range attachments {
		input.Attachments = append(input.Attachments, plugins.MessageClassificationAttachment{
			Filename: attachment.Filename, ContentType: attachment.ContentType, Size: attachment.Size,
		})
	}
	return input
}
