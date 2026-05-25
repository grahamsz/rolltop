package syncer

import (
	"context"
	"strings"

	"mailmirror/backend/store"
)

func (s *Service) mailboxesToSync(ctx context.Context, account store.MailAccount, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return s.requestedMailboxes(ctx, account, requested)
	}
	configured := strings.TrimSpace(account.Mailbox)
	if configured == "" {
		configured = store.DefaultMailboxPattern
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
