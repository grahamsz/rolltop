package search

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/blevesearch/bleve/v2"

	"rolltop/backend/store"
)

const (
	messageIndexChunkTargetBytes uint64 = 12 * 1024 * 1024
	// The iterator appends the document that crosses the soft target, then
	// returns without projecting the next one. Reserve that final document's
	// maximum bounded payload so projection stays inside coordinator accounting.
	maxMessageIndexProjectedDocumentBytes uint64 = 8*maxIndexedHeaderBytes +
		maxIndexedBodyBytes +
		2*maxIndexedNamesBytes +
		maxIndexedAttachmentsBytes +
		maxCompoundFieldBytes +
		maxIndexedLanguageBytes +
		2*20 + 5*5
	messageIndexPreparationReservationBytes = messageIndexChunkTargetBytes + maxMessageIndexProjectedDocumentBytes
)

type messageIndexProjector func(store.MessageRecord, []AttachmentDoc) map[string]any

// preparedMessageIndexDocument contains only the bounded projection handed to
// Bleve plus numeric routing metadata. It intentionally does not retain the raw
// MessageIndexDocument, MIME text, or attachment slice.
type preparedMessageIndexDocument struct {
	id             string
	messageID      int64
	accountID      int64
	mailboxID      int64
	projectedBytes uint64
	projection     map[string]any
}

// preparedMessageIndexChunk is one soft-target Bleve commit. Its final document
// may cross the target, but bounded projection limits cap that overshoot.
type preparedMessageIndexChunk struct {
	userID         int64
	accountID      int64
	mailboxID      int64
	projectedBytes uint64
	documents      []preparedMessageIndexDocument
}

type preparedTenantMessageIndex struct {
	userID int64
	chunks []preparedMessageIndexChunk
}

type messageIndexTenantPlan struct {
	userID    int64
	documents []MessageIndexDocument
}

type messageIndexChunkIterator struct {
	userID      int64
	documents   []MessageIndexDocument
	next        int
	targetBytes uint64
	projector   messageIndexProjector
}

// prepareMessageIndexChunks projects every input exactly once, retains tenant
// first-seen order and per-tenant message order, and finishes each commit when
// its bounded string payload reaches or crosses the soft target.
func prepareMessageIndexChunks(ctx context.Context, documents []MessageIndexDocument) ([]preparedTenantMessageIndex, error) {
	return prepareMessageIndexChunksWith(ctx, documents, messageIndexChunkTargetBytes, buildMessageDocument)
}

func prepareMessageIndexChunksWith(ctx context.Context, documents []MessageIndexDocument, targetBytes uint64, projector messageIndexProjector) ([]preparedTenantMessageIndex, error) {
	plans, err := planMessageIndexTenants(ctx, documents)
	if err != nil {
		return nil, err
	}
	tenants := make([]preparedTenantMessageIndex, 0, len(plans))
	for _, plan := range plans {
		prepared := preparedTenantMessageIndex{userID: plan.userID}
		iterator := newMessageIndexChunkIterator(plan, targetBytes, projector)
		for {
			chunk, ok, err := iterator.Next(ctx)
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			prepared.chunks = append(prepared.chunks, chunk)
		}
		tenants = append(tenants, prepared)
	}
	return tenants, nil
}

func planMessageIndexTenants(ctx context.Context, documents []MessageIndexDocument) ([]messageIndexTenantPlan, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	plans := make([]messageIndexTenantPlan, 0, 1)
	positions := make(map[int64]int, 1)
	for _, document := range documents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		userID := document.Message.UserID
		position, exists := positions[userID]
		if !exists {
			position = len(plans)
			positions[userID] = position
			plans = append(plans, messageIndexTenantPlan{userID: userID})
		}
		plans[position].documents = append(plans[position].documents, document)
	}
	return plans, nil
}

func newMessageIndexChunkIterator(plan messageIndexTenantPlan, targetBytes uint64, projector messageIndexProjector) *messageIndexChunkIterator {
	if targetBytes == 0 {
		targetBytes = messageIndexChunkTargetBytes
	}
	if projector == nil {
		projector = buildMessageDocument
	}
	return &messageIndexChunkIterator{
		userID: plan.userID, documents: plan.documents, targetBytes: targetBytes, projector: projector,
	}
}

func (i *messageIndexChunkIterator) HasNext() bool {
	return i != nil && i.next < len(i.documents)
}

func (i *messageIndexChunkIterator) reservationBytes() uint64 {
	if i == nil {
		return 0
	}
	if i.targetBytes == messageIndexChunkTargetBytes {
		return messageIndexPreparationReservationBytes
	}
	return saturatingAdd(i.targetBytes, maxMessageIndexProjectedDocumentBytes)
}

func (i *messageIndexChunkIterator) Next(ctx context.Context) (preparedMessageIndexChunk, bool, error) {
	if !i.HasNext() {
		return preparedMessageIndexChunk{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return preparedMessageIndexChunk{}, false, err
	}
	chunk := preparedMessageIndexChunk{userID: i.userID}
	for i.next < len(i.documents) {
		if err := ctx.Err(); err != nil {
			return preparedMessageIndexChunk{}, false, err
		}
		document := i.documents[i.next]
		i.next++
		projection := i.projector(document.Message, document.Attachments)
		prepared := preparedMessageIndexDocument{
			id:             strconv.FormatInt(document.Message.ID, 10),
			messageID:      document.Message.ID,
			accountID:      document.Message.AccountID,
			mailboxID:      document.Message.MailboxID,
			projectedBytes: projectedDocumentStringBytes(projection),
			projection:     projection,
		}
		chunk.append(prepared)
		if chunk.projectedBytes >= i.targetBytes {
			return chunk, true, nil
		}
	}
	return chunk, true, nil
}

func (c *preparedMessageIndexChunk) append(document preparedMessageIndexDocument) {
	if len(c.documents) == 0 {
		c.accountID = document.accountID
		c.mailboxID = document.mailboxID
	} else {
		if c.accountID != document.accountID {
			c.accountID = 0
		}
		if c.mailboxID != document.mailboxID {
			c.mailboxID = 0
		}
	}
	c.projectedBytes = saturatingAdd(c.projectedBytes, document.projectedBytes)
	c.documents = append(c.documents, document)
}

func projectedDocumentStringBytes(projection map[string]any) uint64 {
	var total uint64
	for _, value := range projection {
		text, ok := value.(string)
		if !ok {
			continue
		}
		total = saturatingAdd(total, uint64(len(text)))
	}
	return total
}

func saturatingAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}

// newBleveBatch maps this prepared projection into a new set of Bleve document
// objects. Callers must never reuse a Batch after an active Batch call stalls.
func (c preparedMessageIndexChunk) newBleveBatch(index bleve.Index) (*bleve.Batch, error) {
	if index == nil {
		return nil, fmt.Errorf("prepare Bleve batch: index is nil")
	}
	batch := index.NewBatch()
	for _, document := range c.documents {
		if err := batch.Index(document.id, document.projection); err != nil {
			return nil, fmt.Errorf("prepare Bleve document id %d: %w", document.messageID, err)
		}
	}
	return batch, nil
}

func (c preparedMessageIndexChunk) diagnostics(operation string) bleveErrorContext {
	details := bleveErrorContext{
		Operation:  operation,
		UserID:     c.userID,
		AccountID:  c.accountID,
		MailboxID:  c.mailboxID,
		Documents:  len(c.documents),
		BatchBytes: c.projectedBytes,
	}
	details.DocumentIDs = make([]int64, 0, min(len(c.documents), maxStallDocumentIDs))
	for _, document := range c.documents {
		id := document.messageID
		if id <= 0 {
			continue
		}
		if details.FirstDocumentID == 0 || id < details.FirstDocumentID {
			details.FirstDocumentID = id
		}
		if id > details.LastDocumentID {
			details.LastDocumentID = id
		}
		if len(details.DocumentIDs) < maxStallDocumentIDs {
			details.DocumentIDs = append(details.DocumentIDs, id)
		}
	}
	return details
}
