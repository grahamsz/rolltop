package syncer

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"mailmirror/backend/blob"
	"mailmirror/backend/search"
	"mailmirror/backend/store"
)

type MailboxInfo struct {
	Name string
}

type MailboxStatus struct {
	Messages    uint32
	Unseen      uint32
	UIDNext     uint32
	UIDValidity uint32
}

type MailboxPlan struct {
	Name    string
	Status  MailboxStatus
	LastUID uint32
	Pending int
}

type FetchedMessage struct {
	Mailbox      string
	UID          uint32
	InternalDate time.Time
	Size         int64
	Flags        []string
	Raw          []byte
}

type Fetcher interface {
	ListMailboxes(ctx context.Context, account store.MailAccount) ([]MailboxInfo, error)
	MailboxStatus(ctx context.Context, account store.MailAccount, mailbox string) (MailboxStatus, error)
	UIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error)
	FetchMailbox(ctx context.Context, account store.MailAccount, mailbox string, afterUID uint32, handle func(FetchedMessage) error) error
	FetchMessage(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (FetchedMessage, error)
	SetSeen(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, seen bool) error
	SeenUIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error)
	SetFlagged(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, flagged bool) error
	FlaggedUIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error)
	MoveMessage(ctx context.Context, account store.MailAccount, sourceMailbox string, destMailbox string, uid uint32) error
}

type Service struct {
	Store   *store.Store
	Blobs   *blob.Store
	Search  *search.Service
	Fetcher Fetcher

	BlobRetention time.Duration
	Notify        func(userID int64)
}

const inlineMetadataSyncLimit = 10000

func (s *Service) SyncUser(ctx context.Context, userID int64) (store.SyncRun, error) {
	return s.syncUser(ctx, userID, nil)
}

func (s *Service) SyncUserMailboxes(ctx context.Context, userID int64, mailboxNames []string) (store.SyncRun, error) {
	return s.syncUser(ctx, userID, mailboxNames)
}

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
			msg, err := s.storeFetchedMessage(ctx, userID, account, mailbox, item)
			if err != nil {
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

func (s *Service) updateSyncProgress(ctx context.Context, userID, runID int64, progress store.SyncProgress) error {
	if err := s.Store.UpdateSyncRunProgress(ctx, userID, runID, progress); err != nil {
		return err
	}
	s.notify(userID)
	return nil
}

func (s *Service) recordMailboxStatus(ctx context.Context, userID int64, mailbox store.Mailbox, status MailboxStatus) {
	if status.UIDNext == 0 && status.Messages == 0 && status.Unseen == 0 && status.UIDValidity == 0 {
		return
	}
	if err := s.Store.UpdateMailboxRemoteStatus(ctx, userID, mailbox.ID, int(status.Messages), int(status.Unseen), status.UIDNext, status.UIDValidity); err != nil {
		log.Printf("store mailbox status user_id=%d mailbox=%s: %v", userID, mailbox.Name, err)
	}
}

func (s *Service) shouldSyncInlineMetadata(plan MailboxPlan) bool {
	return plan.Status.Messages == 0 || plan.Status.Messages <= inlineMetadataSyncLimit
}
func (s *Service) notify(userID int64) {
	if s.Notify != nil {
		s.Notify(userID)
	}
}
