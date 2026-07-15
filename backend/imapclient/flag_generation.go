// File overview: Same-session UIDVALIDITY validation for remote flag writes.

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

type uidValidityFlagClient interface {
	Select(mailbox string, readOnly bool) (*imap.MailboxStatus, error)
	UidStore(seqset *imap.SeqSet, item imap.StoreItem, value interface{}, ch chan *imap.Message) error
}

type uidValidityFlagSearchClient interface {
	Select(mailbox string, readOnly bool) (*imap.MailboxStatus, error)
	UidSearch(criteria *imap.SearchCriteria) ([]uint32, error)
}

var _ syncer.UIDValidityFlagFetcher = (*Fetcher)(nil)
var _ syncer.UIDValidityFlagReader = (*Fetcher)(nil)

func (f *Fetcher) SetSeenWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, seen bool, expectedUIDValidity uint32) (bool, error) {
	c, err := f.login(account)
	if err != nil {
		return false, err
	}
	defer c.Logout()
	return setFlagWithUIDValidity(ctx, c, mailbox, uid, seen, expectedUIDValidity, imap.SeenFlag, "seen")
}

func (f *Fetcher) SetFlaggedWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, flagged bool, expectedUIDValidity uint32) (bool, error) {
	c, err := f.login(account)
	if err != nil {
		return false, err
	}
	defer c.Logout()
	return setFlagWithUIDValidity(ctx, c, mailbox, uid, flagged, expectedUIDValidity, imap.FlaggedFlag, "flagged")
}

func (f *Fetcher) SeenUIDsWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, expectedUIDValidity uint32) ([]uint32, bool, error) {
	c, err := f.login(account)
	if err != nil {
		return nil, false, err
	}
	defer c.Logout()
	return flagUIDsWithUIDValidity(ctx, c, mailbox, expectedUIDValidity, imap.SeenFlag, "seen")
}

func (f *Fetcher) FlaggedUIDsWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, expectedUIDValidity uint32) ([]uint32, bool, error) {
	c, err := f.login(account)
	if err != nil {
		return nil, false, err
	}
	defer c.Logout()
	return flagUIDsWithUIDValidity(ctx, c, mailbox, expectedUIDValidity, imap.FlaggedFlag, "flagged")
}

func setFlagWithUIDValidity(ctx context.Context, c uidValidityFlagClient, mailbox string, uid uint32, enabled bool, expectedUIDValidity uint32, flag, label string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	mailbox = strings.TrimSpace(mailbox)
	if mailbox == "" || uid == 0 || expectedUIDValidity == 0 {
		return false, errors.New("generation-bound flag update requires a mailbox, UID, and UIDVALIDITY")
	}
	status, err := c.Select(mailbox, false)
	if err != nil {
		return false, fmt.Errorf("select mailbox %q read-write for %s update: %w", mailbox, label, err)
	}
	if status == nil || status.UidValidity == 0 {
		return false, fmt.Errorf("select mailbox %q returned no UIDVALIDITY for %s update", mailbox, label)
	}
	if status.UidValidity != expectedUIDValidity {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	var op imap.FlagsOp = imap.AddFlags
	if !enabled {
		op = imap.RemoveFlags
	}
	item := imap.FormatFlagsOp(op, true)
	if err := c.UidStore(seqset, item, []interface{}{flag}, nil); err != nil {
		return false, fmt.Errorf("sync %s flag mailbox %q UID %d: %w", label, mailbox, uid, err)
	}
	return true, nil
}

func flagUIDsWithUIDValidity(ctx context.Context, c uidValidityFlagSearchClient, mailbox string, expectedUIDValidity uint32, flag, label string) ([]uint32, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	mailbox = strings.TrimSpace(mailbox)
	if mailbox == "" || expectedUIDValidity == 0 {
		return nil, false, errors.New("generation-bound flag search requires a mailbox and UIDVALIDITY")
	}
	status, err := c.Select(mailbox, true)
	if err != nil {
		return nil, false, fmt.Errorf("select mailbox %q read-only for %s search: %w", mailbox, label, err)
	}
	if status == nil || status.UidValidity == 0 {
		return nil, false, fmt.Errorf("select mailbox %q returned no UIDVALIDITY for %s search", mailbox, label)
	}
	if status.UidValidity != expectedUIDValidity {
		return nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	criteria := imap.NewSearchCriteria()
	criteria.WithFlags = []string{flag}
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return nil, false, fmt.Errorf("search %s UIDs in mailbox %q: %w", label, mailbox, err)
	}
	return uids, true, nil
}
