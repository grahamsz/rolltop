package syncer

import (
	"context"
	"errors"

	"rolltop/backend/store"
)

type moveOutcomeUnknownError struct {
	err error
}

type appendAppliedError struct {
	err error
}

type appendOutcomeUnknownError struct {
	err error
}

func (e *moveOutcomeUnknownError) Error() string {
	return "IMAP move outcome is unknown: " + e.err.Error()
}

func (e *moveOutcomeUnknownError) Unwrap() error {
	return e.err
}

// MoveOutcomeUnknown marks a transport error returned after an IMAP MOVE was
// dispatched, when the server may have applied the mutation before disconnecting.
func MoveOutcomeUnknown(err error) error {
	if err == nil || IsMoveOutcomeUnknown(err) {
		return err
	}
	return &moveOutcomeUnknownError{err: err}
}

// IsMoveOutcomeUnknown reports whether a failed MOVE must be reconciled rather
// than immediately recorded as a definitive remote failure.
func IsMoveOutcomeUnknown(err error) bool {
	var unknown *moveOutcomeUnknownError
	return errors.As(err, &unknown)
}

func (e *appendAppliedError) Error() string {
	return "IMAP append succeeded but its destination UID is unavailable: " + e.err.Error()
}

func (e *appendAppliedError) Unwrap() error {
	return e.err
}

// AppendApplied marks an error from work performed after the IMAP server
// accepted APPEND. The remote copy exists even though its UID could not be
// confirmed, so callers must retain transfer correlation before returning.
func AppendApplied(err error) error {
	if err == nil || IsAppendApplied(err) {
		return err
	}
	return &appendAppliedError{err: err}
}

// IsAppendApplied reports whether APPEND itself completed successfully before
// a later SELECT, SEARCH, or FETCH step failed.
func IsAppendApplied(err error) bool {
	var applied *appendAppliedError
	return errors.As(err, &applied)
}

func (e *appendOutcomeUnknownError) Error() string {
	return "IMAP append outcome is unknown: " + e.err.Error()
}

func (e *appendOutcomeUnknownError) Unwrap() error {
	return e.err
}

// AppendOutcomeUnknown marks a transport failure while APPEND was in flight.
// The server may have stored the message, but no tagged response was received.
func AppendOutcomeUnknown(err error) error {
	if err == nil || IsAppendOutcomeUnknown(err) {
		return err
	}
	return &appendOutcomeUnknownError{err: err}
}

// IsAppendOutcomeUnknown reports whether an APPEND must remain pending and
// fail open during later arrival classification.
func IsAppendOutcomeUnknown(err error) bool {
	var unknown *appendOutcomeUnknownError
	return errors.As(err, &unknown)
}

// MoveReceipt identifies the destination copy created by a successful IMAP
// MOVE when the server supplies a UIDPLUS COPYUID response code.
type MoveReceipt struct {
	DestinationUIDValidity uint32
	DestinationUID         uint32
}

// MoveReceiptFetcher is an optional Fetcher capability. A nil receipt means
// the MOVE succeeded but the server did not return usable COPYUID metadata.
type MoveReceiptFetcher interface {
	MoveMessageWithReceipt(ctx context.Context, account store.MailAccount, sourceMailbox string, destMailbox string, uid uint32, expectedSourceUIDValidity uint32) (*MoveReceipt, error)
}

// UIDExistenceFetcher is an optional Fetcher capability for bounded checks of
// a single UID in a mailbox.
type UIDExistenceFetcher interface {
	UIDExists(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (bool, error)
}

// UIDValidityExistenceFetcher is the preferred existence-check capability. It
// returns UIDVALIDITY from the same selected mailbox session used for the UID
// search, so callers cannot mistake a UID from another mailbox generation for
// evidence that a message was moved.
type UIDValidityExistenceFetcher interface {
	UIDExistsWithValidity(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (exists bool, uidValidity uint32, err error)
}

// BatchUIDValidityExistenceFetcher checks an exact set of UIDs under one
// read-only SELECT. ExistingUIDsWithValidity returns the requested UIDs that
// still exist plus UIDVALIDITY from that same mailbox session.
type BatchUIDValidityExistenceFetcher interface {
	ExistingUIDsWithValidity(ctx context.Context, account store.MailAccount, mailbox string, uids []uint32) (existingUIDs []uint32, uidValidity uint32, err error)
}

// MailboxUIDSnapshot binds a mailbox UID listing to UIDVALIDITY and UIDNEXT from
// the same read-only SELECT. UIDNext is an exclusive upper bound: local rows at
// or above it may have been inserted after the UID search and cannot be reconciled.
type MailboxUIDSnapshot struct {
	UIDs        []uint32
	UIDValidity uint32
	UIDNext     uint32
}

// MailboxUIDSnapshotFetcher is an optional Fetcher capability used by
// reconciliation. Implementations must obtain all fields from one selected
// mailbox session.
type MailboxUIDSnapshotFetcher interface {
	SnapshotMailboxUIDs(ctx context.Context, account store.MailAccount, mailbox string) (MailboxUIDSnapshot, error)
}

// MailboxAppendBoundary is an authoritative destination generation and
// exclusive UID boundary captured by one read-only SELECT before APPEND.
type MailboxAppendBoundary struct {
	UIDValidity uint32
	UIDNext     uint32
}

// MailboxAppendBoundaryFetcher captures the durable pre-command boundary used
// to reconcile a COPY whose APPEND outcome is unknown.
type MailboxAppendBoundaryFetcher interface {
	SnapshotMailboxAppendBoundary(ctx context.Context, account store.MailAccount, mailbox string) (MailboxAppendBoundary, error)
}

// ExactMessageMatchSnapshot reports exact raw/canonical matches found under
// one selected destination generation. MatchingUIDs includes old matches so a
// preexisting identical copy can be treated as ambiguous instead of retried.
type ExactMessageMatchSnapshot struct {
	UIDValidity   uint32
	UIDNext       uint32
	CandidateUIDs []uint32
	MatchingUIDs  []uint32
}

// ExactMessageMatchFetcher performs a Message-ID candidate search and confirms
// matches by RFC822 bytes. When Message-ID is absent, minimumUID bounds the raw
// scan to UIDs created after the pre-dispatch destination snapshot.
type ExactMessageMatchFetcher interface {
	SnapshotExactMessageMatches(ctx context.Context, account store.MailAccount, mailbox, messageID string, raw []byte, minimumUID uint32) (ExactMessageMatchSnapshot, error)
}
