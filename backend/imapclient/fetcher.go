// File overview: IMAP fetching, mailbox listing, and mailbox watch implementation.

package imapclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	mmcrypto "mailmirror/backend/crypto"
	"mailmirror/backend/store"
	"mailmirror/backend/syncer"
)

const (
	idleCycleDuration = 29 * time.Minute
	idleStopGrace     = 5 * time.Second
)

var errIdleStopTimeout = errors.New("IDLE session did not stop cleanly")

// Fetcher implements syncer.Fetcher using go-imap and encrypted MailMirror account credentials.
type Fetcher struct {
	MasterKey []byte
	Timeout   time.Duration
	BatchSize uint32
}

// ListMailboxes logs in, lists selectable folders, and returns only names. It does
// not create local rows; sync.Service decides which folders belong to the user DB.
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

// MailboxStatus uses IMAP STATUS instead of SELECT where possible so folder counts
// and UIDNEXT can be refreshed cheaply for progress planning and UI hints.
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

// UIDs returns every UID currently present in a mailbox for local deletion/reconciliation checks.
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

// FetchMailbox is the incremental body fetch path. It selects read-only, searches
// for UIDs greater than afterUID, fetches RFC822 bodies in batches, and streams each
// result to the syncer callback instead of accumulating a mailbox in memory.
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

	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddRange(afterUID+1, 0)
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return fmt.Errorf("search new UIDs in mailbox %q after UID %d: %w", mailbox, afterUID, err)
	}
	return f.fetchUIDs(ctx, c, mailbox, uids, handle)
}

// FetchUIDs fetches a known sparse UID set. Explicit folder repair uses this to
// fill local holes without downloading every already-mirrored message body.
func (f *Fetcher) FetchUIDs(ctx context.Context, account store.MailAccount, mailbox string, uids []uint32, handle func(syncer.FetchedMessage) error) error {
	c, err := f.login(account)
	if err != nil {
		return err
	}
	defer c.Logout()
	if _, err := c.Select(mailbox, true); err != nil {
		return fmt.Errorf("select mailbox %q read-only: %w", mailbox, err)
	}
	return f.fetchUIDs(ctx, c, mailbox, uids, handle)
}

func (f *Fetcher) fetchUIDs(ctx context.Context, c *client.Client, mailbox string, uids []uint32, handle func(syncer.FetchedMessage) error) error {
	uids = normalizeUIDList(uids)
	if len(uids) == 0 {
		return nil
	}
	batchSize := f.BatchSize
	if batchSize == 0 {
		batchSize = 10
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
			if msg == nil {
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

func normalizeUIDList(uids []uint32) []uint32 {
	if len(uids) == 0 {
		return nil
	}
	out := append([]uint32(nil), uids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	var prev uint32
	for _, uid := range out {
		if uid == 0 || (n > 0 && uid == prev) {
			continue
		}
		out[n] = uid
		n++
		prev = uid
	}
	return out[:n]
}

// FetchMessage retrieves one raw message body for on-demand thread hydration or
// attachment download when the local blob has been pruned.
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

// SetSeen is the one remote read-state mutation MailMirror intentionally allows:
// it toggles only the IMAP \Seen flag for a single UID.
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

// SeenUIDs returns the remote set of read messages so local read state can be
// reconciled after another client changes flags.
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

// SetFlagged toggles the IMAP \Flagged flag for one UID so local star changes can be pushed upstream.
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

// FlaggedUIDs returns the remote starred set. The syncer stores this locally so
// MailMirror reflects stars added by another IMAP client.
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

// MoveMessage uses IMAP MOVE for one UID and refuses to emulate move with copy/delete when the server lacks support.
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

// WatchMailbox keeps an IMAP IDLE session open for one folder and invokes onChange
// when the server reports message/mailbox updates. The runner decides what to sync.
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

// idleMailboxOnce runs one bounded IDLE cycle. It exits on update, timeout, or
// context cancellation so callers can restart cleanly without a stuck connection.
func (f *Fetcher) idleMailboxOnce(ctx context.Context, c *client.Client, updates <-chan client.Update, mailbox string, onChange func()) error {
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.Idle(stop, &client.IdleOptions{LogoutTimeout: -1})
	}()
	stopIdle := func() {
		_ = stopIdleSession(stop, done, c.Terminate, idleStopGrace)
	}
	timer := time.NewTimer(idleCycleDuration)
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
				if err := stopIdleSession(stop, done, c.Terminate, idleStopGrace); err != nil {
					return fmt.Errorf("IDLE mailbox %q: %w", mailbox, err)
				}
				return fmt.Errorf("IDLE mailbox %q updates channel closed", mailbox)
			}
			switch update.(type) {
			case *client.MailboxUpdate, *client.MessageUpdate, *client.ExpungeUpdate:
				if onChange != nil {
					onChange()
				}
			}
			if err := stopIdleSession(stop, done, c.Terminate, idleStopGrace); err != nil {
				return fmt.Errorf("IDLE mailbox %q: %w", mailbox, err)
			}
			return nil
		case <-timer.C:
			if err := stopIdleSession(stop, done, c.Terminate, idleStopGrace); err != nil {
				return fmt.Errorf("IDLE mailbox %q: %w", mailbox, err)
			}
			return nil
		}
	}
}

func stopIdleSession(stop chan struct{}, done <-chan error, terminate func() error, grace time.Duration) error {
	close(stop)
	if grace <= 0 {
		grace = idleStopGrace
	}
	timer := time.NewTimer(grace)
	select {
	case err := <-done:
		timer.Stop()
		return err
	case <-timer.C:
	}
	if terminate != nil {
		if err := terminate(); err != nil {
			return fmt.Errorf("%w: terminate connection: %v", errIdleStopTimeout, err)
		}
	}
	timer = time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %v", errIdleStopTimeout, err)
		}
		return errIdleStopTimeout
	case <-timer.C:
		return errIdleStopTimeout
	}
}

// login decrypts the IMAP password at the last possible moment, opens TLS/plain
// transport according to account settings, and returns an authenticated client.
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
