package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mailmirror/backend/store"
)

const OnDemandBlobCacheRetention = 7 * 24 * time.Hour

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
	pruned, err := s.pruneMessageBlobs(ctx, messages)
	if err != nil {
		return stats, err
	}
	stats.PrunedBlobs += pruned

	cacheCutoff := time.Now().UTC().Add(-OnDemandBlobCacheRetention)
	cachedMessages, err := s.Store.ListMessagesWithExpiredCachedBlobs(ctx, cacheCutoff, batch)
	if err != nil {
		return stats, err
	}
	pruned, err = s.pruneMessageBlobs(ctx, cachedMessages)
	if err != nil {
		return stats, err
	}
	stats.PrunedBlobs += pruned
	return stats, nil
}

func (s *Service) pruneMessageBlobs(ctx context.Context, messages []store.MessageRecord) (int, error) {
	pruned := 0
	for _, msg := range messages {
		if strings.TrimSpace(msg.BlobPath) != "" && s.Blobs != nil {
			if err := s.Blobs.DeleteUserBlob(msg.UserID, msg.BlobPath); err != nil {
				return pruned, err
			}
		}
		if err := s.Store.MarkMessageBlobPruned(ctx, msg.UserID, msg.ID, msg.BlobID); err != nil {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}

func (s *Service) FetchRawMessageForMessage(ctx context.Context, userID int64, msg store.MessageRecord) ([]byte, error) {
	raw, _, _, err := s.fetchRawMessageForMessage(ctx, userID, msg)
	return raw, err
}

func (s *Service) FetchAndCacheRawMessageForMessage(ctx context.Context, userID int64, msg store.MessageRecord) ([]byte, error) {
	raw, account, mailbox, err := s.fetchRawMessageForMessage(ctx, userID, msg)
	if err != nil {
		return nil, err
	}
	if s.Blobs == nil || len(raw) == 0 || strings.TrimSpace(msg.BlobPath) != "" {
		return raw, nil
	}
	saved, err := s.Blobs.SaveRawMessage(userID, account.ID, mailbox.Name, msg.UID, raw)
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.CacheMessageBlob(ctx, userID, msg.ID, store.BlobRecord{
		Kind:   "message-cache",
		Path:   saved.Path,
		SHA256: saved.SHA256,
		Size:   saved.Size,
	}); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Service) fetchRawMessageForMessage(ctx context.Context, userID int64, msg store.MessageRecord) ([]byte, store.MailAccount, store.Mailbox, error) {
	if msg.UserID != userID {
		return nil, store.MailAccount{}, store.Mailbox{}, store.ErrNotFound
	}
	if s.Blobs != nil && strings.TrimSpace(msg.BlobPath) != "" {
		file, err := s.Blobs.OpenUserBlob(userID, msg.BlobPath)
		if err == nil {
			defer file.Close()
			raw, err := io.ReadAll(file)
			if err == nil {
				return raw, store.MailAccount{}, store.Mailbox{}, nil
			}
		}
	}
	if s.Fetcher == nil {
		return nil, store.MailAccount{}, store.Mailbox{}, errors.New("sync fetcher is not configured")
	}
	account, err := s.Store.GetMailAccountForUser(ctx, userID, msg.AccountID)
	if err != nil {
		return nil, store.MailAccount{}, store.Mailbox{}, err
	}
	if account.ID != msg.AccountID {
		return nil, store.MailAccount{}, store.Mailbox{}, errors.New("message account does not belong to current user account")
	}
	mailbox, err := s.Store.GetMailboxForUser(ctx, userID, msg.MailboxID)
	if err != nil {
		return nil, store.MailAccount{}, store.Mailbox{}, err
	}
	if mailbox.AccountID != msg.AccountID {
		return nil, store.MailAccount{}, store.Mailbox{}, errors.New("message mailbox does not belong to message account")
	}
	item, err := s.Fetcher.FetchMessage(ctx, account, mailbox.Name, msg.UID)
	if err != nil {
		return nil, store.MailAccount{}, store.Mailbox{}, err
	}
	if len(item.Raw) == 0 {
		return nil, store.MailAccount{}, store.Mailbox{}, fmt.Errorf("IMAP returned empty raw message mailbox %q UID %d", mailbox.Name, msg.UID)
	}
	return item.Raw, account, mailbox, nil
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
