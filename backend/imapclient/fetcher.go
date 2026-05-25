package imapclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	mmcrypto "mailmirror/backend/crypto"
	"mailmirror/backend/store"
	"mailmirror/backend/syncer"
)

type Fetcher struct {
	MasterKey []byte
	Timeout   time.Duration
	BatchSize uint32
}

func (f *Fetcher) ListMailboxes(ctx context.Context, account store.MailAccount) ([]syncer.MailboxInfo, error) {
	c, err := f.login(account)
	if err != nil {
		return nil, err
	}
	defer c.Logout()

	mailboxes := make(chan *imap.MailboxInfo, 50)
	done := make(chan error, 1)
	go func() {
		done <- c.List("", "*", mailboxes)
	}()

	var out []syncer.MailboxInfo
	for mb := range mailboxes {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if mb == nil || hasAttr(mb.Attributes, imap.NoSelectAttr) {
			continue
		}
		out = append(out, syncer.MailboxInfo{Name: mb.Name})
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	return out, nil
}

func (f *Fetcher) MailboxStatus(ctx context.Context, account store.MailAccount, mailbox string) (syncer.MailboxStatus, error) {
	select {
	case <-ctx.Done():
		return syncer.MailboxStatus{}, ctx.Err()
	default:
	}
	c, err := f.login(account)
	if err != nil {
		return syncer.MailboxStatus{}, err
	}
	defer c.Logout()
	status, err := c.Status(mailbox, []imap.StatusItem{imap.StatusMessages, imap.StatusUnseen, imap.StatusUidNext, imap.StatusUidValidity})
	if err != nil {
		return syncer.MailboxStatus{}, fmt.Errorf("status mailbox %q: %w", mailbox, err)
	}
	return syncer.MailboxStatus{Messages: status.Messages, Unseen: status.Unseen, UIDNext: status.UidNext, UIDValidity: status.UidValidity}, nil
}

func (f *Fetcher) UIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	c, err := f.login(account)
	if err != nil {
		return nil, err
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, true); err != nil {
		return nil, fmt.Errorf("select mailbox %q read-only for UID reconcile: %w", mailbox, err)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddRange(1, 0)
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("search UIDs in mailbox %q: %w", mailbox, err)
	}
	return uids, nil
}

func (f *Fetcher) FetchMailbox(ctx context.Context, account store.MailAccount, mailbox string, afterUID uint32, handle func(syncer.FetchedMessage) error) error {
	c, err := f.login(account)
	if err != nil {
		return err
	}
	defer c.Logout()

	mbox, err := c.Select(mailbox, true)
	if err != nil {
		return fmt.Errorf("select mailbox %q read-only: %w", mailbox, err)
	}
	if mbox.Messages == 0 || mbox.UidNext <= afterUID+1 || afterUID == ^uint32(0) {
		return nil
	}

	batchSize := f.BatchSize
	if batchSize == 0 {
		batchSize = 10
	}

	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddRange(afterUID+1, 0)
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return fmt.Errorf("search new UIDs in mailbox %q after UID %d: %w", mailbox, afterUID, err)
	}
	if len(uids) == 0 {
		return nil
	}

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchInternalDate, imap.FetchRFC822Size, imap.FetchFlags, section.FetchItem()}
	for i := 0; i < len(uids); i += int(batchSize) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		end := i + int(batchSize)
		if end > len(uids) {
			end = len(uids)
		}
		seqset := new(imap.SeqSet)
		seqset.AddNum(uids[i:end]...)
		messages := make(chan *imap.Message, 20)
		done := make(chan error, 1)
		go func() {
			done <- c.UidFetch(seqset, items, messages)
		}()
		for msg := range messages {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if msg == nil || msg.Uid <= afterUID {
				continue
			}
			body := msg.GetBody(section)
			if body == nil {
				continue
			}
			raw, err := io.ReadAll(body)
			if err != nil {
				return fmt.Errorf("read message body mailbox %q UID %d: %w", mailbox, msg.Uid, err)
			}
			if err := handle(syncer.FetchedMessage{
				Mailbox:      mailbox,
				UID:          msg.Uid,
				InternalDate: msg.InternalDate,
				Size:         int64(msg.Size),
				Flags:        msg.Flags,
				Raw:          raw,
			}); err != nil {
				return fmt.Errorf("store message mailbox %q UID %d: %w", mailbox, msg.Uid, err)
			}
		}
		if err := <-done; err != nil {
			return fmt.Errorf("fetch mailbox %q UID batch %s: %w", mailbox, describeBatch(uids[i:end]), err)
		}
	}
	return nil
}

func (f *Fetcher) FetchMessage(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (syncer.FetchedMessage, error) {
	select {
	case <-ctx.Done():
		return syncer.FetchedMessage{}, ctx.Err()
	default:
	}
	if uid == 0 {
		return syncer.FetchedMessage{}, fmt.Errorf("fetch message requires a nonzero UID")
	}
	c, err := f.login(account)
	if err != nil {
		return syncer.FetchedMessage{}, err
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, true); err != nil {
		return syncer.FetchedMessage{}, fmt.Errorf("select mailbox %q read-only: %w", mailbox, err)
	}
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchInternalDate, imap.FetchRFC822Size, imap.FetchFlags, section.FetchItem()}
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqset, items, messages)
	}()
	var out syncer.FetchedMessage
	found := false
	for msg := range messages {
		select {
		case <-ctx.Done():
			return syncer.FetchedMessage{}, ctx.Err()
		default:
		}
		if msg == nil || msg.Uid != uid {
			continue
		}
		body := msg.GetBody(section)
		if body == nil {
			continue
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			return syncer.FetchedMessage{}, fmt.Errorf("read message body mailbox %q UID %d: %w", mailbox, uid, err)
		}
		out = syncer.FetchedMessage{
			Mailbox:      mailbox,
			UID:          msg.Uid,
			InternalDate: msg.InternalDate,
			Size:         int64(msg.Size),
			Flags:        msg.Flags,
			Raw:          raw,
		}
		found = true
	}
	if err := <-done; err != nil {
		return syncer.FetchedMessage{}, fmt.Errorf("fetch mailbox %q UID %d: %w", mailbox, uid, err)
	}
	if !found {
		return syncer.FetchedMessage{}, fmt.Errorf("message not found mailbox %q UID %d", mailbox, uid)
	}
	return out, nil
}

func (f *Fetcher) SetSeen(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, seen bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c, err := f.login(account)
	if err != nil {
		return err
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, false); err != nil {
		return fmt.Errorf("select mailbox %q read-write: %w", mailbox, err)
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	var op imap.FlagsOp = imap.AddFlags
	if !seen {
		op = imap.RemoveFlags
	}
	item := imap.FormatFlagsOp(op, true)
	if err := c.UidStore(seqset, item, []interface{}{imap.SeenFlag}, nil); err != nil {
		return fmt.Errorf("sync seen flag mailbox %q UID %d: %w", mailbox, uid, err)
	}
	return nil
}

func (f *Fetcher) SeenUIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	c, err := f.login(account)
	if err != nil {
		return nil, err
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, true); err != nil {
		return nil, fmt.Errorf("select mailbox %q read-only for seen search: %w", mailbox, err)
	}
	criteria := imap.NewSearchCriteria()
	criteria.WithFlags = []string{imap.SeenFlag}
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("search seen UIDs in mailbox %q: %w", mailbox, err)
	}
	return uids, nil
}

func (f *Fetcher) SetFlagged(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, flagged bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c, err := f.login(account)
	if err != nil {
		return err
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, false); err != nil {
		return fmt.Errorf("select mailbox %q read-write: %w", mailbox, err)
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	var op imap.FlagsOp = imap.AddFlags
	if !flagged {
		op = imap.RemoveFlags
	}
	item := imap.FormatFlagsOp(op, true)
	if err := c.UidStore(seqset, item, []interface{}{imap.FlaggedFlag}, nil); err != nil {
		return fmt.Errorf("sync flagged flag mailbox %q UID %d: %w", mailbox, uid, err)
	}
	return nil
}

func (f *Fetcher) FlaggedUIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	c, err := f.login(account)
	if err != nil {
		return nil, err
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, true); err != nil {
		return nil, fmt.Errorf("select mailbox %q read-only for flagged search: %w", mailbox, err)
	}
	criteria := imap.NewSearchCriteria()
	criteria.WithFlags = []string{imap.FlaggedFlag}
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("search flagged UIDs in mailbox %q: %w", mailbox, err)
	}
	return uids, nil
}

func (f *Fetcher) MoveMessage(ctx context.Context, account store.MailAccount, sourceMailbox string, destMailbox string, uid uint32) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	sourceMailbox = strings.TrimSpace(sourceMailbox)
	destMailbox = strings.TrimSpace(destMailbox)
	if sourceMailbox == "" || destMailbox == "" || uid == 0 {
		return fmt.Errorf("move message requires source mailbox, destination mailbox, and UID")
	}
	c, err := f.login(account)
	if err != nil {
		return err
	}
	defer c.Logout()
	if _, err := c.Select(sourceMailbox, false); err != nil {
		return fmt.Errorf("select mailbox %q read-write for move: %w", sourceMailbox, err)
	}
	ok, err := c.Support("MOVE")
	if err != nil {
		return fmt.Errorf("check IMAP MOVE support: %w", err)
	}
	if !ok {
		return fmt.Errorf("IMAP server does not support MOVE; MailMirror will not emulate move with copy/delete")
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	if err := c.UidMove(seqset, destMailbox); err != nil {
		return fmt.Errorf("move mailbox %q UID %d to %q: %w", sourceMailbox, uid, destMailbox, err)
	}
	return nil
}

func (f *Fetcher) WatchMailbox(ctx context.Context, account store.MailAccount, mailbox string, onChange func()) error {
	if strings.TrimSpace(mailbox) == "" {
		return fmt.Errorf("watch mailbox requires a mailbox name")
	}
	c, err := f.login(account)
	if err != nil {
		return err
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, true); err != nil {
		return fmt.Errorf("select mailbox %q read-only for IDLE: %w", mailbox, err)
	}
	updates := make(chan client.Update, 10)
	c.Updates = updates
	for {
		if err := f.idleMailboxOnce(ctx, c, updates, mailbox, onChange); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (f *Fetcher) idleMailboxOnce(ctx context.Context, c *client.Client, updates <-chan client.Update, mailbox string, onChange func()) error {
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.Idle(stop, nil)
	}()
	stopIdle := func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	timer := time.NewTimer(29 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			stopIdle()
			return ctx.Err()
		case err := <-done:
			if err != nil {
				return fmt.Errorf("IDLE mailbox %q: %w", mailbox, err)
			}
			return nil
		case update, ok := <-updates:
			if !ok {
				stopIdle()
				return fmt.Errorf("IDLE mailbox %q updates channel closed", mailbox)
			}
			switch update.(type) {
			case *client.MailboxUpdate, *client.MessageUpdate, *client.ExpungeUpdate:
				if onChange != nil {
					onChange()
				}
			}
			stopIdle()
			return nil
		case <-timer.C:
			stopIdle()
			return nil
		}
	}
}

func (f *Fetcher) login(account store.MailAccount) (*client.Client, error) {
	password, err := mmcrypto.DecryptString(f.MasterKey, account.EncryptedPassword)
	if err != nil {
		return nil, fmt.Errorf("decrypt IMAP password: %w", err)
	}
	addr := net.JoinHostPort(account.Host, fmt.Sprintf("%d", account.Port))
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	var c *client.Client
	if account.UseTLS {
		dialer := &net.Dialer{Timeout: timeout}
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: account.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return nil, fmt.Errorf("connect TLS to IMAP server %s: %w", addr, err)
		}
		c, err = client.New(conn)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("initialize IMAP client for %s: %w", addr, err)
		}
	} else {
		c, err = client.DialWithDialer(&net.Dialer{Timeout: timeout}, addr)
		if err != nil {
			return nil, fmt.Errorf("connect plain IMAP to server %s: %w", addr, err)
		}
	}
	if err := c.Login(account.Username, password); err != nil {
		_ = c.Logout()
		return nil, fmt.Errorf("login to IMAP server %s: %w", addr, err)
	}
	return c, nil
}

func hasAttr(attrs []string, target string) bool {
	for _, attr := range attrs {
		if strings.EqualFold(attr, target) {
			return true
		}
	}
	return false
}

func describeBatch(uids []uint32) string {
	if len(uids) == 0 {
		return "empty"
	}
	if len(uids) == 1 {
		return fmt.Sprintf("%d", uids[0])
	}
	return fmt.Sprintf("%d..%d (%d messages)", uids[0], uids[len(uids)-1], len(uids))
}
