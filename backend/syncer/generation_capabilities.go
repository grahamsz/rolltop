// File overview: Optional IMAP capabilities that bind UID work to one mailbox generation.

package syncer

import (
	"context"

	"rolltop/backend/store"
)

// UIDValidityMailboxFetcher performs SELECT, generation validation, and body
// fetches on one IMAP session. Production sync uses this capability so a
// UIDVALIDITY reset between STATUS and SELECT cannot store reused UIDs.
type UIDValidityMailboxFetcher interface {
	FetchMailboxWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, afterUID, expectedUIDValidity uint32, handle func(FetchedMessage) error) error
	FetchUIDsWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, uids []uint32, expectedUIDValidity uint32, handle func(FetchedMessage) error) error
}

// UIDValidityFlagFetcher performs SELECT, generation validation, and UID STORE
// on one IMAP session. A false applied result is a safe generation mismatch:
// callers leave the local mutation pending for a later, refreshed sync.
type UIDValidityFlagFetcher interface {
	SetSeenWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, seen bool, expectedUIDValidity uint32) (applied bool, err error)
	SetFlaggedWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, flagged bool, expectedUIDValidity uint32) (applied bool, err error)
}

// UIDValidityFlagReader binds flag SEARCH results to the UIDVALIDITY returned
// by the same SELECT. A false matched result means callers must leave local
// flags unchanged until mailbox generation reset/refresh completes.
type UIDValidityFlagReader interface {
	SeenUIDsWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, expectedUIDValidity uint32) (uids []uint32, matched bool, err error)
	FlaggedUIDsWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, expectedUIDValidity uint32) (uids []uint32, matched bool, err error)
}
