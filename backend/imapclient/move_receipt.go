package imapclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/commands"
	"github.com/emersion/go-imap/responses"

	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

const copyUIDResponseCode imap.StatusRespCode = "COPYUID"

type moveCommandClient interface {
	Select(mailbox string, readOnly bool) (*imap.MailboxStatus, error)
	UidSearch(criteria *imap.SearchCriteria) ([]uint32, error)
	Support(capability string) (bool, error)
	Execute(command imap.Commander, handler responses.Handler) (*imap.StatusResp, error)
}

type uidSearchClient interface {
	Select(mailbox string, readOnly bool) (*imap.MailboxStatus, error)
	UidSearch(criteria *imap.SearchCriteria) ([]uint32, error)
}

var (
	_ syncer.MoveReceiptFetcher               = (*Fetcher)(nil)
	_ syncer.UIDExistenceFetcher              = (*Fetcher)(nil)
	_ syncer.UIDValidityExistenceFetcher      = (*Fetcher)(nil)
	_ syncer.BatchUIDValidityExistenceFetcher = (*Fetcher)(nil)
)

// MoveMessageWithReceipt uses IMAP MOVE for one UID and returns the destination
// UID when the server supplies a valid UIDPLUS COPYUID response code.
func (f *Fetcher) MoveMessageWithReceipt(ctx context.Context, account store.MailAccount, sourceMailbox string, destMailbox string, uid uint32, expectedSourceUIDValidity uint32) (*syncer.MoveReceipt, error) {
	if err := validateMoveRequest(ctx, sourceMailbox, destMailbox, uid, expectedSourceUIDValidity); err != nil {
		return nil, err
	}
	c, err := f.login(account)
	if err != nil {
		return nil, err
	}
	defer c.Logout()
	return moveMessageWithReceipt(ctx, c, sourceMailbox, destMailbox, uid, expectedSourceUIDValidity)
}

func moveMessageWithReceipt(ctx context.Context, c moveCommandClient, sourceMailbox string, destMailbox string, uid uint32, expectedSourceUIDValidity uint32) (*syncer.MoveReceipt, error) {
	if err := validateMoveRequest(ctx, sourceMailbox, destMailbox, uid, expectedSourceUIDValidity); err != nil {
		return nil, err
	}
	sourceMailbox = strings.TrimSpace(sourceMailbox)
	destMailbox = strings.TrimSpace(destMailbox)
	selected, err := c.Select(sourceMailbox, false)
	if err != nil {
		return nil, fmt.Errorf("select mailbox %q read-write for move: %w", sourceMailbox, err)
	}
	if selected == nil || selected.UidValidity == 0 || selected.UidValidity != expectedSourceUIDValidity {
		selectedUIDValidity := uint32(0)
		if selected != nil {
			selectedUIDValidity = selected.UidValidity
		}
		return nil, fmt.Errorf("source mailbox %q UIDVALIDITY is %d, expected %d; refresh before moving",
			sourceMailbox, selectedUIDValidity, expectedSourceUIDValidity)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddNum(uid)
	foundUIDs, err := c.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("search source mailbox %q for UID %d before move: %w", sourceMailbox, uid, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found := false
	for _, foundUID := range foundUIDs {
		if foundUID == uid {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("source mailbox %q no longer contains UID %d; refresh before moving", sourceMailbox, uid)
	}
	ok, err := c.Support("MOVE")
	if err != nil {
		return nil, fmt.Errorf("check IMAP MOVE support: %w", err)
	}
	if !ok {
		return nil, errors.New("IMAP server does not support MOVE; rolltop will not emulate move with copy/delete")
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	command := &commands.Uid{Cmd: &commands.Move{SeqSet: seqset, Mailbox: destMailbox}}
	status, err := c.Execute(command, nil)
	if err != nil {
		return nil, syncer.MoveOutcomeUnknown(fmt.Errorf("move mailbox %q UID %d to %q: %w", sourceMailbox, uid, destMailbox, err))
	}
	if status == nil {
		return nil, syncer.MoveOutcomeUnknown(fmt.Errorf("move mailbox %q UID %d to %q: IMAP connection closed before MOVE completed", sourceMailbox, uid, destMailbox))
	}
	if err := status.Err(); err != nil {
		return nil, fmt.Errorf("move mailbox %q UID %d to %q: %w", sourceMailbox, uid, destMailbox, err)
	}
	return parseMoveReceipt(status, uid), nil
}

func validateMoveRequest(ctx context.Context, sourceMailbox string, destMailbox string, uid uint32, expectedSourceUIDValidity uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(sourceMailbox) == "" || strings.TrimSpace(destMailbox) == "" || uid == 0 || expectedSourceUIDValidity == 0 {
		return errors.New("move message requires source mailbox, destination mailbox, UID, and source UIDVALIDITY")
	}
	return nil
}

func parseMoveReceipt(status *imap.StatusResp, requestedUID uint32) *syncer.MoveReceipt {
	if status == nil || !strings.EqualFold(string(status.Code), string(copyUIDResponseCode)) || len(status.Arguments) != 3 {
		return nil
	}
	uidValidityValue, ok := responseAtom(status.Arguments[0])
	if !ok {
		return nil
	}
	uidValidity, err := strconv.ParseUint(uidValidityValue, 10, 32)
	if err != nil || uidValidity == 0 {
		return nil
	}
	sourceValue, ok := responseAtom(status.Arguments[1])
	if !ok {
		return nil
	}
	destinationValue, ok := responseAtom(status.Arguments[2])
	if !ok {
		return nil
	}
	sourceUID, ok := singleStaticUID(sourceValue)
	if !ok || sourceUID != requestedUID {
		return nil
	}
	destinationUID, ok := singleStaticUID(destinationValue)
	if !ok {
		return nil
	}
	return &syncer.MoveReceipt{
		DestinationUIDValidity: uint32(uidValidity),
		DestinationUID:         destinationUID,
	}
}

func responseAtom(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case imap.RawString:
		return string(value), true
	default:
		return "", false
	}
}

func singleStaticUID(value string) (uint32, bool) {
	set, err := imap.ParseSeqSet(value)
	if err != nil || len(set.Set) != 1 {
		return 0, false
	}
	item := set.Set[0]
	if item.Start == 0 || item.Start != item.Stop {
		return 0, false
	}
	return item.Start, true
}

// UIDExists checks one UID with a bounded UID SEARCH in the selected mailbox.
func (f *Fetcher) UIDExists(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (bool, error) {
	exists, _, err := f.UIDExistsWithValidity(ctx, account, mailbox, uid)
	return exists, err
}

// UIDExistsWithValidity checks one UID and returns UIDVALIDITY from the same
// read-only SELECT used for the UID SEARCH. Closing the connection when the
// context expires bounds commands even though go-imap does not accept context.
func (f *Fetcher) UIDExistsWithValidity(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (bool, uint32, error) {
	if err := validateUIDExistsRequest(ctx, mailbox, uid); err != nil {
		return false, 0, err
	}
	existing, uidValidity, err := f.ExistingUIDsWithValidity(ctx, account, mailbox, []uint32{uid})
	if err != nil {
		return false, 0, err
	}
	for _, found := range existing {
		if found == uid {
			return true, uidValidity, nil
		}
	}
	return false, uidValidity, nil
}

// ExistingUIDsWithValidity checks an exact UID set with one login, SELECT, and
// UID SEARCH, bounded by the caller's shared batch deadline.
func (f *Fetcher) ExistingUIDsWithValidity(ctx context.Context, account store.MailAccount, mailbox string, uids []uint32) ([]uint32, uint32, error) {
	if err := validateUIDBatchRequest(ctx, mailbox, uids); err != nil {
		return nil, 0, err
	}
	bounded, err := f.boundedByContext(ctx)
	if err != nil {
		return nil, 0, err
	}
	c, err := bounded.loginByContext(ctx, account)
	if err != nil {
		return nil, 0, err
	}
	defer c.Terminate()

	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Terminate()
		case <-watchDone:
		}
	}()
	defer close(watchDone)

	existing, uidValidity, err := existingUIDsWithValidity(ctx, c, mailbox, uids)
	if err != nil {
		return nil, 0, preferContextError(ctx, err)
	}
	return existing, uidValidity, nil
}

func uidExists(ctx context.Context, c uidSearchClient, mailbox string, uid uint32) (bool, error) {
	exists, _, err := uidExistsWithValidity(ctx, c, mailbox, uid)
	return exists, err
}

func uidExistsWithValidity(ctx context.Context, c uidSearchClient, mailbox string, uid uint32) (bool, uint32, error) {
	if err := validateUIDExistsRequest(ctx, mailbox, uid); err != nil {
		return false, 0, err
	}
	existing, uidValidity, err := existingUIDsWithValidity(ctx, c, mailbox, []uint32{uid})
	if err != nil {
		return false, 0, err
	}
	for _, found := range existing {
		if found == uid {
			return true, uidValidity, nil
		}
	}
	return false, uidValidity, nil
}

func existingUIDsWithValidity(ctx context.Context, c uidSearchClient, mailbox string, uids []uint32) ([]uint32, uint32, error) {
	if err := validateUIDBatchRequest(ctx, mailbox, uids); err != nil {
		return nil, 0, err
	}
	mailbox = strings.TrimSpace(mailbox)
	status, err := c.Select(mailbox, true)
	if err != nil {
		return nil, 0, fmt.Errorf("select mailbox %q read-only for UID existence: %w", mailbox, err)
	}
	if status == nil || status.UidValidity == 0 {
		return nil, 0, fmt.Errorf("select mailbox %q returned no UIDVALIDITY", mailbox)
	}
	seqset := new(imap.SeqSet)
	requested := make(map[uint32]struct{}, len(uids))
	for _, uid := range uids {
		seqset.AddNum(uid)
		requested[uid] = struct{}{}
	}
	criteria := imap.NewSearchCriteria()
	criteria.Uid = seqset
	foundUIDs, err := c.UidSearch(criteria)
	if err != nil {
		return nil, 0, fmt.Errorf("search mailbox %q for exact UIDs: %w", mailbox, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	existing := make([]uint32, 0, len(foundUIDs))
	seen := make(map[uint32]struct{}, len(foundUIDs))
	for _, found := range foundUIDs {
		if _, ok := requested[found]; !ok {
			continue
		}
		if _, duplicate := seen[found]; !duplicate {
			existing = append(existing, found)
			seen[found] = struct{}{}
		}
	}
	return existing, status.UidValidity, nil
}

func (f *Fetcher) boundedByContext(ctx context.Context) (*Fetcher, error) {
	if f == nil {
		return nil, errors.New("UID existence check requires a fetcher")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := f.commandTimeout()
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	bounded := *f
	bounded.Timeout = timeout
	return &bounded, nil
}

func (f *Fetcher) loginByContext(ctx context.Context, account store.MailAccount) (*client.Client, error) {
	type loginResult struct {
		client *client.Client
		err    error
	}
	results := make(chan loginResult, 1)
	go func() {
		c, err := f.login(account)
		results <- loginResult{client: c, err: err}
	}()
	select {
	case result := <-results:
		if result.err != nil {
			return nil, preferContextError(ctx, result.err)
		}
		return result.client, nil
	case <-ctx.Done():
		// login itself has a bounded socket timeout but no context API. Reap and
		// close a connection that finishes after the caller's deadline.
		go func() {
			result := <-results
			if result.client != nil {
				_ = result.client.Terminate()
			}
		}()
		return nil, ctx.Err()
	}
}

func preferContextError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return err
}

func validateUIDExistsRequest(ctx context.Context, mailbox string, uid uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(mailbox) == "" || uid == 0 {
		return errors.New("UID existence check requires mailbox and UID")
	}
	return nil
}

func validateUIDBatchRequest(ctx context.Context, mailbox string, uids []uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(mailbox) == "" || len(uids) == 0 {
		return errors.New("UID existence check requires mailbox and UIDs")
	}
	for _, uid := range uids {
		if uid == 0 {
			return errors.New("UID existence check requires non-zero UIDs")
		}
	}
	return nil
}
