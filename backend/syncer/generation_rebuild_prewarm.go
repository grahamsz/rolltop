// File overview: Newest-message prewarming for crash-resumable mailbox generation rebuilds.

package syncer

import (
	"context"
	"fmt"
	"sort"
	"time"

	"rolltop/backend/store"
)

const (
	mailboxGenerationPrewarmLimit            = 200
	mailboxGenerationPrewarmPageSize         = 50
	mailboxGenerationRecoveryBatchSize       = 500
	mailboxGenerationRecoveryRefreshInterval = 30 * time.Second
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
) (MailboxUIDSnapshot, error, error) {
	snapshotFetcher, ok := s.Fetcher.(MailboxUIDSnapshotFetcher)
	if !ok {
		return MailboxUIDSnapshot{}, nil, nil
	}
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
	previewUIDs := snapshot.UIDs
	if len(previewUIDs) > mailboxGenerationPrewarmLimit {
		previewUIDs = previewUIDs[len(previewUIDs)-mailboxGenerationPrewarmLimit:]
	}
	if len(previewUIDs) == 0 {
		return snapshot, nil, nil
	}

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
		fetchErr := s.fetchUIDsForGeneration(ctx, account, mailbox.Name, phase, expectedUIDValidity, func(item FetchedMessage) error {
			err := handle(item)
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

// fetchMailboxGenerationSnapshotInBatches processes one immutable UID snapshot
// in bounded requests. Refresh runs only between requests, never from inside an
// active IMAP body callback, so production fetchers do not need reentrant
// connections. A final refresh is mandatory before the caller may remove the
// generation marker.
func (s *Service) fetchMailboxGenerationSnapshotInBatches(
	ctx context.Context,
	account store.MailAccount,
	mailbox store.Mailbox,
	afterUID, expectedUIDValidity uint32,
	snapshot MailboxUIDSnapshot,
	handle func(FetchedMessage) error,
	refresh func(final bool) error,
) error {
	uids := snapshot.UIDs
	first := sort.Search(len(uids), func(i int) bool { return uids[i] > afterUID })
	uids = uids[first:]
	lastRefresh := s.mailboxGenerationRecoveryTime()
	for start := 0; start < len(uids); start += mailboxGenerationRecoveryBatchSize {
		end := start + mailboxGenerationRecoveryBatchSize
		if end > len(uids) {
			end = len(uids)
		}
		if err := s.fetchUIDsForGeneration(ctx, account, mailbox.Name, uids[start:end],
			expectedUIDValidity, handle); err != nil {
			return err
		}
		if end < len(uids) && s.mailboxGenerationRecoveryTime().Sub(lastRefresh) >= mailboxGenerationRecoveryRefreshInterval {
			if err := refresh(false); err != nil {
				return err
			}
			lastRefresh = s.mailboxGenerationRecoveryTime()
		}
	}
	return refresh(true)
}

func (s *Service) mailboxGenerationRecoveryTime() time.Time {
	if s != nil && s.generationRecoveryNow != nil {
		return s.generationRecoveryNow()
	}
	return time.Now()
}
