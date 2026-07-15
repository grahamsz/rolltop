package syncer

import (
	"context"
	"strings"
	"testing"
)

func TestCopyRejectsPrunedBlobFetchFromReusedUIDGeneration(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher:  fixture.fetcher,
		raw:              []byte("Message-ID: <reused-uid@example.test>\r\nSubject: Wrong generation\r\n\r\nwrong body\r\n"),
		fetchUIDValidity: moveTestSourceUIDValidity + 1,
	}
	fixture.service.Fetcher = fetcher

	err := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("CopyMessage error = %v, want reused-UID generation rejection", err)
	}
	if fetcher.appendCalls != 0 {
		t.Fatalf("reused-UID raw content was appended %d times", fetcher.appendCalls)
	}
	var transfers int
	if err := fixture.store.DB().QueryRow(`SELECT COUNT(*) FROM message_transfers WHERE user_id = ?`, fixture.userID).Scan(&transfers); err != nil {
		t.Fatal(err)
	}
	if transfers != 0 {
		t.Fatalf("reused-UID copy staged %d transfer rows", transfers)
	}
}
