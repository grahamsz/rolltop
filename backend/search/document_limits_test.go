package search

import (
	"strings"
	"testing"
	"unicode/utf8"

	"rolltop/backend/store"
)

func TestBuildMessageDocumentBoundsAnalyzedMessageText(t *testing.T) {
	document := buildMessageDocument(store.MessageRecord{
		ID:              1,
		UserID:          2,
		MailboxID:       3,
		Subject:         strings.Repeat("s", maxIndexedHeaderBytes+100),
		FromAddr:        strings.Repeat("f", maxIndexedHeaderBytes+100),
		ToAddr:          strings.Repeat("t", maxIndexedHeaderBytes+100),
		CCAddr:          strings.Repeat("c", maxIndexedHeaderBytes+100),
		MessageIDHeader: strings.Repeat("m", maxIndexedHeaderBytes+100),
		BodyText:        strings.Repeat("body ", maxIndexedBodyBytes),
	}, []AttachmentDoc{
		{Filename: strings.Repeat("n", maxIndexedNamesBytes), ContentType: "text/plain", Text: strings.Repeat("a", maxIndexedAttachmentsBytes)},
		{Filename: "second.txt", ContentType: "text/plain", Text: strings.Repeat("b", maxIndexedAttachmentsBytes)},
	})

	assertBoundedDocumentField(t, document, "subject", maxIndexedHeaderBytes)
	assertBoundedDocumentField(t, document, "from", maxIndexedHeaderBytes)
	assertBoundedDocumentField(t, document, "to", maxIndexedHeaderBytes)
	assertBoundedDocumentField(t, document, "cc", maxIndexedHeaderBytes)
	assertBoundedDocumentField(t, document, "message_id", maxIndexedHeaderBytes)
	assertBoundedDocumentField(t, document, "body", maxIndexedBodyBytes)
	assertBoundedDocumentField(t, document, "attachment_names", maxIndexedNamesBytes)
	assertBoundedDocumentField(t, document, "attachment_types", maxIndexedNamesBytes)
	assertBoundedDocumentField(t, document, "attachments", maxIndexedAttachmentsBytes)
	if projected := projectedDocumentStringBytes(document); projected > maxMessageIndexProjectedDocumentBytes {
		t.Fatalf("projected document bytes=%d, reservation bound=%d", projected, maxMessageIndexProjectedDocumentBytes)
	}
}

func TestBoundedIndexTextKeepsValidUTF8(t *testing.T) {
	value := strings.Repeat("x", 7) + "é" + strings.Repeat("z", 10)
	got := boundedIndexText(value, 8)
	if !utf8.ValidString(got) {
		t.Fatalf("bounded text is not valid UTF-8: %q", got)
	}
	if len(got) > 8 {
		t.Fatalf("bounded text length=%d, want <= 8", len(got))
	}
}

func TestBoundedIndexTextSplitsPathologicalTokens(t *testing.T) {
	got := boundedIndexText(strings.Repeat("a", 3*maxIndexedTokenRunBytes+17), maxIndexedBodyBytes)
	for _, token := range strings.Fields(got) {
		if len(token) > maxIndexedTokenRunBytes {
			t.Fatalf("indexed token length=%d, want <= %d", len(token), maxIndexedTokenRunBytes)
		}
	}
}

func TestBoundedLanguageCodeCapsHookOutput(t *testing.T) {
	got := boundedLanguageCode(strings.Repeat("l", maxIndexedLanguageBytes+100))
	if len(got) != maxIndexedLanguageBytes {
		t.Fatalf("bounded language code length=%d, want %d", len(got), maxIndexedLanguageBytes)
	}
}

func assertBoundedDocumentField(t *testing.T, document map[string]any, field string, limit int) {
	t.Helper()
	value, ok := document[field].(string)
	if !ok {
		t.Fatalf("document field %q = %T, want string", field, document[field])
	}
	if len(value) > limit {
		t.Fatalf("document field %q length=%d, want <= %d", field, len(value), limit)
	}
	if !utf8.ValidString(value) {
		t.Fatalf("document field %q is not valid UTF-8", field)
	}
}
