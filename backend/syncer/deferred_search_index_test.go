package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/search"
)

func TestStoreFetchedMessageDefersAttachmentSearchExtraction(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	fixture.service.Blobs = blob.New(t.TempDir())
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	fixture.service.Search = searchService
	if !fixture.source.IncludeInSearch {
		t.Fatal("test mailbox must be visible in search")
	}

	marker := filepath.Join(t.TempDir(), "pdftotext-called")
	binDir := t.TempDir()
	pdftotext := filepath.Join(binDir, "pdftotext")
	if err := os.WriteFile(pdftotext, []byte("#!/bin/sh\n: > \"$ROLLTOP_TEST_PDF_MARKER\"\nprintf deferredattachmenttoken\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLTOP_TEST_PDF_MARKER", marker)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	raw := []byte("From: sender@example.test\r\n" +
		"To: receiver@example.test\r\n" +
		"Subject: Deferred search preparation\r\n" +
		"Date: Tue, 14 Jul 2026 12:00:00 +0000\r\n" +
		"Message-ID: <deferred-search@example.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=rolltop-deferred\r\n\r\n" +
		"--rolltop-deferred\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n" +
		"--rolltop-deferred\r\nContent-Type: application/pdf; name=note.pdf\r\n" +
		"Content-Disposition: attachment; filename=note.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nJVBERi10ZXN0\r\n" +
		"--rolltop-deferred--\r\n")
	item := FetchedMessage{
		Mailbox:      fixture.source.Name,
		UID:          74,
		UIDValidity:  uint32(fixture.source.UIDValidity),
		InternalDate: time.Date(2026, 7, 14, 12, 1, 0, 0, time.UTC),
		Raw:          raw,
	}

	msg, _, pendingIndex, err := fixture.service.storeFetchedMessage(ctx, fixture.userID,
		fixture.account, fixture.source, item, false)
	if err != nil {
		t.Fatal(err)
	}
	if pendingIndex != nil {
		t.Fatal("deferred storage prepared a search document")
	}
	if !msg.HasAttachments || !msg.AttachmentIndexedAt.IsZero() {
		t.Fatalf("deferred message attachments=%t indexed_at=%s", msg.HasAttachments, msg.AttachmentIndexedAt)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("attachment text extractor ran during deferred storage: %v", err)
	}
	attachments, err := fixture.store.ListAttachmentsForMessage(ctx, fixture.userID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "note.pdf" || attachments[0].IsInline {
		t.Fatalf("deferred attachment metadata = %+v", attachments)
	}
	hits, err := searchService.Search(ctx, fixture.userID, "deferredattachmenttoken", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("deferred search hits = %v, want none", hits)
	}

	if err := fixture.service.IndexAttachmentsForMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("deferred attachment extractor did not run: %v", err)
	}
	hits, err = searchService.Search(ctx, fixture.userID, "deferredattachmenttoken", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0] != msg.ID {
		t.Fatalf("post-index search hits = %v, want message %d", hits, msg.ID)
	}
	indexed, err := fixture.store.GetMessageForUser(ctx, fixture.userID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.AttachmentIndexedAt.IsZero() {
		t.Fatal("post-index message was not marked attachment-indexed")
	}
}
