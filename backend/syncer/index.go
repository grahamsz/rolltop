// File overview: Search indexing helpers used during sync.

package syncer

import (
	"context"
	"fmt"

	"mailmirror/backend/mailparse"
	"mailmirror/backend/plugins"
	languagesearch "mailmirror/backend/plugins/language_search"
	"mailmirror/backend/search"
	"mailmirror/backend/store"
)

func (s *Service) storeFetchedMessage(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox, item FetchedMessage) (store.MessageRecord, *pendingFetchedSearchIndex, error) {
	parsed, err := mailparse.Parse(item.Raw)
	if err != nil {
		parsed = mailparse.ParsedMessage{
			Subject: fmt.Sprintf("Unparseable message UID %d", item.UID),
			Text:    fmt.Sprintf("rolltop stored the raw message, but could not parse its MIME body: %v. Download the raw .eml to inspect it.", err),
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
			return store.MessageRecord{}, nil, err
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
		return store.MessageRecord{}, nil, err
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
		return store.MessageRecord{}, nil, err
	}
	if msg.LanguageCode != languageCode {
		msg.LanguageCode = languageCode
		if err := s.Store.UpdateMessageLanguage(ctx, userID, msg.ID, languageCode); err != nil {
			return store.MessageRecord{}, nil, err
		}
	}
	if err := s.Store.CreateLocation(ctx, userID, msg.ID, mailbox.ID, item.UID); err != nil {
		return store.MessageRecord{}, nil, err
	}
	attachmentDocs := make([]search.AttachmentDoc, 0, len(parsed.Files))
	visibleAttachmentCount := 0
	if len(parsed.Files) > 0 {
		if err := s.Store.DeleteAttachmentsForMessage(ctx, userID, msg.ID); err != nil {
			return store.MessageRecord{}, nil, err
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
			return store.MessageRecord{}, nil, err
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
		return msg, &pendingFetchedSearchIndex{
			Document: search.MessageIndexDocument{
				Message:     indexMsg,
				Attachments: attachmentDocs,
			},
			HasVisibleAttachments: visibleAttachmentCount > 0,
		}, nil
	}
	if err := s.Store.MarkMessageAttachmentIndexed(ctx, userID, msg.ID, visibleAttachmentCount > 0); err != nil {
		return store.MessageRecord{}, nil, err
	}
	return msg, nil, nil
}

// IndexPendingAttachmentsForUser indexes attachment text from raw message bodies for pending messages.
func (s *Service) IndexPendingAttachmentsForUser(ctx context.Context, userID int64, limit int) (int, error) {
	messages, err := s.Store.ListMessagesNeedingAttachmentIndex(ctx, userID, limit)
	if err != nil {
		return 0, err
	}
	batch := newFetchedSearchIndexBatch(s)
	processed := 0
	for _, msg := range messages {
		item, err := s.prepareAttachmentIndexMessage(ctx, msg)
		if err != nil {
			return processed, err
		}
		if err := batch.Add(ctx, item); err != nil {
			return processed, err
		}
		processed++
	}
	if err := batch.Flush(ctx); err != nil {
		return processed, err
	}
	return processed, nil
}

// IndexAttachmentsForMessage reparses one raw message, indexes its attachment text, and updates message metadata.
func (s *Service) IndexAttachmentsForMessage(ctx context.Context, msg store.MessageRecord) error {
	batch := newFetchedSearchIndexBatch(s)
	item, err := s.prepareAttachmentIndexMessage(ctx, msg)
	if err != nil {
		return err
	}
	if err := batch.Add(ctx, item); err != nil {
		return err
	}
	return batch.Flush(ctx)
}

// prepareAttachmentIndexMessage refreshes attachment rows and returns a pending
// Bleve document. The caller owns batching and the post-commit metadata mark.
func (s *Service) prepareAttachmentIndexMessage(ctx context.Context, msg store.MessageRecord) (*pendingFetchedSearchIndex, error) {
	if s.Search == nil {
		return nil, nil
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, msg.UserID, msg.MailboxID)
	if err != nil {
		return nil, err
	}
	if !mailbox.IncludeInSearch {
		return nil, s.Search.DeleteMessage(ctx, msg.UserID, msg.ID)
	}
	raw, err := s.FetchRawMessageForMessage(ctx, msg.UserID, msg)
	if err != nil {
		return nil, err
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		if msg.LanguageCode == "" && s.pluginEnabled(ctx, plugins.LanguageSearch) {
			msg.LanguageCode = languagesearch.DetectCode(msg.Subject, msg.BodyText)
			if err := s.Store.UpdateMessageLanguage(ctx, msg.UserID, msg.ID, msg.LanguageCode); err != nil {
				return nil, err
			}
		}
		return &pendingFetchedSearchIndex{
			Document: search.MessageIndexDocument{Message: msg},
		}, nil
	}
	if len(parsed.Files) > 0 {
		if err := s.Store.DeleteAttachmentsForMessage(ctx, msg.UserID, msg.ID); err != nil {
			return nil, err
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
			return nil, err
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
			return nil, err
		}
	} else {
		msg.LanguageCode = ""
	}
	return &pendingFetchedSearchIndex{
		Document: search.MessageIndexDocument{
			Message:     msg,
			Attachments: attachmentDocs,
		},
		HasVisibleAttachments: visibleAttachmentCount > 0,
	}, nil
}

// RepairMailboxSearchIndex indexes local mailbox messages that are missing from Bleve.
func (s *Service) RepairMailboxSearchIndex(ctx context.Context, userID int64, mailbox store.Mailbox, runID int64, progress *store.SyncProgress) (int, error) {
	if s.Search == nil || !mailbox.IncludeInSearch {
		return 0, nil
	}
	indexedIDs, err := s.Search.MailboxMessageIDs(ctx, userID, mailbox.ID)
	if err != nil {
		return 0, err
	}
	missing, err := s.countMissingMailboxSearchDocuments(ctx, userID, mailbox.ID, indexedIDs)
	if err != nil || missing == 0 {
		return 0, err
	}
	previousLatestFrom := ""
	previousLatestSubject := ""
	if progress != nil {
		previousLatestFrom = progress.LatestNewFrom
		previousLatestSubject = progress.LatestNewSubject
		progress.MessagesTotal += missing
		progress.CurrentMailbox = mailbox.Name
		progress.LatestNewFrom = "mailmirror:maintenance"
		progress.LatestNewSubject = "Repairing full-text index"
		if err := s.updateSyncProgress(ctx, userID, runID, *progress); err != nil {
			return 0, err
		}
	}

	indexed := 0
	var afterID int64
	batch := newFetchedSearchIndexBatch(s)
	for {
		messages, err := s.Store.ListMessagesForMailboxIndex(ctx, userID, mailbox.ID, 100, afterID)
		if err != nil {
			return indexed, err
		}
		if len(messages) == 0 {
			if err := batch.Flush(ctx); err != nil {
				return indexed, err
			}
			if progress != nil {
				progress.LatestNewFrom = previousLatestFrom
				progress.LatestNewSubject = previousLatestSubject
				if err := s.updateSyncProgress(ctx, userID, runID, *progress); err != nil {
					return indexed, err
				}
			}
			return indexed, nil
		}
		for _, msg := range messages {
			afterID = msg.ID
			if indexedIDs[msg.ID] {
				continue
			}
			if progress != nil {
				progress.CurrentMailbox = mailbox.Name
				progress.CurrentUID = msg.UID
			}
			item, err := s.prepareAttachmentIndexMessage(ctx, msg)
			if err != nil {
				return indexed, err
			}
			if err := batch.Add(ctx, item); err != nil {
				return indexed, err
			}
			indexed++
			indexedIDs[msg.ID] = true
			if progress != nil {
				progress.MessagesSeen++
				progress.MessagesStored++
				if err := s.updateSyncProgress(ctx, userID, runID, *progress); err != nil {
					return indexed, err
				}
			}
		}
	}
}

func (s *Service) countMissingMailboxSearchDocuments(ctx context.Context, userID, mailboxID int64, indexedIDs map[int64]bool) (int, error) {
	missing := 0
	var afterID int64
	for {
		messages, err := s.Store.ListMessagesForMailboxIndex(ctx, userID, mailboxID, 500, afterID)
		if err != nil {
			return missing, err
		}
		if len(messages) == 0 {
			return missing, nil
		}
		for _, msg := range messages {
			afterID = msg.ID
			if !indexedIDs[msg.ID] {
				missing++
			}
		}
	}
}

// ReconcileMailboxSearchIndex adds or removes documents when a mailbox search-visibility setting changes.
func (s *Service) ReconcileMailboxSearchIndex(ctx context.Context, userID, mailboxID int64, include bool) error {
	if s.Search == nil {
		return nil
	}
	var batch *fetchedSearchIndexBatch
	if include {
		batch = newFetchedSearchIndexBatch(s)
	}
	var afterID int64
	for {
		messages, err := s.Store.ListMessagesForMailboxIndex(ctx, userID, mailboxID, 200, afterID)
		if err != nil {
			return err
		}
		if len(messages) == 0 {
			if batch != nil {
				return batch.Flush(ctx)
			}
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
			item, err := s.prepareAttachmentIndexMessage(ctx, msg)
			if err != nil {
				return err
			}
			if err := batch.Add(ctx, item); err != nil {
				return err
			}
		}
	}
}
