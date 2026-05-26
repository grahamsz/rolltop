// File overview: Batched search-index writes for fetched and repaired message documents.

package syncer

import (
	"context"

	"mailmirror/backend/search"
)

const fetchedSearchIndexBatchSize = 25

// pendingFetchedSearchIndex carries a Bleve document plus the metadata flag
// that can only be marked after the document has been committed successfully.
type pendingFetchedSearchIndex struct {
	Document              search.MessageIndexDocument
	HasVisibleAttachments bool
}

// fetchedSearchIndexBatch amortizes Bleve commit cost during IMAP fetches and
// repair indexing. SQLite/blob writes still happen one message at a time, but
// search documents are flushed in small groups; if a flush fails,
// attachment_indexed_at remains unset so the normal repair path can retry safely.
type fetchedSearchIndexBatch struct {
	service *Service
	items   []pendingFetchedSearchIndex
}

func newFetchedSearchIndexBatch(service *Service) *fetchedSearchIndexBatch {
	return &fetchedSearchIndexBatch{service: service}
}

// Add queues one prepared message and flushes when the batch reaches the
// configured size. Nil entries represent mailboxes that are not search-visible.
func (b *fetchedSearchIndexBatch) Add(ctx context.Context, item *pendingFetchedSearchIndex) error {
	if item == nil {
		return nil
	}
	b.items = append(b.items, *item)
	if len(b.items) < fetchedSearchIndexBatchSize {
		return nil
	}
	return b.Flush(ctx)
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
		if err := b.service.Search.IndexMessages(ctx, documents); err != nil {
			return err
		}
	}
	for _, item := range b.items {
		message := item.Document.Message
		if err := b.service.Store.MarkMessageAttachmentIndexed(ctx, message.UserID, message.ID, item.HasVisibleAttachments); err != nil {
			return err
		}
	}
	b.items = b.items[:0]
	return nil
}
