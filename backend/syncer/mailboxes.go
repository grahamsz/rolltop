// File overview: Mail account and mailbox persistence, defaults, hierarchy, and sync-mode helpers.

package syncer

import (
	"context"
	"log"
	"strings"

	"rolltop/backend/store"
)

func (s *Service) mailboxesToSync(ctx context.Context, account store.MailAccount, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return s.requestedMailboxes(ctx, account, requested)
	}
	configured := strings.TrimSpace(account.Mailbox)
	if configured == "" {
		configured = store.DefaultMailboxPattern
	}
	infos, err := s.configuredMailboxes(ctx, account, configured)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		mb, err := s.Store.GetOrCreateMailboxWithRole(ctx, account.UserID, account.ID, info.Name, mailboxSpecialUseRole(info.Attributes))
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
			rebuildPending, err := s.Store.MailboxGenerationRebuildExists(ctx, account.UserID, account.ID, mb.ID)
			if err != nil {
				return nil, err
			}
			if !rebuildPending {
				continue
			}
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

func (s *Service) configuredMailboxes(ctx context.Context, account store.MailAccount, configured string) ([]MailboxInfo, error) {
	if configured != "*" {
		parts := strings.Split(configured, ",")
		out := make([]MailboxInfo, 0, len(parts))
		seen := map[string]bool{}
		for _, part := range parts {
			name := strings.TrimSpace(part)
			key := strings.ToLower(name)
			if name != "" && !seen[key] {
				seen[key] = true
				out = append(out, MailboxInfo{Name: name})
			}
		}
		if len(out) == 0 {
			return []MailboxInfo{{Name: "INBOX"}}, nil
		}
		return out, nil
	}
	infos, err := s.Fetcher.ListMailboxes(ctx, account)
	if err != nil {
		return nil, err
	}
	// The LIST completed without error, so it is authoritative: local folders
	// the server no longer reports are pruned immediately. A failed LIST
	// returns above and prunes nothing.
	if err := s.reconcileServerDeletedMailboxes(ctx, account, infos); err != nil {
		log.Printf("reconcile server-deleted mailboxes user_id=%d account_id=%d: %v", account.UserID, account.ID, err)
	}
	out := make([]MailboxInfo, 0, len(infos))
	seen := map[string]bool{}
	for _, info := range infos {
		name := strings.TrimSpace(info.Name)
		key := strings.ToLower(name)
		if name != "" && !seen[key] {
			seen[key] = true
			out = append(out, MailboxInfo{Name: name, Attributes: append([]string(nil), info.Attributes...)})
		}
	}
	if len(out) == 0 {
		return []MailboxInfo{{Name: "INBOX"}}, nil
	}
	return out, nil
}

func mailboxSpecialUseRole(attributes []string) string {
	// Junk wins if a broken server reports multiple special-use attributes: it
	// is the most safety-sensitive role and must not be treated as Inbox/All Mail.
	for _, attribute := range attributes {
		if strings.EqualFold(strings.TrimSpace(attribute), "\\Junk") {
			return "junk"
		}
	}
	for _, attribute := range attributes {
		switch strings.ToLower(strings.TrimSpace(attribute)) {
		case "\\all":
			return "all"
		case "\\sent":
			return "sent"
		case "\\drafts":
			return "drafts"
		case "\\trash":
			return "trash"
		}
	}
	return ""
}

// reconcileServerDeletedMailboxes compares local folders against a completed
// IMAP LIST and prunes mirrors the server has dropped, the same way
// reconcileMailboxUIDs drops local messages the server no longer reports. A
// successful LIST is authoritative: a folder it does not report is gone, so
// the local mirror is pruned immediately with no grace period. It runs only
// for accounts in "*" discovery mode (callers never invoke it for explicit
// folder lists) and only after a fully successful LIST. INBOX, special-use
// folders, and folders the user excluded from sync are never touched.
func (s *Service) reconcileServerDeletedMailboxes(ctx context.Context, account store.MailAccount, infos []MailboxInfo) error {
	reported := make(map[string]bool, len(infos))
	for _, info := range infos {
		if name := strings.TrimSpace(info.Name); name != "" {
			reported[strings.ToLower(name)] = true
		}
	}
	local, err := s.Store.ListMailboxesForAccount(ctx, account.UserID, account.ID)
	if err != nil {
		return err
	}
	pruned := 0
	for _, mb := range local {
		name := strings.TrimSpace(mb.Name)
		if reported[strings.ToLower(name)] {
			continue
		}
		if strings.EqualFold(name, "INBOX") || mb.Role != "" {
			continue
		}
		effective, err := s.Store.EffectiveMailboxSyncMode(ctx, account.UserID, account.ID, mb)
		if err != nil {
			return err
		}
		if strings.EqualFold(effective, "never") {
			continue
		}
		if err := s.pruneServerDeletedMailbox(ctx, account.UserID, mb); err != nil {
			return err
		}
		pruned++
	}
	if pruned > 0 {
		log.Printf("pruned %d folder(s) deleted on server user_id=%d account_id=%d", pruned, account.UserID, account.ID)
	}
	return nil
}

// pruneServerDeletedMailbox removes the local mirror of a folder the server
// dropped: message and blob rows plus search documents go through the normal
// purge path, then the mailbox row itself is deleted. It never touches the
// IMAP server.
func (s *Service) pruneServerDeletedMailbox(ctx context.Context, userID int64, mb store.Mailbox) error {
	purged, err := s.PurgeMailboxLocalReferences(ctx, userID, mb.ID)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteMailbox(ctx, userID, mb.ID); err != nil {
		return err
	}
	log.Printf("pruned folder deleted on server user_id=%d account_id=%d mailbox=%q purged_messages=%d",
		userID, mb.AccountID, mb.Name, purged)
	return nil
}
