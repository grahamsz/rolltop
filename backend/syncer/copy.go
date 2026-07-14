// File overview: IMAP-backed message copy helpers for drag/drop copy operations.

package syncer

import (
	"context"
	"errors"
	"log"
	"strings"

	"rolltop/backend/store"
)

// CopyMessages appends local or refetched raw messages to the destination IMAP
// mailbox, then mirrors the new server-assigned UID locally. Source messages are
// left untouched, and the destination may belong to a different IMAP account.
func (s *Service) CopyMessages(ctx context.Context, userID int64, messageIDs []int64, destMailboxID int64) (int, error) {
	ids := uniqueMessageIDs(messageIDs)
	if len(ids) == 0 {
		return 0, errors.New("no messages selected")
	}
	copied := 0
	for _, id := range ids {
		if err := s.CopyMessage(ctx, userID, id, destMailboxID); err != nil {
			return copied, err
		}
		copied++
	}
	return copied, nil
}

// StartCopyMessages runs a large copy as a background sync run so the HTTP
// request can return quickly while progress remains visible in the sidebar.
func (s *Service) StartCopyMessages(ctx context.Context, userID int64, messageIDs []int64, destMailboxID int64, onDone func()) (store.SyncRun, error) {
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
	progress := store.SyncProgress{MessagesTotal: len(ids), MailboxesTotal: 1, CurrentMailbox: "Copying to " + dest.Name}
	if err := s.Store.UpdateSyncRunProgress(ctx, userID, run.ID, progress); err != nil {
		return store.SyncRun{}, err
	}
	s.notify(userID)
	go s.runCopyMessages(context.Background(), userID, ids, destMailboxID, dest.Name, run.ID, progress, onDone)
	return run, nil
}

func (s *Service) runCopyMessages(ctx context.Context, userID int64, ids []int64, destMailboxID int64, destName string, runID int64, progress store.SyncProgress, onDone func()) {
	status := "ok"
	errText := ""
	defer func() {
		if ctx.Err() != nil && status == "ok" {
			status = "interrupted"
			errText = "Server stopped before this copy finished."
		}
		if status == "ok" {
			progress.MailboxesDone = 1
		}
		if err := s.Store.FinishSyncRun(context.Background(), userID, runID, status, progress, errText); err != nil {
			log.Printf("finish copy run user_id=%d run_id=%d: %v", userID, runID, err)
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
		progress.CurrentMailbox = "Copying to " + destName
		progress.CurrentUID = msg.UID
		if err := s.CopyMessage(ctx, userID, id, destMailboxID); err != nil {
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

// CopyMessage writes one message to the destination IMAP mailbox and stores the
// resulting remote UID locally. It never deletes or alters the source message.
func (s *Service) CopyMessage(ctx context.Context, userID, messageID, destMailboxID int64) error {
	if s.Fetcher == nil {
		return errors.New("sync fetcher is not configured")
	}
	msg, err := s.Store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return err
	}
	dest, err := s.Store.GetMailboxForUser(ctx, userID, destMailboxID)
	if err != nil {
		return err
	}
	destAccount, err := s.Store.GetMailAccountForUser(ctx, userID, dest.AccountID)
	if err != nil {
		return err
	}
	raw, err := s.FetchRawMessageForMessage(ctx, userID, msg)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return errors.New("source message has no raw body to copy")
	}
	date := msg.InternalDate
	if date.IsZero() {
		date = msg.Date
	}
	fetched, err := s.Fetcher.AppendMessage(ctx, destAccount, dest.Name, raw, msg.MessageIDHeader, date)
	if err != nil {
		return err
	}
	if fetched.UID == 0 {
		return errors.New("copied IMAP message is missing a UID")
	}
	if fetched.Mailbox == "" {
		fetched.Mailbox = dest.Name
	}
	fetched.Date = msg.Date
	if fetched.InternalDate.IsZero() {
		fetched.InternalDate = date
	}
	if fetched.Size == 0 {
		fetched.Size = int64(len(raw))
	}
	if len(fetched.Raw) == 0 {
		fetched.Raw = raw
	}
	if err := s.applyCopiedMessageFlags(ctx, destAccount, dest, fetched.UID, msg, &fetched); err != nil {
		return err
	}
	copied, parsed, pendingIndex, err := s.storeFetchedMessage(ctx, userID, destAccount, dest, fetched)
	if err != nil {
		return err
	}
	searchBatch := newFetchedSearchIndexBatch(s)
	if err := searchBatch.Add(ctx, pendingIndex); err != nil {
		return err
	}
	if err := searchBatch.Flush(ctx); err != nil {
		return err
	}
	if err := s.Store.UpdateMailboxLastUID(ctx, userID, dest.ID, copied.UID); err != nil {
		return err
	}
	// A copied message is a new local row and should receive the same advisory
	// post-index classification as a normally fetched message. Run it only after
	// essential copy state is durable; plugin discovery and classifier failures
	// remain best-effort and cannot turn a completed IMAP copy into a failure.
	if classifiers, _, hookErr := s.postStorePluginHooks(ctx); hookErr != nil {
		log.Printf("copied message classifiers unavailable user_id=%d message_id=%d error_type=%T", userID, copied.ID, hookErr)
	} else {
		s.classifyStoredMessage(ctx, classifiers, copied, parsed)
	}
	s.notify(userID)
	return nil
}

func (s *Service) applyCopiedMessageFlags(ctx context.Context, account store.MailAccount, mailbox store.Mailbox, uid uint32, source store.MessageRecord, fetched *FetchedMessage) error {
	if !source.IsRead && hasSeen(fetched.Flags) {
		if err := s.Fetcher.SetSeen(ctx, account, mailbox.Name, uid, false); err != nil {
			return err
		}
		fetched.Flags = withoutIMAPFlag(fetched.Flags, "\\Seen")
	}
	if source.IsStarred && !hasFlagged(fetched.Flags) {
		if err := s.Fetcher.SetFlagged(ctx, account, mailbox.Name, uid, true); err != nil {
			return err
		}
		fetched.Flags = append(fetched.Flags, "\\Flagged")
	}
	return nil
}

func withoutIMAPFlag(flags []string, target string) []string {
	out := flags[:0]
	for _, flag := range flags {
		if !strings.EqualFold(flag, target) {
			out = append(out, flag)
		}
	}
	return out
}
