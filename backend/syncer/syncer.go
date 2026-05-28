// File overview: Core IMAP sync service. It coordinates mailbox discovery, message fetch, blob storage, parsing, database writes, and search indexing.

package syncer

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

// MailboxInfo is the minimal mailbox discovery record returned by a Fetcher.
type MailboxInfo struct {
	Name string
}

// MailboxStatus mirrors IMAP STATUS counters used for planning and UI progress.
type MailboxStatus struct {
	Messages    uint32
	Unseen      uint32
	UIDNext     uint32
	UIDValidity uint32
}

// MailboxPlan combines a mailbox name, remote status, last local UID, and estimated pending work.
type MailboxPlan struct {
	Name    string
	Status  MailboxStatus
	LastUID uint32
	Pending int
}

// FetchedMessage is a raw message body plus IMAP metadata streamed from Fetcher to Service.
type FetchedMessage struct {
	Mailbox      string
	UID          uint32
	Date         time.Time
	InternalDate time.Time
	Size         int64
	Flags        []string
	Raw          []byte
}

// Fetcher is the narrow IMAP boundary used by sync. Keeping this interface here
// lets tests substitute fake servers while the real imapclient package handles
// protocol details and password decryption.
type Fetcher interface {
	ListMailboxes(ctx context.Context, account store.MailAccount) ([]MailboxInfo, error)
	MailboxStatus(ctx context.Context, account store.MailAccount, mailbox string) (MailboxStatus, error)
	UIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error)
	FetchMailbox(ctx context.Context, account store.MailAccount, mailbox string, afterUID uint32, handle func(FetchedMessage) error) error
	FetchMessage(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (FetchedMessage, error)
	AppendMessage(ctx context.Context, account store.MailAccount, mailbox string, raw []byte, messageID string, date time.Time) (FetchedMessage, error)
	SetSeen(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, seen bool) error
	SeenUIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error)
	SetFlagged(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, flagged bool) error
	FlaggedUIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error)
	MoveMessage(ctx context.Context, account store.MailAccount, sourceMailbox string, destMailbox string, uid uint32) error
}

// Service is the sync orchestrator. It owns no goroutine scheduling itself; the
// Runner decides when work starts, then Service performs one account/mailbox sync
// against Store, Blob, Search, and Fetcher dependencies.
type Service struct {
	Store   *store.Store
	Blobs   *blob.Store
	Search  *search.Service
	Fetcher Fetcher

	BlobRetention time.Duration
	Notify        func(userID int64)
}

const inlineMetadataSyncLimit = 10000

// SyncUser syncs every configured account for a user using each account's auto
// mailbox plan. Runner normally decomposes this into mailbox jobs, but tests and
// direct callers can still use this whole-account entrypoint.
func (s *Service) SyncUser(ctx context.Context, userID int64) (store.SyncRun, error) {
	return s.syncUser(ctx, userID, nil)
}

// SyncUserMailboxes applies the same requested folder names to every account on
// the user. It is used for global priority jobs like refreshing INBOX after a
// webhook.
func (s *Service) SyncUserMailboxes(ctx context.Context, userID int64, mailboxNames []string) (store.SyncRun, error) {
	return s.syncUser(ctx, userID, mailboxNames)
}

// SyncUserAccountMailboxes targets one IMAP server, which avoids ambiguous folder
// names when multiple accounts contain the same mailbox path.
func (s *Service) SyncUserAccountMailboxes(ctx context.Context, userID, accountID int64, mailboxNames []string) (store.SyncRun, error) {
	if s.Fetcher == nil {
		return store.SyncRun{}, errors.New("sync fetcher is not configured")
	}
	account, err := s.Store.GetMailAccountForUser(ctx, userID, accountID)
	if err != nil {
		return store.SyncRun{}, err
	}
	return s.syncAccount(ctx, userID, account, mailboxNames)
}

// DiscoverMailboxes refreshes local mailbox rows and remote STATUS counters
// without fetching message bodies. The UI uses this for folder lists and stale
// sync clues even when a full sync is not running.
func (s *Service) DiscoverMailboxes(ctx context.Context, userID int64) (int, error) {
	if s.Fetcher == nil {
		return 0, errors.New("sync fetcher is not configured")
	}
	accounts, err := s.Store.ListMailAccountsForUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	defer s.notify(userID)
	count := 0
	for _, account := range accounts {
		configured := strings.TrimSpace(account.Mailbox)
		if configured == "" {
			configured = store.DefaultMailboxPattern
		}
		names, err := s.configuredMailboxNames(ctx, account, configured)
		if err != nil {
			return count, err
		}
		for _, name := range names {
			mb, err := s.Store.GetOrCreateMailbox(ctx, userID, account.ID, name)
			if err != nil {
				return count, err
			}
			if status, err := s.Fetcher.MailboxStatus(ctx, account, mb.Name); err == nil {
				s.recordMailboxStatus(ctx, userID, mb, status)
			} else {
				log.Printf("refresh mailbox status user_id=%d account_id=%d mailbox=%s: %v", userID, account.ID, mb.Name, err)
			}
			count++
		}
	}
	return count, nil
}

// AutoMailboxNames returns the effective set of folders that an account-wide sync
// should visit. It respects per-folder sync modes and prioritizes inbox-like
// names so fresh mail is mirrored before archival folders.
func (s *Service) AutoMailboxNames(ctx context.Context, userID int64) ([]string, error) {
	accounts, err := s.Store.ListMailAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, account := range accounts {
		names, err := s.mailboxesToSync(ctx, account, nil)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			key := strings.ToLower(strings.TrimSpace(name))
			if key != "" && !seen[key] {
				seen[key] = true
				out = append(out, name)
			}
		}
	}
	return prioritizeInbox(out), nil
}

// syncUser is the common multi-account loop. It returns the first run record so
// callers have something stable to display even when the user owns several IMAP
// accounts.
func (s *Service) syncUser(ctx context.Context, userID int64, requestedMailboxes []string) (store.SyncRun, error) {
	if s.Fetcher == nil {
		return store.SyncRun{}, errors.New("sync fetcher is not configured")
	}
	accounts, err := s.Store.ListMailAccountsForUser(ctx, userID)
	if err != nil {
		return store.SyncRun{}, err
	}
	if len(accounts) == 0 {
		return store.SyncRun{}, store.ErrNotFound
	}
	var first store.SyncRun
	for _, account := range accounts {
		run, err := s.syncAccount(ctx, userID, account, requestedMailboxes)
		if first.ID == 0 {
			first = run
		}
		if err != nil {
			return first, err
		}
	}
	return first, nil
}

// syncAccount is the main incremental sync pipeline:
// 1. create a sync_runs row for UI progress,
// 2. plan folders from IMAP STATUS and last stored UID,
// 3. push pending local Seen/Flagged changes,
// 4. fetch only UIDs newer than each mailbox's last UID,
// 5. store blobs/message rows/attachments/search documents, and
// 6. reconcile lightweight metadata for small folders.
// The defer marks interrupted runs when the process context is cancelled.
func (s *Service) syncAccount(ctx context.Context, userID int64, account store.MailAccount, requestedMailboxes []string) (store.SyncRun, error) {
	run, err := s.Store.CreateSyncRun(ctx, userID, account.ID)
	if err != nil {
		return store.SyncRun{}, err
	}

	progress := store.SyncProgress{}
	status := "ok"
	errText := ""
	defer func() {
		if ctx.Err() != nil && (status != "ok" || progress.MailboxesDone < progress.MailboxesTotal) {
			status = "interrupted"
			errText = "Server stopped before this sync finished."
		}
		if err := s.Store.FinishSyncRun(context.Background(), userID, run.ID, status, progress, errText); err != nil {
			log.Printf("finish sync run user_id=%d run_id=%d: %v", userID, run.ID, err)
		}
		s.notify(userID)
	}()

	mailboxNames, err := s.mailboxesToSync(ctx, account, requestedMailboxes)
	if err != nil {
		status = "failed"
		errText = err.Error()
		return run, err
	}
	lastUIDs, err := s.Store.LastUIDs(ctx, userID, account.ID)
	if err != nil {
		status = "failed"
		errText = err.Error()
		return run, err
	}
	plan := s.planMailboxes(ctx, account, mailboxNames, lastUIDs)
	requestedSet := requestedMailboxSet(requestedMailboxes)
	progress.MailboxesTotal = len(plan)
	for _, item := range plan {
		progress.MessagesTotal += item.Pending
	}
	s.updateSyncProgress(ctx, userID, run.ID, progress)

	if err := s.PushPendingReadState(ctx, userID, 500); err != nil {
		status = "failed"
		errText = err.Error()
		return run, err
	}
	if err := s.PushPendingStarState(ctx, userID, 500); err != nil {
		status = "failed"
		errText = err.Error()
		return run, err
	}

	for _, planned := range plan {
		mailboxName := planned.Name
		mailboxLastUIDAtStart := planned.LastUID
		progress.CurrentMailbox = mailboxName
		progress.CurrentUID = mailboxLastUIDAtStart
		s.updateSyncProgress(ctx, userID, run.ID, progress)

		mailbox, err := s.Store.GetOrCreateMailbox(ctx, userID, account.ID, mailboxName)
		if err != nil {
			status = "failed"
			errText = err.Error()
			return run, err
		}
		s.recordMailboxStatus(ctx, userID, mailbox, planned.Status)
		repairedPlan, repaired, err := s.repairRequestedIncompleteMailbox(ctx, userID, account, mailbox, planned, requestedSet[strings.ToLower(mailboxName)], run.ID, &progress)
		if err != nil {
			status = "failed"
			errText = err.Error()
			return run, err
		}
		if repaired {
			planned = repairedPlan
			mailboxLastUIDAtStart = planned.LastUID
			lastUIDs[mailboxName] = planned.LastUID
			progress.CurrentUID = planned.LastUID
			if err := s.updateSyncProgress(ctx, userID, run.ID, progress); err != nil {
				status = "failed"
				errText = err.Error()
				return run, err
			}
		}
		if _, err := s.RepairMailboxSearchIndex(ctx, userID, mailbox, run.ID, &progress); err != nil {
			status = "failed"
			errText = err.Error()
			return run, err
		}
		searchBatch := newFetchedSearchIndexBatch(s)
		err = s.Fetcher.FetchMailbox(ctx, account, mailboxName, mailboxLastUIDAtStart, func(item FetchedMessage) error {
			if item.Mailbox == "" {
				item.Mailbox = mailboxName
			}
			if item.InternalDate.IsZero() {
				item.InternalDate = time.Now().UTC()
			}
			if item.Size == 0 {
				item.Size = int64(len(item.Raw))
			}
			progress.MessagesSeen++
			progress.CurrentMailbox = item.Mailbox
			progress.CurrentUID = item.UID

			if item.UID <= lastUIDs[mailboxName] {
				progress.MessagesSkipped++
				return s.updateSyncProgress(ctx, userID, run.ID, progress)
			}
			exists, err := s.Store.MessageExistsByUID(ctx, userID, account.ID, mailbox.ID, item.UID)
			if err != nil {
				return err
			}
			if exists {
				progress.MessagesSkipped++
				if item.UID > lastUIDs[mailboxName] {
					lastUIDs[mailboxName] = item.UID
					if err := s.Store.UpdateMailboxLastUID(ctx, userID, mailbox.ID, item.UID); err != nil {
						return err
					}
				}
				return s.updateSyncProgress(ctx, userID, run.ID, progress)
			}
			msg, pendingIndex, err := s.storeFetchedMessage(ctx, userID, account, mailbox, item)
			if err != nil {
				return err
			}
			if err := searchBatch.Add(ctx, pendingIndex); err != nil {
				return err
			}
			progress.MessagesStored++
			if shouldNotifyNewMail(mailbox, mailboxLastUIDAtStart, item) {
				progress.NewMessages++
				progress.LatestNewFrom = msg.FromAddr
				progress.LatestNewSubject = msg.Subject
			}
			if item.UID > lastUIDs[mailboxName] {
				lastUIDs[mailboxName] = item.UID
				if err := s.Store.UpdateMailboxLastUID(ctx, userID, mailbox.ID, item.UID); err != nil {
					return err
				}
			}
			return s.updateSyncProgress(ctx, userID, run.ID, progress)
		})
		if err != nil {
			status = "failed"
			errText = err.Error()
			return run, err
		}
		if err := searchBatch.Flush(ctx); err != nil {
			status = "failed"
			errText = err.Error()
			return run, err
		}
		if s.shouldSyncInlineMetadata(planned) {
			if err := s.syncMailboxReadFlags(ctx, userID, account, mailbox); err != nil {
				log.Printf("sync seen flags user_id=%d mailbox=%s: %v", userID, mailboxName, err)
			}
			if err := s.syncMailboxStarFlags(ctx, userID, account, mailbox); err != nil {
				log.Printf("sync flagged flags user_id=%d mailbox=%s: %v", userID, mailboxName, err)
			}
			if err := s.reconcileMailboxUIDs(ctx, userID, account, mailbox); err != nil {
				log.Printf("reconcile mailbox user_id=%d mailbox=%s: %v", userID, mailboxName, err)
			}
		} else {
			log.Printf("skip inline metadata sync user_id=%d mailbox=%s messages=%d limit=%d", userID, mailboxName, planned.Status.Messages, inlineMetadataSyncLimit)
		}
		progress.MailboxesDone++
		progress.CurrentMailbox = mailboxName
		progress.CurrentUID = lastUIDs[mailboxName]
		s.updateSyncProgress(ctx, userID, run.ID, progress)
	}
	return run, nil
}

func requestedMailboxSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			out[name] = true
		}
	}
	return out
}

type uidBatchFetcher interface {
	FetchUIDs(ctx context.Context, account store.MailAccount, mailbox string, uids []uint32, handle func(FetchedMessage) error) error
}

// repairRequestedIncompleteMailbox handles the explicit "Sync now" repair case.
// If local rows are missing but the UID checkpoint says there are no newer
// messages, a normal incremental sync cannot fill the gap. Instead of resetting
// the checkpoint and downloading every body again, this compares the remote UID
// list to local UIDs and fetches only the missing bodies.
func (s *Service) repairRequestedIncompleteMailbox(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox, plan MailboxPlan, requested bool, runID int64, progress *store.SyncProgress) (MailboxPlan, bool, error) {
	if !requested || plan.Status.Messages == 0 {
		return plan, false, nil
	}
	localUIDs, err := s.Store.MessageUIDsForMailbox(ctx, userID, account.ID, mailbox.ID)
	if err != nil {
		return plan, false, err
	}
	remoteCount := int(plan.Status.Messages)
	if remoteCount > 0 && len(localUIDs) >= remoteCount && plan.Pending == 0 {
		return plan, false, nil
	}
	remoteUIDs, err := s.Fetcher.UIDs(ctx, account, mailbox.Name)
	if err != nil {
		return plan, false, err
	}
	missing := missingRemoteUIDs(remoteUIDs, localUIDs)
	highestRemoteUID := maxUID(remoteUIDs)
	if progress != nil {
		progress.MessagesTotal += len(missing) - plan.Pending
		if progress.MessagesTotal < progress.MessagesSeen {
			progress.MessagesTotal = progress.MessagesSeen
		}
		progress.CurrentMailbox = mailbox.Name
		progress.CurrentUID = plan.LastUID
		if err := s.updateSyncProgress(ctx, userID, runID, *progress); err != nil {
			return plan, false, err
		}
	}
	if len(missing) == 0 {
		if highestRemoteUID > plan.LastUID {
			if err := s.Store.UpdateMailboxLastUID(ctx, userID, mailbox.ID, highestRemoteUID); err != nil {
				return plan, false, err
			}
			plan.LastUID = highestRemoteUID
		}
		plan.Pending = 0
		return plan, true, nil
	}
	log.Printf("repair incomplete mailbox user_id=%d account_id=%d mailbox=%s local=%d remote=%d missing=%d last_uid=%d uidnext=%d", userID, account.ID, mailbox.Name, len(localUIDs), len(remoteUIDs), len(missing), plan.LastUID, plan.Status.UIDNext)

	searchBatch := newFetchedSearchIndexBatch(s)
	handle := func(item FetchedMessage) error {
		if item.Mailbox == "" {
			item.Mailbox = mailbox.Name
		}
		if item.InternalDate.IsZero() {
			item.InternalDate = time.Now().UTC()
		}
		if item.Size == 0 {
			item.Size = int64(len(item.Raw))
		}
		if progress != nil {
			progress.MessagesSeen++
			progress.CurrentMailbox = item.Mailbox
			progress.CurrentUID = item.UID
		}
		exists, err := s.Store.MessageExistsByUID(ctx, userID, account.ID, mailbox.ID, item.UID)
		if err != nil {
			return err
		}
		if exists {
			if progress != nil {
				progress.MessagesSkipped++
				return s.updateSyncProgress(ctx, userID, runID, *progress)
			}
			return nil
		}
		msg, pendingIndex, err := s.storeFetchedMessage(ctx, userID, account, mailbox, item)
		if err != nil {
			return err
		}
		if err := searchBatch.Add(ctx, pendingIndex); err != nil {
			return err
		}
		if progress != nil {
			progress.MessagesStored++
			if shouldNotifyNewMail(mailbox, plan.LastUID, item) {
				progress.NewMessages++
				progress.LatestNewFrom = msg.FromAddr
				progress.LatestNewSubject = msg.Subject
			}
			if err := s.updateSyncProgress(ctx, userID, runID, *progress); err != nil {
				return err
			}
		}
		return nil
	}
	if batchFetcher, ok := s.Fetcher.(uidBatchFetcher); ok {
		if err := batchFetcher.FetchUIDs(ctx, account, mailbox.Name, missing, handle); err != nil {
			return plan, false, err
		}
	} else {
		for _, uid := range missing {
			item, err := s.Fetcher.FetchMessage(ctx, account, mailbox.Name, uid)
			if err != nil {
				return plan, false, err
			}
			if err := handle(item); err != nil {
				return plan, false, err
			}
		}
	}
	if err := searchBatch.Flush(ctx); err != nil {
		return plan, false, err
	}
	if highestRemoteUID > plan.LastUID {
		if err := s.Store.UpdateMailboxLastUID(ctx, userID, mailbox.ID, highestRemoteUID); err != nil {
			return plan, false, err
		}
		plan.LastUID = highestRemoteUID
	}
	plan.Pending = 0
	return plan, true, nil
}

func missingRemoteUIDs(remoteUIDs, localUIDs []uint32) []uint32 {
	local := make(map[uint32]bool, len(localUIDs))
	for _, uid := range localUIDs {
		if uid != 0 {
			local[uid] = true
		}
	}
	seen := map[uint32]bool{}
	out := make([]uint32, 0)
	for _, uid := range remoteUIDs {
		if uid == 0 || local[uid] || seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out
}

func maxUID(uids []uint32) uint32 {
	var max uint32
	for _, uid := range uids {
		if uid > max {
			max = uid
		}
	}
	return max
}

// planMailboxes makes progress meaningful before the first message arrives.
// IMAP STATUS is cheap compared with fetching bodies, and UIDNEXT lets us
// estimate remaining work per folder without mutating the remote mailbox.
func (s *Service) planMailboxes(ctx context.Context, account store.MailAccount, names []string, lastUIDs map[string]uint32) []MailboxPlan {
	plans := make([]MailboxPlan, 0, len(names))
	for _, name := range names {
		status, err := s.Fetcher.MailboxStatus(ctx, account, name)
		if err != nil {
			plans = append(plans, MailboxPlan{Name: name, LastUID: lastUIDs[name]})
			continue
		}
		pending := 0
		if status.UIDNext > 0 {
			highest := status.UIDNext - 1
			if highest > lastUIDs[name] {
				pending = int(highest - lastUIDs[name])
			}
		}
		plans = append(plans, MailboxPlan{Name: name, Status: status, LastUID: lastUIDs[name], Pending: pending})
	}
	return plans
}

// updateSyncProgress persists a progress snapshot and immediately notifies the
// event hub, which is what drives the sidebar and settings sync indicators.
func (s *Service) updateSyncProgress(ctx context.Context, userID, runID int64, progress store.SyncProgress) error {
	if err := s.Store.UpdateSyncRunProgress(ctx, userID, runID, progress); err != nil {
		return err
	}
	s.notify(userID)
	return nil
}

// recordMailboxStatus keeps remote counters separate from local/indexed counters
// so the UI can tell "known remotely" from "mirrored locally".
func (s *Service) recordMailboxStatus(ctx context.Context, userID int64, mailbox store.Mailbox, status MailboxStatus) {
	if status.UIDNext == 0 && status.Messages == 0 && status.Unseen == 0 && status.UIDValidity == 0 {
		return
	}
	if err := s.Store.UpdateMailboxRemoteStatus(ctx, userID, mailbox.ID, int(status.Messages), int(status.Unseen), status.UIDNext, status.UIDValidity); err != nil {
		log.Printf("store mailbox status user_id=%d mailbox=%s: %v", userID, mailbox.Name, err)
	}
}

// shouldSyncInlineMetadata avoids expensive full-mailbox flag and UID searches on
// very large folders; those folders still get new-message fetches incrementally.
func (s *Service) shouldSyncInlineMetadata(plan MailboxPlan) bool {
	return plan.Status.Messages == 0 || plan.Status.Messages <= inlineMetadataSyncLimit
}
func (s *Service) notify(userID int64) {
	if s.Notify != nil {
		s.Notify(userID)
	}
}
