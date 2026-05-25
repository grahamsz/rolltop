// File overview: IMAP flag reconciliation helpers for read and starred state.

package syncer

import (
	"context"
	"errors"
	"strings"

	"mailmirror/backend/store"
)

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
	account, err := s.Store.GetMailAccountForUser(ctx, userID, msg.AccountID)
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
	account, err := s.Store.GetMailAccountForUser(ctx, userID, msg.AccountID)
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
