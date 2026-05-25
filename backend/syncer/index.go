package syncer

import (
	"context"
	"errors"
	"fmt"
	"log"

	"mailmirror/backend/mailparse"
	"mailmirror/backend/plugins"
	languagesearch "mailmirror/backend/plugins/language_search"
	"mailmirror/backend/search"
	"mailmirror/backend/store"
)

func (s *Service) storeFetchedMessage(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox, item FetchedMessage) (store.MessageRecord, error) {
	parsed, err := mailparse.Parse(item.Raw)
	if err != nil {
		parsed = mailparse.ParsedMessage{
			Subject: fmt.Sprintf("Unparseable message UID %d", item.UID),
			Text:    fmt.Sprintf("MailMirror stored the raw message, but could not parse its MIME body: %v. Download the raw .eml to inspect it.", err),
		}
	}
	date := parsed.Date
	if date.IsZero() {
		date = item.InternalDate
	}

	rawHash := sha256Hex(item.Raw)
	blobPath := ""
	blobRecordPath := remoteMessagePath(userID, account.ID, item.Mailbox, item.UID, rawHash)
	blobKind := "message-remote"
	blobSize := int64(0)
	if s.shouldRetainBlob(date) {
		saved, err := s.Blobs.SaveRawMessage(userID, account.ID, item.Mailbox, item.UID, item.Raw)
		if err != nil {
			return store.MessageRecord{}, err
		}
		blobPath = saved.Path
		blobRecordPath = saved.Path
		blobKind = "message"
		blobSize = saved.Size
		rawHash = saved.SHA256
	}
	blobRec, err := s.Store.CreateBlob(ctx, store.BlobRecord{
		UserID: userID,
		Kind:   blobKind,
		Path:   blobRecordPath,
		SHA256: rawHash,
		Size:   blobSize,
	})
	if err != nil {
		return store.MessageRecord{}, err
	}

	languageCode := ""
	if s.pluginEnabled(ctx, plugins.LanguageSearch) {
		languageCode = languagesearch.DetectCode(parsed.Subject, parsed.Text)
	}
	msg, err := s.Store.CreateMessage(ctx, store.CreateMessage{
		UserID:           userID,
		AccountID:        account.ID,
		MailboxID:        mailbox.ID,
		BlobID:           blobRec.ID,
		MessageIDHeader:  parsed.MessageID,
		InReplyTo:        parsed.InReplyTo,
		ReferencesHeader: parsed.References,
		Subject:          parsed.Subject,
		LanguageCode:     languageCode,
		FromAddr:         parsed.From,
		ToAddr:           parsed.To,
		CCAddr:           parsed.CC,
		Date:             date,
		InternalDate:     item.InternalDate,
		UID:              item.UID,
		Size:             item.Size,
		BlobPath:         blobPath,
		BodyText:         store.MessageBodyPreview(parsed.Text, store.DefaultMessageBodyPreviewBytes),
		BodyHTML:         "",
		IsRead:           hasSeen(item.Flags),
		IsStarred:        hasFlagged(item.Flags),
		HasAttachments:   len(parsed.Files) > 0,
	})
	if err != nil {
		return store.MessageRecord{}, err
	}
	if msg.LanguageCode != languageCode {
		msg.LanguageCode = languageCode
		if err := s.Store.UpdateMessageLanguage(ctx, userID, msg.ID, languageCode); err != nil {
			return store.MessageRecord{}, err
		}
	}
	if err := s.Store.CreateLocation(ctx, userID, msg.ID, mailbox.ID, item.UID); err != nil {
		return store.MessageRecord{}, err
	}
	attachmentDocs := make([]search.AttachmentDoc, 0, len(parsed.Files))
	visibleAttachmentCount := 0
	if len(parsed.Files) > 0 {
		if err := s.Store.DeleteAttachmentsForMessage(ctx, userID, msg.ID); err != nil {
			return store.MessageRecord{}, err
		}
	}
	for _, file := range parsed.Files {
		if _, err := s.Store.CreateAttachment(ctx, store.Attachment{
			UserID:      userID,
			MessageID:   msg.ID,
			BlobID:      blobRec.ID,
			Filename:    file.Filename,
			ContentType: file.ContentType,
			ContentID:   file.ContentID,
			IsInline:    file.IsInline,
			Size:        int64(len(file.Data)),
			BlobPath:    "",
		}); err != nil {
			return store.MessageRecord{}, err
		}
		if !file.IsInline {
			visibleAttachmentCount++
			attachmentDocs = append(attachmentDocs, search.AttachmentDoc{
				Filename:    file.Filename,
				ContentType: file.ContentType,
				Text:        file.SearchableText(),
			})
		}
	}
	msg.HasAttachments = visibleAttachmentCount > 0
	if mailbox.IncludeInSearch && s.Search != nil {
		indexMsg := msg
		indexMsg.BodyText = parsed.Text
		indexMsg.BodyHTML = ""
		if err := s.Search.IndexMessage(ctx, indexMsg, attachmentDocs); err != nil {
			return store.MessageRecord{}, err
		}
	}
	if err := s.Store.MarkMessageAttachmentIndexed(ctx, userID, msg.ID, visibleAttachmentCount > 0); err != nil {
		return store.MessageRecord{}, err
	}
	return msg, nil
}

func (s *Service) IndexPendingAttachmentsForUser(ctx context.Context, userID int64, limit int) (int, error) {
	messages, err := s.Store.ListMessagesNeedingAttachmentIndex(ctx, userID, limit)
	if err != nil {
		return 0, err
	}
	for _, msg := range messages {
		if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
			return 0, err
		}
	}
	return len(messages), nil
}

func (s *Service) IndexAttachmentsForMessage(ctx context.Context, msg store.MessageRecord) error {
	if s.Search == nil {
		return nil
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, msg.UserID, msg.MailboxID)
	if err != nil {
		return err
	}
	if !mailbox.IncludeInSearch {
		return s.Search.DeleteMessage(ctx, msg.UserID, msg.ID)
	}
	raw, err := s.FetchRawMessageForMessage(ctx, msg.UserID, msg)
	if err != nil {
		return err
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		if msg.LanguageCode == "" && s.pluginEnabled(ctx, plugins.LanguageSearch) {
			msg.LanguageCode = languagesearch.DetectCode(msg.Subject, msg.BodyText)
			if err := s.Store.UpdateMessageLanguage(ctx, msg.UserID, msg.ID, msg.LanguageCode); err != nil {
				return err
			}
		}
		if indexErr := s.Search.IndexMessage(ctx, msg, nil); indexErr != nil {
			return indexErr
		}
		return s.Store.MarkMessageAttachmentIndexed(ctx, msg.UserID, msg.ID, false)
	}
	if len(parsed.Files) > 0 {
		if err := s.Store.DeleteAttachmentsForMessage(ctx, msg.UserID, msg.ID); err != nil {
			return err
		}
	}
	attachmentDocs := make([]search.AttachmentDoc, 0, len(parsed.Files))
	visibleAttachmentCount := 0
	for _, file := range parsed.Files {
		if _, err := s.Store.CreateAttachment(ctx, store.Attachment{
			UserID:      msg.UserID,
			MessageID:   msg.ID,
			BlobID:      msg.BlobID,
			Filename:    file.Filename,
			ContentType: file.ContentType,
			ContentID:   file.ContentID,
			IsInline:    file.IsInline,
			Size:        int64(len(file.Data)),
			BlobPath:    "",
		}); err != nil {
			return err
		}
		if !file.IsInline {
			visibleAttachmentCount++
			attachmentDocs = append(attachmentDocs, search.AttachmentDoc{
				Filename:    file.Filename,
				ContentType: file.ContentType,
				Text:        file.SearchableText(),
			})
		}
	}
	msg.HasAttachments = visibleAttachmentCount > 0
	msg.BodyText = parsed.Text
	msg.BodyHTML = ""
	if s.pluginEnabled(ctx, plugins.LanguageSearch) {
		msg.LanguageCode = languagesearch.DetectCode(parsed.Subject, parsed.Text)
		if err := s.Store.UpdateMessageLanguage(ctx, msg.UserID, msg.ID, msg.LanguageCode); err != nil {
			return err
		}
	} else {
		msg.LanguageCode = ""
	}
	if err := s.Search.IndexMessage(ctx, msg, attachmentDocs); err != nil {
		return err
	}
	return s.Store.MarkMessageAttachmentIndexed(ctx, msg.UserID, msg.ID, visibleAttachmentCount > 0)
}

func (s *Service) ReconcileMailboxSearchIndex(ctx context.Context, userID, mailboxID int64, include bool) error {
	if s.Search == nil {
		return nil
	}
	var afterID int64
	for {
		messages, err := s.Store.ListMessagesForMailboxIndex(ctx, userID, mailboxID, 200, afterID)
		if err != nil {
			return err
		}
		if len(messages) == 0 {
			return nil
		}
		for _, msg := range messages {
			afterID = msg.ID
			if !include {
				if err := s.Search.DeleteMessage(ctx, msg.UserID, msg.ID); err != nil {
					return err
				}
				continue
			}
			if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
				return err
			}
		}
	}
}

func (s *Service) StartRebuildMailboxSearchIndex(ctx context.Context, userID, mailboxID int64, onDone func()) (store.SyncRun, error) {
	if s.Search == nil {
		return store.SyncRun{}, errors.New("search is not configured")
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, userID, mailboxID)
	if err != nil {
		return store.SyncRun{}, err
	}
	total, err := s.Store.CountMessagesForMailbox(ctx, userID, mailboxID)
	if err != nil {
		return store.SyncRun{}, err
	}
	run, err := s.Store.CreateSyncRun(ctx, userID, mailbox.AccountID)
	if err != nil {
		return store.SyncRun{}, err
	}
	progress := store.SyncProgress{MessagesTotal: total, MailboxesTotal: 1, CurrentMailbox: "Rebuilding index: " + mailbox.Name}
	if err := s.Store.UpdateSyncRunProgress(ctx, userID, run.ID, progress); err != nil {
		return store.SyncRun{}, err
	}
	s.notify(userID)
	go s.runRebuildMailboxSearchIndex(context.Background(), userID, mailboxID, mailbox.Name, run.ID, progress, onDone)
	return run, nil
}

func (s *Service) runRebuildMailboxSearchIndex(ctx context.Context, userID, mailboxID int64, mailboxName string, runID int64, progress store.SyncProgress, onDone func()) {
	status := "ok"
	errText := ""
	defer func() {
		if ctx.Err() != nil && status == "ok" {
			status = "interrupted"
			errText = "Server stopped before this rebuild finished."
		}
		if status == "ok" {
			progress.MailboxesDone = 1
		}
		if err := s.Store.FinishSyncRun(context.Background(), userID, runID, status, progress, errText); err != nil {
			log.Printf("finish search index rebuild user_id=%d run_id=%d: %v", userID, runID, err)
		}
		s.notify(userID)
		if status == "ok" && onDone != nil {
			onDone()
		}
	}()
	var afterID int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		messages, err := s.Store.ListMessagesForMailboxIndex(ctx, userID, mailboxID, 100, afterID)
		if err != nil {
			status = "failed"
			errText = err.Error()
			return
		}
		if len(messages) == 0 {
			return
		}
		for _, msg := range messages {
			afterID = msg.ID
			progress.CurrentMailbox = "Rebuilding index: " + mailboxName
			progress.CurrentUID = msg.UID
			if err := s.Search.DeleteMessage(ctx, msg.UserID, msg.ID); err != nil {
				status = "failed"
				errText = err.Error()
				return
			}
			if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
				status = "failed"
				errText = err.Error()
				return
			}
			progress.MessagesSeen++
			progress.MessagesStored++
			if err := s.Store.UpdateSyncRunProgress(ctx, userID, runID, progress); err != nil {
				status = "failed"
				errText = err.Error()
				return
			}
			s.notify(userID)
		}
	}
}
