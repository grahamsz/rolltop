// File overview: Batched search-index writes for fetched and repaired message documents.

package syncer

import (
	"context"

	"rolltop/backend/search"
	"rolltop/backend/store"
)

const (
	fetchedSearchIndexBatchSize     = 25
	maintenanceSearchCheckpointSize = 5
)

// pendingFetchedSearchIndex carries a Bleve document plus the metadata flag
// that can only be marked after the document has been committed successfully.
type pendingFetchedSearchIndex struct {
	Document              search.MessageIndexDocument
	HasVisibleAttachments bool
	// KeepPending commits a fallback document without claiming that raw-body
	// enrichment completed. The attachment worker can retry the message later.
	KeepPending bool
}

// fetchedSearchIndexBatch amortizes Bleve commit cost during IMAP fetches and
// repair indexing. SQLite/blob writes still happen one message at a time, but
// search documents are flushed in small groups; if a flush fails,
// attachment_indexed_at remains unset so the normal repair path can retry safely.
type fetchedSearchIndexBatch struct {
	service  *Service
	maxItems int
	items    []pendingFetchedSearchIndex
}

type messageImportCompletionBatch struct {
	service    *Service
	userID     int64
	messageIDs []int64
}

func newFetchedSearchIndexBatch(service *Service) *fetchedSearchIndexBatch {
	return newFetchedSearchIndexBatchWithSize(service, fetchedSearchIndexBatchSize)
}

func newFetchedSearchIndexBatchWithSize(service *Service, maxItems int) *fetchedSearchIndexBatch {
	if maxItems <= 0 {
		maxItems = fetchedSearchIndexBatchSize
	}
	return &fetchedSearchIndexBatch{service: service, maxItems: maxItems}
}

func newMessageImportCompletionBatch(service *Service, userID int64) *messageImportCompletionBatch {
	return &messageImportCompletionBatch{service: service, userID: userID}
}

func (b *messageImportCompletionBatch) Add(messageID int64) {
	if b == nil || messageID <= 0 {
		return
	}
	b.messageIDs = append(b.messageIDs, messageID)
}

func (b *messageImportCompletionBatch) Empty() bool {
	return b == nil || len(b.messageIDs) == 0
}

func (b *messageImportCompletionBatch) Flush(ctx context.Context) error {
	if b.Empty() {
		return nil
	}
	if err := b.service.Store.MarkMessagesImportCompleted(ctx, b.userID, b.messageIDs); err != nil {
		return err
	}
	b.messageIDs = b.messageIDs[:0]
	return nil
}

// Add queues one prepared message and flushes when the batch reaches the
// configured size. Nil entries represent mailboxes that are not search-visible.
func (b *fetchedSearchIndexBatch) Add(ctx context.Context, item *pendingFetchedSearchIndex) error {
	if item == nil {
		return nil
	}
	b.items = append(b.items, *item)
	if len(b.items) < b.maxItems {
		return nil
	}
	return b.Flush(ctx)
}

func (b *fetchedSearchIndexBatch) Empty() bool {
	return b == nil || len(b.items) == 0
}

// Flush commits all pending search documents, then marks their attachment text
// extraction as complete. The mark intentionally happens after the Bleve batch
// so interrupted syncs leave rows eligible for reindex repair.
func (b *fetchedSearchIndexBatch) Flush(ctx context.Context) error {
	if len(b.items) == 0 {
		return nil
	}
	documents := make([]search.MessageIndexDocument, 0, len(b.items))
	for _, item := range b.items {
		documents = append(documents, item.Document)
	}
	if b.service.Search != nil {
		generationRecoveryPhase(ctx, "search-index-batch", "bleve")
		if err := b.service.Search.IndexMessages(ctx, documents); err != nil {
			return err
		}
	}
	for _, item := range b.items {
		message := item.Document.Message
		if item.KeepPending {
			if _, err := b.service.Store.GetMessageForUser(ctx, message.UserID, message.ID); err != nil {
				if store.IsNotFound(err) && b.service.Search != nil {
					if deleteErr := b.service.Search.DeleteMessage(ctx, message.UserID, message.ID); deleteErr != nil {
						return deleteErr
					}
					continue
				}
				return err
			}
			continue
		}
		generationRecoveryPhase(ctx, "sqlite-mark-search-indexed", "")
		if err := b.service.Store.MarkMessageAttachmentIndexed(ctx, message.UserID, message.ID, item.HasVisibleAttachments); err != nil {
			if store.IsNotFound(err) && b.service.Search != nil {
				// A move can remove the SQLite row while this batch is waiting for
				// Bleve's writer. Remove the just-committed stale document rather
				// than resurrecting a message that no longer belongs to the folder.
				if deleteErr := b.service.Search.DeleteMessage(ctx, message.UserID, message.ID); deleteErr != nil {
					return deleteErr
				}
				continue
			}
			return err
		}
	}
	b.items = b.items[:0]
	return nil
}
