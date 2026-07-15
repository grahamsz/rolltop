// File overview: Mail account and mailbox persistence, defaults, hierarchy, and sync-mode helpers.

package syncer

import (
	"context"
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
