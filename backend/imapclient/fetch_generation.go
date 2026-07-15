// File overview: Generation-bound production mailbox body fetches.

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

var _ syncer.UIDValidityMailboxFetcher = (*Fetcher)(nil)

// FetchMailboxWithUIDValidity validates the selected mailbox generation before
// searching or emitting any message body.
func (f *Fetcher) FetchMailboxWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, afterUID, expectedUIDValidity uint32, handle func(syncer.FetchedMessage) error) error {
	if err := validateGenerationFetch(ctx, mailbox, expectedUIDValidity, handle); err != nil {
		return err
	}
	c, err := f.login(account)
	if err != nil {
		return err
	}
	defer c.Logout()

	status, err := c.Select(strings.TrimSpace(mailbox), true)
	if err != nil {
		return fmt.Errorf("select mailbox %q read-only: %w", mailbox, err)
	}
	uidValidity, err := requireSelectedUIDValidity(mailbox, status, expectedUIDValidity)
	if err != nil {
		return err
	}
	if status.Messages == 0 || status.UidNext <= afterUID+1 || afterUID == ^uint32(0) {
		return nil
	}
	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddRange(afterUID+1, 0)
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return fmt.Errorf("search new UIDs in mailbox %q after UID %d: %w", mailbox, afterUID, err)
	}
	return f.fetchUIDs(ctx, c, strings.TrimSpace(mailbox), uids, withFetchedUIDValidity(uidValidity, handle))
}

// FetchUIDsWithUIDValidity validates the selected mailbox generation before
// fetching a sparse repair set.
func (f *Fetcher) FetchUIDsWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, uids []uint32, expectedUIDValidity uint32, handle func(syncer.FetchedMessage) error) error {
	if err := validateGenerationFetch(ctx, mailbox, expectedUIDValidity, handle); err != nil {
		return err
	}
	c, err := f.login(account)
	if err != nil {
		return err
	}
	defer c.Logout()

	status, err := c.Select(strings.TrimSpace(mailbox), true)
	if err != nil {
		return fmt.Errorf("select mailbox %q read-only: %w", mailbox, err)
	}
	uidValidity, err := requireSelectedUIDValidity(mailbox, status, expectedUIDValidity)
	if err != nil {
		return err
	}
	return f.fetchUIDs(ctx, c, strings.TrimSpace(mailbox), uids, withFetchedUIDValidity(uidValidity, handle))
}

func validateGenerationFetch(ctx context.Context, mailbox string, expectedUIDValidity uint32, handle func(syncer.FetchedMessage) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(mailbox) == "" || expectedUIDValidity == 0 {
		return errors.New("generation-bound fetch requires a mailbox and UIDVALIDITY")
	}
	if handle == nil {
		return errors.New("generation-bound fetch requires a message handler")
	}
	return nil
}

func requireSelectedUIDValidity(mailbox string, status *imap.MailboxStatus, expected uint32) (uint32, error) {
	selected := uint32(0)
	if status != nil {
		selected = status.UidValidity
	}
	if selected == 0 {
		return 0, fmt.Errorf("selected mailbox %q returned no UIDVALIDITY", strings.TrimSpace(mailbox))
	}
	if selected != expected {
		return 0, fmt.Errorf("selected mailbox %q UIDVALIDITY is %d, expected %d; refresh before fetching", strings.TrimSpace(mailbox), selected, expected)
	}
	return selected, nil
}

func withFetchedUIDValidity(uidValidity uint32, handle func(syncer.FetchedMessage) error) func(syncer.FetchedMessage) error {
	return func(item syncer.FetchedMessage) error {
		item.UIDValidity = uidValidity
		return handle(item)
	}
}
