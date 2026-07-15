package imapclient

import (
	"context"
	"testing"

	"github.com/emersion/go-imap"
)

type fakeUIDSnapshotClient struct {
	status   *imap.MailboxStatus
	uids     []uint32
	selected string
	readOnly bool
	criteria *imap.SearchCriteria
}

func (f *fakeUIDSnapshotClient) Select(mailbox string, readOnly bool) (*imap.MailboxStatus, error) {
	f.selected = mailbox
	f.readOnly = readOnly
	return f.status, nil
}

func (f *fakeUIDSnapshotClient) UidSearch(criteria *imap.SearchCriteria) ([]uint32, error) {
	f.criteria = criteria
	return append([]uint32(nil), f.uids...), nil
}

func TestSnapshotMailboxUIDsBindsSearchToSelectedUIDValidity(t *testing.T) {
	client := &fakeUIDSnapshotClient{
		status: &imap.MailboxStatus{UidValidity: 812, UidNext: 14},
		uids:   []uint32{2, 8, 13},
	}
	snapshot, err := snapshotMailboxUIDs(context.Background(), client, " Archive ")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.UIDValidity != 812 || snapshot.UIDNext != 14 || len(snapshot.UIDs) != 3 || snapshot.UIDs[2] != 13 {
		t.Fatalf("snapshot = %+v, want UIDs [2 8 13], UIDNEXT 14 in generation 812", snapshot)
	}
	if client.selected != "Archive" || !client.readOnly {
		t.Fatalf("selected mailbox = %q readOnly=%t, want Archive read-only", client.selected, client.readOnly)
	}
	if client.criteria == nil || client.criteria.Uid == nil || client.criteria.Uid.String() != "1:13" {
		t.Fatalf("UID search criteria = %+v, want snapshot range 1:13", client.criteria)
	}
}

func TestSnapshotMailboxUIDsRejectsMissingGeneration(t *testing.T) {
	client := &fakeUIDSnapshotClient{status: &imap.MailboxStatus{UidNext: 43}, uids: []uint32{42}}
	if _, err := snapshotMailboxUIDs(context.Background(), client, "Archive"); err == nil {
		t.Fatal("snapshot accepted a selected mailbox without UIDVALIDITY")
	}
	if client.criteria != nil {
		t.Fatal("snapshot searched UIDs without a valid selected generation")
	}
}

func TestSnapshotMailboxUIDsRejectsMissingCutoff(t *testing.T) {
	client := &fakeUIDSnapshotClient{status: &imap.MailboxStatus{UidValidity: 812}, uids: []uint32{42}}
	if _, err := snapshotMailboxUIDs(context.Background(), client, "Archive"); err == nil {
		t.Fatal("snapshot accepted a selected mailbox without UIDNEXT")
	}
	if client.criteria != nil {
		t.Fatal("snapshot searched UIDs without a safe upper bound")
	}
}

func TestSnapshotMailboxUIDsHandlesEmptyMailboxWithoutDynamicSearch(t *testing.T) {
	client := &fakeUIDSnapshotClient{status: &imap.MailboxStatus{UidValidity: 812, UidNext: 1}}
	snapshot, err := snapshotMailboxUIDs(context.Background(), client, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.UIDValidity != 812 || snapshot.UIDNext != 1 || len(snapshot.UIDs) != 0 {
		t.Fatalf("empty snapshot = %+v", snapshot)
	}
	if client.criteria != nil {
		t.Fatal("empty mailbox issued a dynamic UID search")
	}
}
