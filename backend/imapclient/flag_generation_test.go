package imapclient

import (
	"context"
	"testing"

	"github.com/emersion/go-imap"
)

type fakeGenerationFlagClient struct {
	status      *imap.MailboxStatus
	storeCalls  int
	searchCalls int
	uids        []uint32
}

func (f *fakeGenerationFlagClient) Select(string, bool) (*imap.MailboxStatus, error) {
	return f.status, nil
}

func (f *fakeGenerationFlagClient) UidStore(*imap.SeqSet, imap.StoreItem, interface{}, chan *imap.Message) error {
	f.storeCalls++
	return nil
}

func (f *fakeGenerationFlagClient) UidSearch(*imap.SearchCriteria) ([]uint32, error) {
	f.searchCalls++
	return append([]uint32(nil), f.uids...), nil
}

func TestSetFlagWithUIDValiditySkipsSTOREAfterGenerationChange(t *testing.T) {
	client := &fakeGenerationFlagClient{status: &imap.MailboxStatus{UidValidity: 2}}
	applied, err := setFlagWithUIDValidity(context.Background(), client, "INBOX", 42, true, 1, imap.SeenFlag, "seen")
	if err != nil {
		t.Fatal(err)
	}
	if applied || client.storeCalls != 0 {
		t.Fatalf("applied=%t STORE calls=%d, want false/0 after UIDVALIDITY change", applied, client.storeCalls)
	}
}

func TestFlagUIDsWithUIDValiditySkipsSearchAfterGenerationChange(t *testing.T) {
	client := &fakeGenerationFlagClient{status: &imap.MailboxStatus{UidValidity: 2}, uids: []uint32{42}}
	uids, matched, err := flagUIDsWithUIDValidity(context.Background(), client, "INBOX", 1, imap.SeenFlag, "seen")
	if err != nil {
		t.Fatal(err)
	}
	if matched || len(uids) != 0 || client.searchCalls != 0 {
		t.Fatalf("matched=%t UIDs=%v SEARCH calls=%d, want false/empty/0 after UIDVALIDITY change", matched, uids, client.searchCalls)
	}
}
