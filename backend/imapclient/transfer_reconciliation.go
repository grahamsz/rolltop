// File overview: Authoritative destination snapshots for crash-safe COPY reconciliation.

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

var (
	_ syncer.MailboxAppendBoundaryFetcher = (*Fetcher)(nil)
	_ syncer.ExactMessageMatchFetcher     = (*Fetcher)(nil)
)

const maxTransferReconciliationCandidates = 100

// SnapshotMailboxAppendBoundary captures UIDVALIDITY and UIDNEXT from one
// selected destination session before a COPY command is claimed.
func (f *Fetcher) SnapshotMailboxAppendBoundary(ctx context.Context, account store.MailAccount, mailbox string) (syncer.MailboxAppendBoundary, error) {
	if err := ctx.Err(); err != nil {
		return syncer.MailboxAppendBoundary{}, err
	}
	mailbox = strings.TrimSpace(mailbox)
	if mailbox == "" {
		return syncer.MailboxAppendBoundary{}, errors.New("append boundary requires a mailbox")
	}
	c, err := f.login(account)
	if err != nil {
		return syncer.MailboxAppendBoundary{}, err
	}
	defer c.Logout()
	selected, err := c.Select(mailbox, true)
	if err != nil {
		return syncer.MailboxAppendBoundary{}, fmt.Errorf("select mailbox %q for append boundary: %w", mailbox, err)
	}
	if selected == nil || selected.UidValidity == 0 || selected.UidNext == 0 {
		return syncer.MailboxAppendBoundary{}, fmt.Errorf("selected mailbox %q returned no UIDVALIDITY or UIDNEXT", mailbox)
	}
	return syncer.MailboxAppendBoundary{UIDValidity: selected.UidValidity, UIDNext: selected.UidNext}, nil
}

// SnapshotExactMessageMatches searches candidates by Message-ID and confirms
// them by exact or canonical raw bytes under the same selected generation. For
// valid messages without Message-ID, it scans only the bounded UID interval
// created after the caller's pre-dispatch append snapshot.
func (f *Fetcher) SnapshotExactMessageMatches(ctx context.Context, account store.MailAccount, mailbox, messageID string, raw []byte, minimumUID uint32) (syncer.ExactMessageMatchSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return syncer.ExactMessageMatchSnapshot{}, err
	}
	mailbox = strings.TrimSpace(mailbox)
	messageID = strings.TrimSpace(messageID)
	if mailbox == "" || len(raw) == 0 {
		return syncer.ExactMessageMatchSnapshot{}, errors.New("exact message match requires mailbox and raw bytes")
	}
	if messageID == "" && minimumUID == 0 {
		return syncer.ExactMessageMatchSnapshot{}, errors.New("exact raw message match requires a pre-dispatch minimum UID")
	}
	c, err := f.login(account)
	if err != nil {
		return syncer.ExactMessageMatchSnapshot{}, err
	}
	defer c.Logout()
	selected, err := c.Select(mailbox, true)
	if err != nil {
		return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("select mailbox %q for exact message match: %w", mailbox, err)
	}
	if selected == nil || selected.UidValidity == 0 || selected.UidNext == 0 {
		return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("selected mailbox %q returned no UIDVALIDITY or UIDNEXT", mailbox)
	}
	snapshot := syncer.ExactMessageMatchSnapshot{
		UIDValidity: selected.UidValidity,
		UIDNext:     selected.UidNext,
	}
	criteria, search, err := transferReconciliationSearchCriteria(messageID, minimumUID, selected.UidNext)
	if err != nil {
		return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("mailbox %q: %w", mailbox, err)
	}
	if !search {
		if !selectedMailboxGenerationMatches(c.Mailbox(), snapshot.UIDValidity) {
			return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("mailbox %q generation changed during exact COPY reconciliation", mailbox)
		}
		return snapshot, nil
	}
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("search exact COPY candidates in mailbox %q: %w", mailbox, err)
	}
	uids = normalizeUIDList(uids)
	if messageID == "" {
		if err := validatePostSnapshotCandidateUIDs(uids, minimumUID, snapshot.UIDNext); err != nil {
			return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("mailbox %q: %w", mailbox, err)
		}
	}
	uids, err = boundedTransferReconciliationCandidates(uids)
	if err != nil {
		return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("mailbox %q: %w", mailbox, err)
	}
	if len(uids) == 0 {
		if !selectedMailboxGenerationMatches(c.Mailbox(), snapshot.UIDValidity) {
			return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("mailbox %q generation changed during exact COPY reconciliation", mailbox)
		}
		return snapshot, nil
	}
	snapshot.CandidateUIDs = append(snapshot.CandidateUIDs, uids...)
	if err := f.fetchUIDs(ctx, c, mailbox, uids, func(candidate syncer.FetchedMessage) error {
		if appendRawMatches(raw, candidate.Raw) {
			snapshot.MatchingUIDs = append(snapshot.MatchingUIDs, candidate.UID)
		}
		return nil
	}); err != nil {
		return syncer.ExactMessageMatchSnapshot{}, err
	}
	if !selectedMailboxGenerationMatches(c.Mailbox(), snapshot.UIDValidity) {
		return syncer.ExactMessageMatchSnapshot{}, fmt.Errorf("mailbox %q generation changed during exact COPY reconciliation", mailbox)
	}
	return snapshot, nil
}

func transferReconciliationSearchCriteria(messageID string, minimumUID, currentUIDNext uint32) (*imap.SearchCriteria, bool, error) {
	criteria := imap.NewSearchCriteria()
	if messageID = strings.TrimSpace(messageID); messageID != "" {
		criteria.Header.Set("Message-ID", messageID)
		return criteria, true, nil
	}
	if minimumUID == 0 || currentUIDNext == 0 {
		return nil, false, errors.New("exact raw reconciliation requires nonzero UID boundaries")
	}
	if currentUIDNext < minimumUID {
		return nil, false, fmt.Errorf("current UIDNEXT %d precedes pre-dispatch UID %d; mailbox generation is ambiguous", currentUIDNext, minimumUID)
	}
	if currentUIDNext == minimumUID {
		return nil, false, nil
	}
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddRange(minimumUID, currentUIDNext-1)
	return criteria, true, nil
}

func validatePostSnapshotCandidateUIDs(uids []uint32, minimumUID, currentUIDNext uint32) error {
	for _, uid := range uids {
		if uid < minimumUID || uid >= currentUIDNext {
			return fmt.Errorf("UID %d falls outside post-snapshot range [%d,%d)", uid, minimumUID, currentUIDNext)
		}
	}
	return nil
}

func selectedMailboxGenerationMatches(current *imap.MailboxStatus, expectedUIDValidity uint32) bool {
	return current != nil && current.UidValidity != 0 && current.UidValidity == expectedUIDValidity
}

func boundedTransferReconciliationCandidates(uids []uint32) ([]uint32, error) {
	if len(uids) > maxTransferReconciliationCandidates {
		return nil, fmt.Errorf("%d Message-ID candidates make exact COPY reconciliation ambiguous above %d",
			len(uids), maxTransferReconciliationCandidates)
	}
	return uids, nil
}
