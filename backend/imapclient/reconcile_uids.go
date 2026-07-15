// File overview: Generation-bound IMAP UID snapshots for safe reconciliation.

package imapclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-imap"

	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type uidSnapshotClient interface {
	Select(mailbox string, readOnly bool) (*imap.MailboxStatus, error)
	UidSearch(criteria *imap.SearchCriteria) ([]uint32, error)
}

var _ syncer.MailboxUIDSnapshotFetcher = (*Fetcher)(nil)

// SnapshotMailboxUIDs returns every UID and the selected mailbox UIDVALIDITY
// from one authenticated IMAP session.
func (f *Fetcher) SnapshotMailboxUIDs(ctx context.Context, account store.MailAccount, mailbox string) (syncer.MailboxUIDSnapshot, error) {
	if err := validateUIDSnapshotRequest(ctx, mailbox); err != nil {
		return syncer.MailboxUIDSnapshot{}, err
	}
	c, err := f.login(account)
	if err != nil {
		return syncer.MailboxUIDSnapshot{}, err
	}
	defer c.Logout()
	return snapshotMailboxUIDs(ctx, c, mailbox)
}

func snapshotMailboxUIDs(ctx context.Context, c uidSnapshotClient, mailbox string) (syncer.MailboxUIDSnapshot, error) {
	if err := validateUIDSnapshotRequest(ctx, mailbox); err != nil {
		return syncer.MailboxUIDSnapshot{}, err
	}
	mailbox = strings.TrimSpace(mailbox)
	status, err := c.Select(mailbox, true)
	if err != nil {
		return syncer.MailboxUIDSnapshot{}, fmt.Errorf("select mailbox %q read-only for UID reconcile: %w", mailbox, err)
	}
	if status == nil || status.UidValidity == 0 {
		return syncer.MailboxUIDSnapshot{}, fmt.Errorf("select mailbox %q returned no UIDVALIDITY for reconcile", mailbox)
	}
	if status.UidNext == 0 {
		return syncer.MailboxUIDSnapshot{}, fmt.Errorf("select mailbox %q returned no UIDNEXT for reconcile", mailbox)
	}
	if status.UidNext == 1 {
		return syncer.MailboxUIDSnapshot{UIDValidity: status.UidValidity, UIDNext: status.UidNext}, nil
	}
	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddRange(1, status.UidNext-1)
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return syncer.MailboxUIDSnapshot{}, fmt.Errorf("search UIDs in mailbox %q: %w", mailbox, err)
	}
	if err := ctx.Err(); err != nil {
		return syncer.MailboxUIDSnapshot{}, err
	}
	return syncer.MailboxUIDSnapshot{
		UIDs:        append([]uint32(nil), uids...),
		UIDValidity: status.UidValidity,
		UIDNext:     status.UidNext,
	}, nil
}

func validateUIDSnapshotRequest(ctx context.Context, mailbox string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(mailbox) == "" {
		return errors.New("UID snapshot requires a mailbox")
	}
	return nil
}
