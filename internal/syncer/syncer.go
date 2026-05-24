package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mailmirror/internal/blob"
	"mailmirror/internal/language"
	"mailmirror/internal/mailparse"
	"mailmirror/internal/search"
	"mailmirror/internal/store"
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

func (s *Service) DiscoverMailboxes(ctx context.Context, userID int64) (int, error) {
	if s.Fetcher == nil {
		return 0, errors.New("sync fetcher is not configured")
	}
	account, err := s.Store.GetMailAccount(ctx, userID)
	if err != nil {
		return 0, err
	}
	defer s.notify(userID)
	configured := strings.TrimSpace(account.Mailbox)
	if configured == "" {
		configured = "INBOX"
	}
	names, err := s.configuredMailboxNames(ctx, account, configured)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, name := range names {
		mb, err := s.Store.GetOrCreateMailbox(ctx, userID, account.ID, name)
		if err != nil {
			return count, err
		}
		if status, err := s.Fetcher.MailboxStatus(ctx, account, mb.Name); err == nil {
			s.recordMailboxStatus(ctx, userID, mb, status)
		} else {
			log.Printf("refresh mailbox status user_id=%d mailbox=%s: %v", userID, mb.Name, err)
		}
		count++
	}
	return count, nil
}

func (s *Service) AutoMailboxNames(ctx context.Context, userID int64) ([]string, error) {
	account, err := s.Store.GetMailAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.mailboxesToSync(ctx, account, nil)
}

func (s *Service) syncUser(ctx context.Context, userID int64, requestedMailboxes []string) (store.SyncRun, error) {
	if s.Fetcher == nil {
		return store.SyncRun{}, errors.New("sync fetcher is not configured")
	}
	account, err := s.Store.GetMailAccount(ctx, userID)
	if err != nil {
		return store.SyncRun{}, err
	}
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
			if err := s.storeFetchedMessage(ctx, userID, account, mailbox, item); err != nil {
				return err
			}
			progress.MessagesStored++
			if shouldNotifyNewMail(mailbox, mailboxLastUIDAtStart, item) {
				progress.NewMessages++
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

func (s *Service) syncMailboxReadFlags(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox) error {
	seenUIDs, err := s.Fetcher.SeenUIDs(ctx, account, mailbox.Name)
	if err != nil {
		return err
	}
	changedIDs, err := s.Store.UpdateMailboxReadFlags(ctx, userID, account.ID, mailbox.ID, seenUIDs)
	if err != nil {
		return err
	}
	for _, id := range changedIDs {
		msg, err := s.Store.GetMessageForUser(ctx, userID, id)
		if err != nil {
			return err
		}
		if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncMailboxStarFlags(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox) error {
	flaggedUIDs, err := s.Fetcher.FlaggedUIDs(ctx, account, mailbox.Name)
	if err != nil {
		return err
	}
	changedIDs, err := s.Store.UpdateMailboxStarFlags(ctx, userID, account.ID, mailbox.ID, flaggedUIDs)
	if err != nil {
		return err
	}
	for _, id := range changedIDs {
		msg, err := s.Store.GetMessageForUser(ctx, userID, id)
		if err != nil {
			return err
		}
		if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) notify(userID int64) {
	if s.Notify != nil {
		s.Notify(userID)
	}
}

// reconcileMailboxUIDs treats IMAP as the source of truth for membership in a
// folder. If a UID disappears remotely because it was deleted or moved out, the
// local message/search row disappears too, and the raw blob is removed when safe.
func (s *Service) reconcileMailboxUIDs(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox) error {
	uids, err := s.Fetcher.UIDs(ctx, account, mailbox.Name)
	if err != nil {
		return fmt.Errorf("reconcile mailbox %q UIDs: %w", mailbox.Name, err)
	}
	stale, err := s.Store.DeleteMessagesMissingUIDs(ctx, userID, account.ID, mailbox.ID, uids)
	if err != nil {
		return err
	}
	for _, msg := range stale {
		if s.Search != nil {
			if err := s.Search.DeleteMessage(ctx, msg.ID); err != nil {
				return err
			}
		}
		if strings.TrimSpace(msg.BlobPath) != "" && s.Blobs != nil {
			if err := s.Blobs.DeleteUserBlob(userID, msg.BlobPath); err != nil {
				return err
			}
		}
		if err := s.Store.DeleteBlobForUser(ctx, userID, msg.BlobID); err != nil && !store.IsNotFound(err) {
			return err
		}
	}
	if len(stale) > 0 {
		s.notify(userID)
	}
	return nil
}

func (s *Service) PushPendingReadState(ctx context.Context, userID int64, limit int) error {
	messages, err := s.Store.ListMessagesWithReadSyncPending(ctx, userID, limit)
	if err != nil {
		return err
	}
	for _, msg := range messages {
		if err := s.SyncReadStateForMessage(ctx, userID, msg.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SyncReadStateForMessage(ctx context.Context, userID, messageID int64) error {
	msg, err := s.Store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return err
	}
	account, err := s.Store.GetMailAccount(ctx, userID)
	if err != nil {
		return err
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, userID, msg.MailboxID)
	if err != nil {
		return err
	}
	if err := s.Fetcher.SetSeen(ctx, account, mailbox.Name, msg.UID, msg.IsRead); err != nil {
		return err
	}
	if err := s.Store.ClearReadSyncPending(ctx, userID, msg.ID); err != nil {
		return err
	}
	msg.ReadSyncPending = false
	return s.IndexAttachmentsForMessage(ctx, msg)
}

func (s *Service) PushPendingStarState(ctx context.Context, userID int64, limit int) error {
	messages, err := s.Store.ListMessagesWithStarSyncPending(ctx, userID, limit)
	if err != nil {
		return err
	}
	for _, msg := range messages {
		if err := s.SyncStarStateForMessage(ctx, userID, msg.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SetStarredForMessage(ctx context.Context, userID, messageID int64, starred bool) (store.MessageRecord, error) {
	if err := s.Store.MarkMessageStarredForUser(ctx, userID, messageID, starred, true); err != nil {
		return store.MessageRecord{}, err
	}
	msg, err := s.Store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return store.MessageRecord{}, err
	}
	if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
		return store.MessageRecord{}, err
	}
	return msg, nil
}

func (s *Service) SyncStarStateForMessage(ctx context.Context, userID, messageID int64) error {
	if s.Fetcher == nil {
		return errors.New("sync fetcher is not configured")
	}
	msg, err := s.Store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return err
	}
	account, err := s.Store.GetMailAccount(ctx, userID)
	if err != nil {
		return err
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, userID, msg.MailboxID)
	if err != nil {
		return err
	}
	if err := s.Fetcher.SetFlagged(ctx, account, mailbox.Name, msg.UID, msg.IsStarred); err != nil {
		return err
	}
	if err := s.Store.ClearStarSyncPending(ctx, userID, msg.ID); err != nil {
		return err
	}
	msg.StarSyncPending = false
	return s.IndexAttachmentsForMessage(ctx, msg)
}

func uniqueMessageIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (s *Service) MoveMessages(ctx context.Context, userID int64, messageIDs []int64, destMailboxID int64) (int, error) {
	ids := uniqueMessageIDs(messageIDs)
	if len(ids) == 0 {
		return 0, errors.New("no messages selected")
	}
	moved := 0
	for _, id := range ids {
		if err := s.MoveMessage(ctx, userID, id, destMailboxID); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

func (s *Service) StartMoveMessages(ctx context.Context, userID int64, messageIDs []int64, destMailboxID int64, onDone func()) (store.SyncRun, error) {
	if s.Fetcher == nil {
		return store.SyncRun{}, errors.New("sync fetcher is not configured")
	}
	ids := uniqueMessageIDs(messageIDs)
	if len(ids) == 0 {
		return store.SyncRun{}, errors.New("no messages selected")
	}
	dest, err := s.Store.GetMailboxForUser(ctx, userID, destMailboxID)
	if err != nil {
		return store.SyncRun{}, err
	}
	run, err := s.Store.CreateSyncRun(ctx, userID, dest.AccountID)
	if err != nil {
		return store.SyncRun{}, err
	}
	progress := store.SyncProgress{MessagesTotal: len(ids), MailboxesTotal: 1, CurrentMailbox: "Moving to " + dest.Name}
	if err := s.Store.UpdateSyncRunProgress(ctx, userID, run.ID, progress); err != nil {
		return store.SyncRun{}, err
	}
	s.notify(userID)
	go s.runMoveMessages(context.Background(), userID, ids, destMailboxID, dest.Name, run.ID, progress, onDone)
	return run, nil
}

func (s *Service) runMoveMessages(ctx context.Context, userID int64, ids []int64, destMailboxID int64, destName string, runID int64, progress store.SyncProgress, onDone func()) {
	status := "ok"
	errText := ""
	defer func() {
		if ctx.Err() != nil && status == "ok" {
			status = "interrupted"
			errText = "Server stopped before this move finished."
		}
		if status == "ok" {
			progress.MailboxesDone = 1
		}
		if err := s.Store.FinishSyncRun(context.Background(), userID, runID, status, progress, errText); err != nil {
			log.Printf("finish move run user_id=%d run_id=%d: %v", userID, runID, err)
		}
		s.notify(userID)
		if status == "ok" && onDone != nil {
			onDone()
		}
	}()
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return
		default:
		}
		msg, err := s.Store.GetMessageForUser(ctx, userID, id)
		if err != nil {
			status = "failed"
			errText = err.Error()
			return
		}
		progress.CurrentMailbox = "Moving to " + destName
		progress.CurrentUID = msg.UID
		if err := s.MoveMessage(ctx, userID, id, destMailboxID); err != nil {
			status = "failed"
			errText = err.Error()
			return
		}
		progress.MessagesSeen++
		progress.MessagesStored++
		if err := s.Store.UpdateSyncRunProgress(ctx, userID, runID, progress); err != nil {
			status = "failed"
			errText = err.Error()
			return
		}
		s.notify(userID)
	}
}

func (s *Service) MoveMessage(ctx context.Context, userID, messageID, destMailboxID int64) error {
	if s.Fetcher == nil {
		return errors.New("sync fetcher is not configured")
	}
	msg, err := s.Store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return err
	}
	account, err := s.Store.GetMailAccount(ctx, userID)
	if err != nil {
		return err
	}
	source, err := s.Store.GetMailboxForUser(ctx, userID, msg.MailboxID)
	if err != nil {
		return err
	}
	dest, err := s.Store.GetMailboxForUser(ctx, userID, destMailboxID)
	if err != nil {
		return err
	}
	if dest.AccountID != msg.AccountID || source.AccountID != msg.AccountID || account.ID != msg.AccountID {
		return errors.New("destination mailbox does not belong to this message account")
	}
	if strings.EqualFold(strings.TrimSpace(source.Name), strings.TrimSpace(dest.Name)) {
		return nil
	}
	if err := s.Fetcher.MoveMessage(ctx, account, source.Name, dest.Name, msg.UID); err != nil {
		return err
	}
	s.cleanupMovedMessage(ctx, userID, msg)
	return nil
}

func (s *Service) cleanupMovedMessage(ctx context.Context, userID int64, msg store.MessageRecord) {
	if err := s.Store.DeleteMessageForUser(ctx, userID, msg.ID); err != nil && !store.IsNotFound(err) {
		log.Printf("cleanup moved message user_id=%d message_id=%d: %v", userID, msg.ID, err)
		return
	}
	if s.Search != nil {
		if err := s.Search.DeleteMessage(ctx, msg.ID); err != nil {
			log.Printf("cleanup moved search document user_id=%d message_id=%d: %v", userID, msg.ID, err)
		}
	}
	if strings.TrimSpace(msg.BlobPath) != "" && s.Blobs != nil {
		if err := s.Blobs.DeleteUserBlob(userID, msg.BlobPath); err != nil {
			log.Printf("cleanup moved blob user_id=%d message_id=%d: %v", userID, msg.ID, err)
		}
	}
	if err := s.Store.DeleteBlobForUser(ctx, userID, msg.BlobID); err != nil && !store.IsNotFound(err) {
		log.Printf("cleanup moved blob record user_id=%d message_id=%d: %v", userID, msg.ID, err)
	}
	s.notify(userID)
}

func (s *Service) storeFetchedMessage(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox, item FetchedMessage) error {
	parsed, err := mailparse.Parse(item.Raw)
	if err != nil {
		parsed = mailparse.ParsedMessage{
			Subject: fmt.Sprintf("Unparseable message UID %d", item.UID),
			Text:    fmt.Sprintf("MailMirror stored the raw message, but could not parse its MIME body: %v. Download the raw .eml to inspect it.", err),
		}
	}
	date := parsed.Date
	if date.IsZero() {
		date = item.InternalDate
	}

	rawHash := sha256Hex(item.Raw)
	blobPath := ""
	blobRecordPath := remoteMessagePath(userID, account.ID, item.Mailbox, item.UID, rawHash)
	blobKind := "message-remote"
	blobSize := int64(0)
	if s.shouldRetainBlob(date) {
		saved, err := s.Blobs.SaveRawMessage(userID, account.ID, item.Mailbox, item.UID, item.Raw)
		if err != nil {
			return err
		}
		blobPath = saved.Path
		blobRecordPath = saved.Path
		blobKind = "message"
		blobSize = saved.Size
		rawHash = saved.SHA256
	}
	blobRec, err := s.Store.CreateBlob(ctx, store.BlobRecord{
		UserID: userID,
		Kind:   blobKind,
		Path:   blobRecordPath,
		SHA256: rawHash,
		Size:   blobSize,
	})
	if err != nil {
		return err
	}

	languageCode := language.DetectCode(parsed.Subject, parsed.Text)
	msg, err := s.Store.CreateMessage(ctx, store.CreateMessage{
		UserID:           userID,
		AccountID:        account.ID,
		MailboxID:        mailbox.ID,
		BlobID:           blobRec.ID,
		MessageIDHeader:  parsed.MessageID,
		InReplyTo:        parsed.InReplyTo,
		ReferencesHeader: parsed.References,
		Subject:          parsed.Subject,
		LanguageCode:     languageCode,
		FromAddr:         parsed.From,
		ToAddr:           parsed.To,
		CCAddr:           parsed.CC,
		Date:             date,
		InternalDate:     item.InternalDate,
		UID:              item.UID,
		Size:             item.Size,
		BlobPath:         blobPath,
		BodyText:         store.MessageBodyPreview(parsed.Text, store.DefaultMessageBodyPreviewBytes),
		BodyHTML:         "",
		IsRead:           hasSeen(item.Flags),
		IsStarred:        hasFlagged(item.Flags),
		HasAttachments:   len(parsed.Files) > 0,
	})
	if err != nil {
		return err
	}
	if msg.LanguageCode != languageCode {
		msg.LanguageCode = languageCode
		if err := s.Store.UpdateMessageLanguage(ctx, userID, msg.ID, languageCode); err != nil {
			return err
		}
	}
	if err := s.Store.CreateLocation(ctx, userID, msg.ID, mailbox.ID, item.UID); err != nil {
		return err
	}
	attachmentDocs := make([]search.AttachmentDoc, 0, len(parsed.Files))
	visibleAttachmentCount := 0
	if len(parsed.Files) > 0 {
		if err := s.Store.DeleteAttachmentsForMessage(ctx, userID, msg.ID); err != nil {
			return err
		}
	}
	for _, file := range parsed.Files {
		if _, err := s.Store.CreateAttachment(ctx, store.Attachment{
			UserID:      userID,
			MessageID:   msg.ID,
			BlobID:      blobRec.ID,
			Filename:    file.Filename,
			ContentType: file.ContentType,
			ContentID:   file.ContentID,
			IsInline:    file.IsInline,
			Size:        int64(len(file.Data)),
			BlobPath:    "",
		}); err != nil {
			return err
		}
		if !file.IsInline {
			visibleAttachmentCount++
			attachmentDocs = append(attachmentDocs, search.AttachmentDoc{
				Filename:    file.Filename,
				ContentType: file.ContentType,
				Text:        file.SearchableText(),
			})
		}
	}
	msg.HasAttachments = visibleAttachmentCount > 0
	if mailbox.IncludeInSearch && s.Search != nil {
		indexMsg := msg
		indexMsg.BodyText = parsed.Text
		indexMsg.BodyHTML = ""
		if err := s.Search.IndexMessage(ctx, indexMsg, attachmentDocs); err != nil {
			return err
		}
	}
	return s.Store.MarkMessageAttachmentIndexed(ctx, userID, msg.ID, visibleAttachmentCount > 0)
}

func (s *Service) IndexPendingAttachmentsForUser(ctx context.Context, userID int64, limit int) (int, error) {
	messages, err := s.Store.ListMessagesNeedingAttachmentIndex(ctx, userID, limit)
	if err != nil {
		return 0, err
	}
	for _, msg := range messages {
		if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
			return 0, err
		}
	}
	return len(messages), nil
}

func (s *Service) IndexAttachmentsForMessage(ctx context.Context, msg store.MessageRecord) error {
	if s.Search == nil {
		return nil
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, msg.UserID, msg.MailboxID)
	if err != nil {
		return err
	}
	if !mailbox.IncludeInSearch {
		return s.Search.DeleteMessage(ctx, msg.ID)
	}
	raw, err := s.FetchRawMessageForMessage(ctx, msg.UserID, msg)
	if err != nil {
		return err
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		if msg.LanguageCode == "" {
			msg.LanguageCode = language.DetectCode(msg.Subject, msg.BodyText)
			if err := s.Store.UpdateMessageLanguage(ctx, msg.UserID, msg.ID, msg.LanguageCode); err != nil {
				return err
			}
		}
		if indexErr := s.Search.IndexMessage(ctx, msg, nil); indexErr != nil {
			return indexErr
		}
		return s.Store.MarkMessageAttachmentIndexed(ctx, msg.UserID, msg.ID, false)
	}
	if len(parsed.Files) > 0 {
		if err := s.Store.DeleteAttachmentsForMessage(ctx, msg.UserID, msg.ID); err != nil {
			return err
		}
	}
	attachmentDocs := make([]search.AttachmentDoc, 0, len(parsed.Files))
	visibleAttachmentCount := 0
	for _, file := range parsed.Files {
		if _, err := s.Store.CreateAttachment(ctx, store.Attachment{
			UserID:      msg.UserID,
			MessageID:   msg.ID,
			BlobID:      msg.BlobID,
			Filename:    file.Filename,
			ContentType: file.ContentType,
			ContentID:   file.ContentID,
			IsInline:    file.IsInline,
			Size:        int64(len(file.Data)),
			BlobPath:    "",
		}); err != nil {
			return err
		}
		if !file.IsInline {
			visibleAttachmentCount++
			attachmentDocs = append(attachmentDocs, search.AttachmentDoc{
				Filename:    file.Filename,
				ContentType: file.ContentType,
				Text:        file.SearchableText(),
			})
		}
	}
	msg.HasAttachments = visibleAttachmentCount > 0
	msg.BodyText = parsed.Text
	msg.BodyHTML = ""
	msg.LanguageCode = language.DetectCode(parsed.Subject, parsed.Text)
	if err := s.Store.UpdateMessageLanguage(ctx, msg.UserID, msg.ID, msg.LanguageCode); err != nil {
		return err
	}
	if err := s.Search.IndexMessage(ctx, msg, attachmentDocs); err != nil {
		return err
	}
	return s.Store.MarkMessageAttachmentIndexed(ctx, msg.UserID, msg.ID, visibleAttachmentCount > 0)
}

func (s *Service) ReconcileMailboxSearchIndex(ctx context.Context, userID, mailboxID int64, include bool) error {
	if s.Search == nil {
		return nil
	}
	var afterID int64
	for {
		messages, err := s.Store.ListMessagesForMailboxIndex(ctx, userID, mailboxID, 200, afterID)
		if err != nil {
			return err
		}
		if len(messages) == 0 {
			return nil
		}
		for _, msg := range messages {
			afterID = msg.ID
			if !include {
				if err := s.Search.DeleteMessage(ctx, msg.ID); err != nil {
					return err
				}
				continue
			}
			if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
				return err
			}
		}
	}
}

func (s *Service) StartRebuildMailboxSearchIndex(ctx context.Context, userID, mailboxID int64, onDone func()) (store.SyncRun, error) {
	if s.Search == nil {
		return store.SyncRun{}, errors.New("search is not configured")
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, userID, mailboxID)
	if err != nil {
		return store.SyncRun{}, err
	}
	total, err := s.Store.CountMessagesForMailbox(ctx, userID, mailboxID)
	if err != nil {
		return store.SyncRun{}, err
	}
	run, err := s.Store.CreateSyncRun(ctx, userID, mailbox.AccountID)
	if err != nil {
		return store.SyncRun{}, err
	}
	progress := store.SyncProgress{MessagesTotal: total, MailboxesTotal: 1, CurrentMailbox: "Rebuilding index: " + mailbox.Name}
	if err := s.Store.UpdateSyncRunProgress(ctx, userID, run.ID, progress); err != nil {
		return store.SyncRun{}, err
	}
	s.notify(userID)
	go s.runRebuildMailboxSearchIndex(context.Background(), userID, mailboxID, mailbox.Name, run.ID, progress, onDone)
	return run, nil
}

func (s *Service) runRebuildMailboxSearchIndex(ctx context.Context, userID, mailboxID int64, mailboxName string, runID int64, progress store.SyncProgress, onDone func()) {
	status := "ok"
	errText := ""
	defer func() {
		if ctx.Err() != nil && status == "ok" {
			status = "interrupted"
			errText = "Server stopped before this rebuild finished."
		}
		if status == "ok" {
			progress.MailboxesDone = 1
		}
		if err := s.Store.FinishSyncRun(context.Background(), userID, runID, status, progress, errText); err != nil {
			log.Printf("finish search index rebuild user_id=%d run_id=%d: %v", userID, runID, err)
		}
		s.notify(userID)
		if status == "ok" && onDone != nil {
			onDone()
		}
	}()
	var afterID int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		messages, err := s.Store.ListMessagesForMailboxIndex(ctx, userID, mailboxID, 100, afterID)
		if err != nil {
			status = "failed"
			errText = err.Error()
			return
		}
		if len(messages) == 0 {
			return
		}
		for _, msg := range messages {
			afterID = msg.ID
			progress.CurrentMailbox = "Rebuilding index: " + mailboxName
			progress.CurrentUID = msg.UID
			if err := s.Search.DeleteMessage(ctx, msg.ID); err != nil {
				status = "failed"
				errText = err.Error()
				return
			}
			if err := s.IndexAttachmentsForMessage(ctx, msg); err != nil {
				status = "failed"
				errText = err.Error()
				return
			}
			progress.MessagesSeen++
			progress.MessagesStored++
			if err := s.Store.UpdateSyncRunProgress(ctx, userID, runID, progress); err != nil {
				status = "failed"
				errText = err.Error()
				return
			}
			s.notify(userID)
		}
	}
}

type RetentionStats struct {
	CompactedMessages int
	PrunedBlobs       int
}

func (s *Service) ApplyStorageRetention(ctx context.Context, retention time.Duration, batch int) (RetentionStats, error) {
	var stats RetentionStats
	if retention <= 0 {
		return stats, nil
	}
	if batch <= 0 || batch > 1000 {
		batch = 500
	}
	cutoff := time.Now().UTC().Add(-retention)
	compacted, err := s.Store.CompactMessageBodiesBefore(ctx, cutoff, store.DefaultMessageBodyPreviewBytes, batch)
	if err != nil {
		return stats, err
	}
	stats.CompactedMessages = compacted

	messages, err := s.Store.ListMessagesWithPrunableBlobs(ctx, cutoff, batch)
	if err != nil {
		return stats, err
	}
	for _, msg := range messages {
		if strings.TrimSpace(msg.BlobPath) != "" && s.Blobs != nil {
			if err := s.Blobs.DeleteUserBlob(msg.UserID, msg.BlobPath); err != nil {
				return stats, err
			}
		}
		if err := s.Store.MarkMessageBlobPruned(ctx, msg.UserID, msg.ID, msg.BlobID); err != nil {
			return stats, err
		}
		stats.PrunedBlobs++
	}
	return stats, nil
}

func (s *Service) FetchRawMessageForMessage(ctx context.Context, userID int64, msg store.MessageRecord) ([]byte, error) {
	if msg.UserID != userID {
		return nil, store.ErrNotFound
	}
	if s.Blobs != nil && strings.TrimSpace(msg.BlobPath) != "" {
		file, err := s.Blobs.OpenUserBlob(userID, msg.BlobPath)
		if err == nil {
			defer file.Close()
			raw, err := io.ReadAll(file)
			if err == nil {
				return raw, nil
			}
		}
	}
	if s.Fetcher == nil {
		return nil, errors.New("sync fetcher is not configured")
	}
	account, err := s.Store.GetMailAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	if account.ID != msg.AccountID {
		return nil, errors.New("message account does not belong to current user account")
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, userID, msg.MailboxID)
	if err != nil {
		return nil, err
	}
	if mailbox.AccountID != msg.AccountID {
		return nil, errors.New("message mailbox does not belong to message account")
	}
	item, err := s.Fetcher.FetchMessage(ctx, account, mailbox.Name, msg.UID)
	if err != nil {
		return nil, err
	}
	if len(item.Raw) == 0 {
		return nil, fmt.Errorf("IMAP returned empty raw message mailbox %q UID %d", mailbox.Name, msg.UID)
	}
	return item.Raw, nil
}

func (s *Service) shouldRetainBlob(date time.Time) bool {
	if s.BlobRetention <= 0 {
		return true
	}
	if date.IsZero() {
		return true
	}
	return !date.UTC().Before(time.Now().UTC().Add(-s.BlobRetention))
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func remoteMessagePath(userID, accountID int64, mailbox string, uid uint32, hash string) string {
	if len(hash) > 16 {
		hash = hash[:16]
	}
	return filepath.ToSlash(filepath.Join(
		"remote",
		"users",
		strconv.FormatInt(userID, 10),
		"accounts",
		strconv.FormatInt(accountID, 10),
		"mailboxes",
		safeSegment(mailbox),
		fmt.Sprintf("uid-%d-%s.eml", uid, hash),
	))
}

func safeSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return "_"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func shouldNotifyNewMail(mailbox store.Mailbox, mailboxLastUIDAtStart uint32, item FetchedMessage) bool {
	if mailboxLastUIDAtStart == 0 {
		return false
	}
	if mailbox.Role != "inbox" && !strings.EqualFold(strings.TrimSpace(mailbox.Name), "INBOX") {
		return false
	}
	when := item.InternalDate
	if when.IsZero() {
		return true
	}
	return !when.UTC().Before(time.Now().UTC().Add(-30 * time.Minute))
}

func hasSeen(flags []string) bool {
	for _, flag := range flags {
		if strings.EqualFold(flag, "\\Seen") {
			return true
		}
	}
	return false
}

func hasFlagged(flags []string) bool {
	for _, flag := range flags {
		switch {
		case strings.EqualFold(flag, "\\Flagged"):
			return true
		case strings.EqualFold(flag, "$Flagged"):
			return true
		case strings.EqualFold(flag, "$Starred"):
			return true
		}
	}
	return false
}

func (s *Service) mailboxesToSync(ctx context.Context, account store.MailAccount, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return s.requestedMailboxes(ctx, account, requested)
	}
	configured := strings.TrimSpace(account.Mailbox)
	if configured == "" {
		configured = "INBOX"
	}
	names, err := s.configuredMailboxNames(ctx, account, configured)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		mb, err := s.Store.GetOrCreateMailbox(ctx, account.UserID, account.ID, name)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(mb.SyncMode, "auto") {
			out = append(out, mb.Name)
			continue
		}
		effective, err := s.Store.EffectiveMailboxSyncMode(ctx, account.UserID, account.ID, mb)
		if err != nil {
			return nil, err
		}
		if effective == "auto" {
			out = append(out, mb.Name)
		}
	}
	return prioritizeInbox(out), nil
}

func (s *Service) requestedMailboxes(ctx context.Context, account store.MailAccount, requested []string) ([]string, error) {
	out := make([]string, 0, len(requested))
	seen := map[string]bool{}
	for _, raw := range requested {
		name := strings.TrimSpace(raw)
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		mb, err := s.Store.GetOrCreateMailbox(ctx, account.UserID, account.ID, name)
		if err != nil {
			return nil, err
		}
		effective, err := s.Store.EffectiveMailboxSyncMode(ctx, account.UserID, account.ID, mb)
		if err != nil {
			return nil, err
		}
		if effective == "never" {
			continue
		}
		out = append(out, mb.Name)
	}
	return prioritizeInbox(out), nil
}

func prioritizeInbox(mailboxes []string) []string {
	if len(mailboxes) < 2 {
		return mailboxes
	}
	out := make([]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		if strings.EqualFold(strings.TrimSpace(mailbox), "INBOX") {
			out = append(out, mailbox)
		}
	}
	for _, mailbox := range mailboxes {
		if !strings.EqualFold(strings.TrimSpace(mailbox), "INBOX") {
			out = append(out, mailbox)
		}
	}
	return out
}

func (s *Service) configuredMailboxNames(ctx context.Context, account store.MailAccount, configured string) ([]string, error) {
	if configured != "*" {
		parts := strings.Split(configured, ",")
		out := make([]string, 0, len(parts))
		seen := map[string]bool{}
		for _, part := range parts {
			name := strings.TrimSpace(part)
			key := strings.ToLower(name)
			if name != "" && !seen[key] {
				seen[key] = true
				out = append(out, name)
			}
		}
		if len(out) == 0 {
			return []string{"INBOX"}, nil
		}
		return out, nil
	}
	infos, err := s.Fetcher.ListMailboxes(ctx, account)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(infos))
	seen := map[string]bool{}
	for _, info := range infos {
		name := strings.TrimSpace(info.Name)
		key := strings.ToLower(name)
		if name != "" && !seen[key] {
			seen[key] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return []string{"INBOX"}, nil
	}
	return out, nil
}

type Runner struct {
	Service *Service
	ctx     context.Context

	mu             sync.Mutex
	autoRunning    map[int64]bool
	mailboxRunning map[string]bool
	mailboxPending map[string]bool
}

func NewRunner(service *Service) *Runner {
	return NewRunnerWithContext(context.Background(), service)
}

func NewRunnerWithContext(ctx context.Context, service *Service) *Runner {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Runner{
		Service:        service,
		ctx:            ctx,
		autoRunning:    map[int64]bool{},
		mailboxRunning: map[string]bool{},
		mailboxPending: map[string]bool{},
	}
}

func (r *Runner) context() context.Context {
	if r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

func (r *Runner) Start(userID int64) bool {
	ctx := r.context()
	if ctx.Err() != nil {
		return false
	}
	r.mu.Lock()
	if r.autoRunning[userID] {
		r.mu.Unlock()
		return false
	}
	r.autoRunning[userID] = true
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.autoRunning, userID)
			r.mu.Unlock()
		}()
		mailboxes, err := r.Service.AutoMailboxNames(ctx, userID)
		if err != nil {
			log.Printf("plan sync user_id=%d: %v", userID, err)
			return
		}
		// Account-wide sync is deliberately decomposed into mailbox jobs. That
		// keeps long archive backfills visible and allows foreground INBOX work
		// to proceed without waiting behind unrelated folders.
		for _, mailbox := range mailboxes {
			if ctx.Err() != nil {
				return
			}
			if !r.runMailboxes(userID, []string{mailbox}) {
				log.Printf("sync user_id=%d mailbox=%s skipped: already running", userID, mailbox)
			}
		}
		r.StartAttachmentIndex(userID)
	}()
	return true
}

func (r *Runner) StartMailboxes(userID int64, mailboxes []string) bool {
	if r.context().Err() != nil {
		return false
	}
	mailboxes = uniqueMailboxes(mailboxes)
	if len(mailboxes) == 0 {
		return false
	}
	keys, ok := r.reserveMailboxes(userID, mailboxes)
	if !ok {
		return false
	}
	go func() {
		r.runReservedMailboxes(userID, mailboxes, keys)
		r.StartAttachmentIndex(userID)
	}()
	return true
}

func (r *Runner) StartPriorityMailboxes(userID int64, mailboxes []string) bool {
	if r.context().Err() != nil {
		return false
	}
	mailboxes = uniqueMailboxes(mailboxes)
	if len(mailboxes) == 0 {
		return false
	}
	keys, ok := r.reserveMailboxes(userID, mailboxes)
	if !ok {
		r.markPending(userID, mailboxes)
		return false
	}
	go func() {
		r.runReservedMailboxes(userID, mailboxes, keys)
		r.StartAttachmentIndex(userID)
	}()
	return true
}

func (r *Runner) runMailboxes(userID int64, mailboxes []string) bool {
	if r.context().Err() != nil {
		return false
	}
	keys, ok := r.reserveMailboxes(userID, mailboxes)
	if !ok {
		return false
	}
	r.runReservedMailboxes(userID, mailboxes, keys)
	return true
}

func (r *Runner) reserveMailboxes(userID int64, mailboxes []string) ([]string, bool) {
	keys := mailboxKeys(userID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range keys {
		if r.mailboxRunning[key] {
			return nil, false
		}
	}
	for _, key := range keys {
		r.mailboxRunning[key] = true
	}
	return keys, true
}

func (r *Runner) markPending(userID int64, mailboxes []string) {
	keys := mailboxKeys(userID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range keys {
		r.mailboxPending[key] = true
	}
}

func (r *Runner) runReservedMailboxes(userID int64, mailboxes []string, keys []string) {
	defer func() {
		r.mu.Lock()
		var rerun []string
		seen := map[string]bool{}
		for i, key := range keys {
			delete(r.mailboxRunning, key)
			if r.mailboxPending[key] {
				delete(r.mailboxPending, key)
				name := mailboxes[i]
				lower := strings.ToLower(strings.TrimSpace(name))
				if lower != "" && !seen[lower] {
					seen[lower] = true
					rerun = append(rerun, name)
				}
			}
		}
		r.mu.Unlock()
		if len(rerun) > 0 && r.context().Err() == nil {
			r.StartPriorityMailboxes(userID, rerun)
		}
	}()
	ctx := r.context()
	if ctx.Err() != nil {
		return
	}
	if _, err := r.Service.SyncUserMailboxes(ctx, userID, mailboxes); err != nil {
		log.Printf("sync user_id=%d mailboxes=%s: %v", userID, strings.Join(mailboxes, ","), err)
	}
}

func (r *Runner) StartAttachmentIndex(userID int64) bool {
	if r.context().Err() != nil {
		return false
	}
	key := mailboxKey(userID, "__attachments__")
	r.mu.Lock()
	if r.mailboxRunning[key] {
		r.mu.Unlock()
		return false
	}
	r.mailboxRunning[key] = true
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.mailboxRunning, key)
			r.mu.Unlock()
		}()
		ctx := r.context()
		n, err := r.Service.IndexPendingAttachmentsForUser(ctx, userID, 100)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("attachment index user_id=%d: %v", userID, err)
			}
			return
		}
		if n > 0 {
			log.Printf("attachment index user_id=%d indexed=%d", userID, n)
		}
	}()
	return true
}

func (r *Runner) IsRunning(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.autoRunning[userID] {
		return true
	}
	prefix := fmt.Sprintf("%d:", userID)
	for key := range r.mailboxRunning {
		if key == mailboxKey(userID, "__attachments__") {
			continue
		}
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (r *Runner) IsMailboxRunning(userID int64, mailbox string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mailboxRunning[mailboxKey(userID, mailbox)]
}

func uniqueMailboxes(mailboxes []string) []string {
	out := make([]string, 0, len(mailboxes))
	seen := map[string]bool{}
	for _, mailbox := range mailboxes {
		name := strings.TrimSpace(mailbox)
		key := strings.ToLower(name)
		if name != "" && !seen[key] {
			seen[key] = true
			out = append(out, name)
		}
	}
	return out
}

func mailboxKey(userID int64, mailbox string) string {
	return fmt.Sprintf("%d:%s", userID, strings.ToLower(strings.TrimSpace(mailbox)))
}

func mailboxKeys(userID int64, mailboxes []string) []string {
	keys := make([]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		keys = append(keys, mailboxKey(userID, mailbox))
	}
	return keys
}
