// File overview: Newest-message prewarming for crash-resumable mailbox generation rebuilds.

package syncer

import (
	"context"
	"fmt"
	"sort"

	"rolltop/backend/store"
)

const (
	// One newest page is enough to make the mailbox immediately useful. Older
	// history belongs to the bounded ascending recovery turn; prewarming four
	// pages made the supposedly bounded scheduler spend minutes before checkpointing.
	mailboxGenerationPrewarmLimit      = 50
	mailboxGenerationPrewarmPageSize   = 50
	mailboxGenerationRecoveryBatchSize = 500
)

// prewarmPendingMailboxGeneration fetches a bounded newest UID window without
// advancing the ascending rebuild checkpoint. The normal generation-bound fetch
// still proves and processes every earlier UID before it can complete. The first
// error is fatal local/callback work; the second is a best-effort preview fetch.
func (s *Service) prewarmPendingMailboxGeneration(
	ctx context.Context,
	userID int64,
	account store.MailAccount,
	mailbox store.Mailbox,
	expectedUIDValidity uint32,
	handle func(FetchedMessage) error,
	snapshotReady func(MailboxUIDSnapshot) error,
) (MailboxUIDSnapshot, error, error) {
	snapshotFetcher, ok := s.Fetcher.(MailboxUIDSnapshotFetcher)
	if !ok {
		return MailboxUIDSnapshot{}, nil, nil
	}
	generationRecoveryPhase(ctx, "imap-uid-snapshot", "")
	snapshot, err := snapshotFetcher.SnapshotMailboxUIDs(ctx, account, mailbox.Name)
	if err != nil {
		return MailboxUIDSnapshot{}, nil, err
	}
	if snapshot.UIDValidity != expectedUIDValidity {
		return MailboxUIDSnapshot{}, nil, fmt.Errorf("mailbox %q prewarm UIDVALIDITY is %d, expected %d: %w",
			mailbox.Name, snapshot.UIDValidity, expectedUIDValidity, store.ErrMailboxGenerationChanged)
	}
	if snapshot.UIDNext == 0 {
		return MailboxUIDSnapshot{}, nil, fmt.Errorf("mailbox %q prewarm snapshot is missing UIDNEXT", mailbox.Name)
	}

	uids := append([]uint32(nil), snapshot.UIDs...)
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	unique := uids[:0]
	var previous uint32
	for _, uid := range uids {
		if uid == 0 || uid >= snapshot.UIDNext {
			return MailboxUIDSnapshot{}, nil, fmt.Errorf("mailbox %q prewarm snapshot contains UID %d outside UIDNEXT %d",
				mailbox.Name, uid, snapshot.UIDNext)
		}
		if len(unique) > 0 && uid == previous {
			continue
		}
		unique = append(unique, uid)
		previous = uid
	}
	snapshot.UIDs = append([]uint32(nil), unique...)
	generationRecoverySetTotal(ctx, len(snapshot.UIDs))
	if snapshotReady != nil {
		if err := snapshotReady(snapshot); err != nil {
			return snapshot, err, nil
		}
	}
	previewUIDs := snapshot.UIDs
	if len(previewUIDs) > mailboxGenerationPrewarmLimit {
		previewUIDs = previewUIDs[len(previewUIDs)-mailboxGenerationPrewarmLimit:]
	}
	if len(previewUIDs) == 0 {
		return snapshot, nil, nil
	}

	generationRecoveryPhase(ctx, "sqlite-local-uids", "")
	localUIDs, err := s.Store.MessageUIDsForMailbox(ctx, userID, account.ID, mailbox.ID)
	if err != nil {
		return snapshot, err, nil
	}
	local := make(map[uint32]struct{}, len(localUIDs))
	for _, uid := range localUIDs {
		local[uid] = struct{}{}
	}
	missing := make([]uint32, 0, len(previewUIDs))
	for _, uid := range previewUIDs {
		if _, exists := local[uid]; !exists {
			missing = append(missing, uid)
		}
	}
	if len(missing) == 0 {
		return snapshot, nil, nil
	}
	// Production sparse fetches stream each request in ascending UID order. Fetch
	// the highest page separately so the mailbox UI can render its newest page
	// before the rest of the bounded preview arrives.
	newestStart := len(missing) - mailboxGenerationPrewarmPageSize
	if newestStart < 0 {
		newestStart = 0
	}
	phases := [][]uint32{missing[newestStart:]}
	if newestStart > 0 {
		phases = append(phases, missing[:newestStart])
	}

	var callbackErr error
	for _, phase := range phases {
		generationRecoveryPhase(ctx, "imap-newest-fetch", "awaiting-body")
		fetchErr := s.fetchUIDsForGeneration(ctx, account, mailbox.Name, phase, expectedUIDValidity, func(item FetchedMessage) error {
			generationRecoveryStartMessage(ctx, item.UID)
			defer generationRecoveryPhase(ctx, "imap-newest-fetch", "awaiting-body")
			err := handle(item)
			if err == nil {
				generationRecoveryMessageCompleted(ctx, item.UID)
			}
			if err != nil && callbackErr == nil {
				callbackErr = err
			}
			return err
		})
		if callbackErr != nil || fetchErr != nil {
			return snapshot, callbackErr, fetchErr
		}
	}
	return snapshot, nil, nil
}

func mailboxGenerationSnapshotCompletedBefore(snapshot MailboxUIDSnapshot, afterUID uint32) int {
	return sort.Search(len(snapshot.UIDs), func(i int) bool { return snapshot.UIDs[i] > afterUID })
}

// fetchMailboxGenerationSnapshotBatch processes at most one bounded portion of
// an immutable UID snapshot. An incomplete result leaves the durable generation
// marker and ascending checkpoint in place so the runner can release the mailbox
// reservation and rotate to another account. Refresh runs only after the IMAP
// request has returned; the final refresh is mandatory before marker removal.
func (s *Service) fetchMailboxGenerationSnapshotBatch(
	ctx context.Context,
	account store.MailAccount,
	mailbox store.Mailbox,
	afterUID, expectedUIDValidity uint32,
	snapshot MailboxUIDSnapshot,
	handle func(FetchedMessage) error,
	refresh func(final bool) error,
) (bool, error) {
	uids := snapshot.UIDs
	first := sort.Search(len(uids), func(i int) bool { return uids[i] > afterUID })
	uids = uids[first:]
	end := len(uids)
	if end > mailboxGenerationRecoveryBatchSize {
		end = mailboxGenerationRecoveryBatchSize
	}
	if end > 0 {
		generationRecoveryPhase(ctx, "imap-history-fetch", "awaiting-body")
		if err := s.fetchUIDsForGeneration(ctx, account, mailbox.Name, uids[:end],
			expectedUIDValidity, func(item FetchedMessage) error {
				generationRecoveryStartMessage(ctx, item.UID)
				defer generationRecoveryPhase(ctx, "imap-history-fetch", "awaiting-body")
				err := handle(item)
				if err == nil {
					generationRecoveryMessageCompleted(ctx, item.UID)
				}
				return err
			}); err != nil {
			return false, err
		}
	}
	complete := end == len(uids)
	generationRecoveryPhase(ctx, "refresh-newest", "")
	if err := refresh(complete); err != nil {
		return false, err
	}
	return complete, nil
}
