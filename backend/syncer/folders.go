// File overview: Remote IMAP folder management helpers.

package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"rolltop/backend/store"
)

var (
	ErrFolderExists                  = errors.New("folder already exists")
	ErrRemoteFolderCreateUnsupported = errors.New("remote folder creation is not configured")
)

type remoteFolderCreator interface {
	CreateMailbox(ctx context.Context, account store.MailAccount, mailbox string) error
}

// CreateRemoteFolder creates a server-side IMAP folder, then records the local
// mailbox row so Rolltop can sync and configure it.
func (s *Service) CreateRemoteFolder(ctx context.Context, userID, accountID int64, name string) (store.Mailbox, error) {
	if s == nil || s.Store == nil {
		return store.Mailbox{}, errors.New("store is not configured")
	}
	name, err := normalizeRemoteFolderName(name)
	if err != nil {
		return store.Mailbox{}, err
	}
	if existing, err := s.Store.GetMailbox(ctx, userID, accountID, name); err == nil {
		return existing, fmt.Errorf("%w: %s", ErrFolderExists, name)
	} else if !store.IsNotFound(err) {
		return store.Mailbox{}, err
	}
	if s.Fetcher == nil {
		return store.Mailbox{}, ErrRemoteFolderCreateUnsupported
	}
	creator, ok := s.Fetcher.(remoteFolderCreator)
	if !ok {
		return store.Mailbox{}, ErrRemoteFolderCreateUnsupported
	}
	account, err := s.Store.GetMailAccountForUser(ctx, userID, accountID)
	if err != nil {
		return store.Mailbox{}, err
	}
	if err := creator.CreateMailbox(ctx, account, name); err != nil {
		return store.Mailbox{}, err
	}
	mb, err := s.Store.GetOrCreateMailbox(ctx, userID, accountID, name)
	if err != nil {
		return store.Mailbox{}, err
	}
	if status, err := s.Fetcher.MailboxStatus(ctx, account, name); err == nil {
		s.recordMailboxStatus(ctx, userID, mb, status)
		mb, _ = s.Store.GetMailboxForUser(ctx, userID, mb.ID)
	}
	s.notify(userID)
	return mb, nil
}

func normalizeRemoteFolderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("folder name is required")
	}
	if len(name) > 255 {
		return "", errors.New("folder name is too long")
	}
	for _, r := range name {
		if r == 0 || r == '\r' || r == '\n' || unicode.IsControl(r) {
			return "", errors.New("folder name contains an invalid character")
		}
	}
	if strings.EqualFold(name, "INBOX") {
		return "", fmt.Errorf("%w: INBOX", ErrFolderExists)
	}
	return name, nil
}
