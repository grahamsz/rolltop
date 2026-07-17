package search

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rolltop/backend/store"
)

func TestPrepareMessageIndexChunksPreservesTenantAndDocumentOrder(t *testing.T) {
	documents := []MessageIndexDocument{
		{Message: store.MessageRecord{ID: 70, UserID: 7, AccountID: 4, MailboxID: 34, Subject: "first tenant first"}},
		{Message: store.MessageRecord{ID: 30, UserID: 3, AccountID: 9, MailboxID: 90, Subject: "second tenant first"}},
		{Message: store.MessageRecord{ID: 71, UserID: 7, AccountID: 4, MailboxID: 34, Subject: "first tenant second"}},
		{Message: store.MessageRecord{ID: 31, UserID: 3, AccountID: 9, MailboxID: 91, Subject: "second tenant second"}},
	}
	projected := map[int64]int{}
	projector := func(message store.MessageRecord, _ []AttachmentDoc) map[string]any {
		projected[message.ID]++
		return map[string]any{"subject": message.Subject}
	}

	tenants, err := prepareMessageIndexChunksWith(context.Background(), documents, 1024, projector)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int64{tenants[0].userID, tenants[1].userID}; !reflect.DeepEqual(got, []int64{7, 3}) {
		t.Fatalf("tenant order = %v, want [7 3]", got)
	}
	if len(tenants[0].chunks) != 1 || len(tenants[1].chunks) != 1 {
		t.Fatalf("tenant chunks = [%d %d], want one each", len(tenants[0].chunks), len(tenants[1].chunks))
	}
	first := tenants[0].chunks[0]
	second := tenants[1].chunks[0]
	if got := preparedMessageIDs(first); !reflect.DeepEqual(got, []int64{70, 71}) {
		t.Fatalf("first tenant document order = %v", got)
	}
	if got := preparedMessageIDs(second); !reflect.DeepEqual(got, []int64{30, 31}) {
		t.Fatalf("second tenant document order = %v", got)
	}
	if first.userID != 7 || first.accountID != 4 || first.mailboxID != 34 {
		t.Fatalf("first tenant metadata = user:%d account:%d mailbox:%d", first.userID, first.accountID, first.mailboxID)
	}
	if second.userID != 3 || second.accountID != 9 || second.mailboxID != 0 {
		t.Fatalf("mixed-mailbox metadata = user:%d account:%d mailbox:%d", second.userID, second.accountID, second.mailboxID)
	}
	for _, document := range documents {
		if projected[document.Message.ID] != 1 {
			t.Fatalf("message %d projected %d times, want exactly once", document.Message.ID, projected[document.Message.ID])
		}
	}

	details := first.diagnostics("index-batch")
	if details.UserID != 7 || details.AccountID != 4 || details.MailboxID != 34 || details.Documents != 2 {
		t.Fatalf("diagnostics metadata = %+v", details)
	}
	if !reflect.DeepEqual(details.DocumentIDs, []int64{70, 71}) || details.FirstDocumentID != 70 || details.LastDocumentID != 71 {
		t.Fatalf("diagnostics IDs = %+v", details)
	}
}

func TestPrepareMessageIndexChunksSplitsAtByteTarget(t *testing.T) {
	documents := []MessageIndexDocument{
		{Message: store.MessageRecord{ID: 1, UserID: 8, BodyText: strings.Repeat("a", 6)}},
		{Message: store.MessageRecord{ID: 2, UserID: 8, BodyText: strings.Repeat("b", 4)}},
		{Message: store.MessageRecord{ID: 3, UserID: 8, BodyText: strings.Repeat("c", 7)}},
		{Message: store.MessageRecord{ID: 4, UserID: 8, BodyText: strings.Repeat("d", 15)}},
		{Message: store.MessageRecord{ID: 5, UserID: 8, BodyText: "e"}},
	}
	projected := 0
	projector := func(message store.MessageRecord, _ []AttachmentDoc) map[string]any {
		projected++
		return map[string]any{"body": message.BodyText}
	}
	tenants, err := prepareMessageIndexChunksWith(context.Background(), documents, 10, projector)
	if err != nil {
		t.Fatal(err)
	}
	if projected != len(documents) {
		t.Fatalf("projection calls = %d, want %d", projected, len(documents))
	}
	chunks := tenants[0].chunks
	wantIDs := [][]int64{{1, 2}, {3, 4}, {5}}
	wantBytes := []uint64{10, 22, 1}
	if len(chunks) != len(wantIDs) {
		t.Fatalf("chunk count = %d, want %d", len(chunks), len(wantIDs))
	}
	for index, chunk := range chunks {
		if got := preparedMessageIDs(chunk); !reflect.DeepEqual(got, wantIDs[index]) {
			t.Fatalf("chunk %d IDs = %v, want %v", index, got, wantIDs[index])
		}
		if chunk.projectedBytes != wantBytes[index] {
			t.Fatalf("chunk %d bytes = %d, want %d", index, chunk.projectedBytes, wantBytes[index])
		}
	}
	if chunks[1].projectedBytes <= 10 || chunks[1].projectedBytes > 10+15 {
		t.Fatalf("soft-target overshoot was not bounded by one document: %+v", chunks[1])
	}
}

func TestMessageIndexChunkIteratorProjectsLazily(t *testing.T) {
	documents := make([]MessageIndexDocument, 0, 5)
	for id := int64(1); id <= 5; id++ {
		documents = append(documents, MessageIndexDocument{Message: store.MessageRecord{
			ID: id, UserID: 8, BodyText: strings.Repeat("x", 6),
		}})
	}
	projected := 0
	projector := func(message store.MessageRecord, _ []AttachmentDoc) map[string]any {
		projected++
		return map[string]any{"body": message.BodyText}
	}
	plans, err := planMessageIndexTenants(context.Background(), documents)
	if err != nil {
		t.Fatal(err)
	}
	if projected != 0 {
		t.Fatalf("tenant planning projected %d documents", projected)
	}
	iterator := newMessageIndexChunkIterator(plans[0], 10, projector)
	first, ok, err := iterator.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("first chunk = %+v, %t, %v", first, ok, err)
	}
	if got := preparedMessageIDs(first); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("first chunk IDs = %v", got)
	}
	if projected != 2 {
		t.Fatalf("first chunk projected %d documents, want only the committed chunk", projected)
	}

	gotIDs := preparedMessageIDs(first)
	for {
		chunk, ok, err := iterator.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		gotIDs = append(gotIDs, preparedMessageIDs(chunk)...)
	}
	if !reflect.DeepEqual(gotIDs, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("lazy iterator order = %v", gotIDs)
	}
	if projected != len(documents) {
		t.Fatalf("projection calls = %d, want %d", projected, len(documents))
	}
}

func TestPreparedChunkCreatesFreshBleveBatch(t *testing.T) {
	documents := []MessageIndexDocument{
		{Message: store.MessageRecord{ID: 1, UserID: 6, Subject: "alpha"}},
		{Message: store.MessageRecord{ID: 2, UserID: 6, Subject: "beta"}},
	}
	tenants, err := prepareMessageIndexChunks(context.Background(), documents)
	if err != nil {
		t.Fatal(err)
	}
	index, err := openIndex(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	first, err := tenants[0].chunks[0].newBleveBatch(index)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tenants[0].chunks[0].newBleveBatch(index)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("newBleveBatch reused a Bleve batch")
	}
	if first.TotalDocsSize() == 0 || first.TotalDocsSize() != second.TotalDocsSize() {
		t.Fatalf("fresh batch sizes = [%d %d]", first.TotalDocsSize(), second.TotalDocsSize())
	}
}

func TestPreparedChunkDiagnosticsExcludeRawInputContent(t *testing.T) {
	privateSubject := "private-subject-need-not-appear"
	privateBody := "private-body-need-not-appear"
	documents := []MessageIndexDocument{{Message: store.MessageRecord{
		ID: 414, UserID: 19, AccountID: 2, MailboxID: 5,
		Subject: privateSubject, BodyText: privateBody,
	}}}
	tenants, err := prepareMessageIndexChunks(context.Background(), documents)
	if err != nil {
		t.Fatal(err)
	}
	chunk := tenants[0].chunks[0]
	diagnosticText := fmt.Sprintf("%+v", chunk.diagnostics("index-batch"))
	for _, private := range []string{privateSubject, privateBody} {
		if strings.Contains(diagnosticText, private) {
			t.Fatalf("diagnostics exposed raw content %q: %s", private, diagnosticText)
		}
	}
	for _, want := range []string{"UserID:19", "AccountID:2", "MailboxID:5", "Documents:1", "DocumentIDs:[414]"} {
		if !strings.Contains(diagnosticText, want) {
			t.Fatalf("diagnostics missing %q: %s", want, diagnosticText)
		}
	}
	wantProjectedBytes := projectedDocumentStringBytes(chunk.documents[0].projection)
	if chunk.projectedBytes == 0 || chunk.projectedBytes != wantProjectedBytes {
		t.Fatalf("projected byte count = %d, want %d", chunk.projectedBytes, wantProjectedBytes)
	}
}

func preparedMessageIDs(chunk preparedMessageIndexChunk) []int64 {
	ids := make([]int64, 0, len(chunk.documents))
	for _, document := range chunk.documents {
		ids = append(ids, document.messageID)
	}
	return ids
}
