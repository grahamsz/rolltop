// File overview: Sync-side routing for generation-bound mailbox body fetches.

package syncer

import (
	"context"
	"errors"
	"fmt"

	"rolltop/backend/store"
)

func (s *Service) fetchMailboxForGeneration(ctx context.Context, account store.MailAccount, mailbox string, afterUID, expectedUIDValidity uint32, handle func(FetchedMessage) error) error {
	if expectedUIDValidity == 0 {
		return errors.New("mailbox fetch requires a known UIDVALIDITY")
	}
	bound, ok := s.Fetcher.(UIDValidityMailboxFetcher)
	if !ok {
		return errors.New("IMAP fetcher cannot prove mailbox generation for full fetch")
	}
	return bound.FetchMailboxWithUIDValidity(ctx, account, mailbox, afterUID, expectedUIDValidity,
		validateFetchedGeneration(mailbox, expectedUIDValidity, handle))
}

func (s *Service) fetchUIDsForGeneration(ctx context.Context, account store.MailAccount, mailbox string, uids []uint32, expectedUIDValidity uint32, handle func(FetchedMessage) error) error {
	if expectedUIDValidity == 0 {
		return errors.New("sparse mailbox fetch requires a known UIDVALIDITY")
	}
	bound, ok := s.Fetcher.(UIDValidityMailboxFetcher)
	if !ok {
		return errors.New("IMAP fetcher cannot prove mailbox generation for sparse repair")
	}
	return bound.FetchUIDsWithUIDValidity(ctx, account, mailbox, uids, expectedUIDValidity,
		validateFetchedGeneration(mailbox, expectedUIDValidity, handle))
}

func validateFetchedGeneration(mailbox string, expectedUIDValidity uint32, handle func(FetchedMessage) error) func(FetchedMessage) error {
	return func(item FetchedMessage) error {
		if item.UIDValidity == 0 {
			return fmt.Errorf("mailbox %q fetch returned UID %d without UIDVALIDITY", mailbox, item.UID)
		}
		if item.UIDValidity != expectedUIDValidity {
			return fmt.Errorf("mailbox %q fetch returned UIDVALIDITY %d, expected %d", mailbox, item.UIDValidity, expectedUIDValidity)
		}
		return handle(item)
	}
}
