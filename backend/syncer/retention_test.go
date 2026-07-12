package syncer

import (
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestShouldNotifyNewMailOnlyForCurrentIncrementalInboxArrivals(t *testing.T) {
	now := time.Now().UTC()
	inbox := store.Mailbox{Name: "INBOX", Role: "inbox"}
	previouslyEmptyInbox := store.Mailbox{
		Name: "INBOX", Role: "inbox", StatusCheckedAt: now.Add(-time.Minute),
		RemoteMessageCount: 0, RemoteUIDNext: 1,
	}
	initiallyPopulatedInbox := store.Mailbox{
		Name: "INBOX", Role: "inbox", StatusCheckedAt: now.Add(-time.Minute),
		RemoteMessageCount: 10, RemoteUIDNext: 11,
	}
	archive := store.Mailbox{Name: "Archive", Role: "archive"}
	current := FetchedMessage{UID: 11, InternalDate: now}
	delayed := FetchedMessage{UID: 12, InternalDate: now.Add(-time.Hour)}
	historical := FetchedMessage{UID: 9, InternalDate: now}

	if shouldNotifyNewMail(inbox, 0, current) {
		t.Fatal("initial INBOX backfill was treated as new mail")
	}
	if shouldNotifyNewMail(initiallyPopulatedInbox, 0, current) {
		t.Fatal("discovered initial INBOX backfill was treated as new mail")
	}
	if !shouldNotifyNewMail(previouslyEmptyInbox, 0, FetchedMessage{UID: 1, InternalDate: now}) {
		t.Fatal("first arrival after an empty INBOX status was not treated as new mail")
	}
	if shouldNotifyNewMail(archive, 10, current) {
		t.Fatal("non-INBOX arrival was treated as new mail")
	}
	if shouldNotifyNewMail(inbox, 10, historical) {
		t.Fatal("historical INBOX repair was treated as new mail")
	}
	if !shouldNotifyNewMail(inbox, 10, current) {
		t.Fatal("current incremental INBOX arrival was not treated as new mail")
	}
	if !shouldNotifyNewMail(inbox, 10, delayed) {
		t.Fatal("delayed incremental INBOX arrival was not treated as new mail")
	}
	if !shouldCancelSnoozeForNewMessage(archive, 10, current) {
		t.Fatal("incremental non-INBOX arrival would not cancel a snooze")
	}
	if shouldCancelSnoozeForNewMessage(archive, 0, current) {
		t.Fatal("initial non-INBOX backfill would cancel a snooze")
	}
	if !shouldCancelSnoozeForNewMessage(previouslyEmptyInbox, 0, FetchedMessage{UID: 1, InternalDate: now}) {
		t.Fatal("first arrival after an empty mailbox would not cancel a snooze")
	}
}
