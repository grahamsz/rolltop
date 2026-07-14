// File overview: Successful-move plugin observation ordering, context, and failure isolation tests.

package syncer

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

type moveTestCall struct {
	account     store.MailAccount
	source      string
	destination string
	uid         uint32
}

type moveTestFetcher struct {
	moveErr   error
	moveCalls []moveTestCall
}

func (f *moveTestFetcher) ListMailboxes(context.Context, store.MailAccount) ([]MailboxInfo, error) {
	return nil, errors.New("unexpected ListMailboxes call")
}

func (f *moveTestFetcher) MailboxStatus(context.Context, store.MailAccount, string) (MailboxStatus, error) {
	return MailboxStatus{}, errors.New("unexpected MailboxStatus call")
}

func (f *moveTestFetcher) UIDs(context.Context, store.MailAccount, string) ([]uint32, error) {
	return nil, errors.New("unexpected UIDs call")
}

func (f *moveTestFetcher) FetchMailbox(context.Context, store.MailAccount, string, uint32, func(FetchedMessage) error) error {
	return errors.New("unexpected FetchMailbox call")
}

func (f *moveTestFetcher) FetchMessage(context.Context, store.MailAccount, string, uint32) (FetchedMessage, error) {
	return FetchedMessage{}, errors.New("unexpected FetchMessage call")
}

func (f *moveTestFetcher) AppendMessage(context.Context, store.MailAccount, string, []byte, string, time.Time) (FetchedMessage, error) {
	return FetchedMessage{}, errors.New("unexpected AppendMessage call")
}

func (f *moveTestFetcher) SetSeen(context.Context, store.MailAccount, string, uint32, bool) error {
	return errors.New("unexpected SetSeen call")
}

func (f *moveTestFetcher) SeenUIDs(context.Context, store.MailAccount, string) ([]uint32, error) {
	return nil, errors.New("unexpected SeenUIDs call")
}

func (f *moveTestFetcher) SetFlagged(context.Context, store.MailAccount, string, uint32, bool) error {
	return errors.New("unexpected SetFlagged call")
}

func (f *moveTestFetcher) FlaggedUIDs(context.Context, store.MailAccount, string) ([]uint32, error) {
	return nil, errors.New("unexpected FlaggedUIDs call")
}

func (f *moveTestFetcher) MoveMessage(_ context.Context, account store.MailAccount, source, destination string, uid uint32) error {
	f.moveCalls = append(f.moveCalls, moveTestCall{account: account, source: source, destination: destination, uid: uid})
	return f.moveErr
}

type moveTestFixture struct {
	store       *store.Store
	service     *Service
	fetcher     *moveTestFetcher
	userID      int64
	account     store.MailAccount
	source      store.Mailbox
	destination store.Mailbox
	message     store.MessageRecord
}

func newMoveTestFixture(t *testing.T) moveTestFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := db.CreateUser(ctx, "move-hook@example.test", "Move Hook", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: "move-hook@example.test", Host: "imap.example.test", Port: 993,
		Username: "move-hook", EncryptedPassword: "encrypted-test-value", UseTLS: true, Mailbox: store.DefaultMailboxPattern,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	destination, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Spam")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: "users/move-hook/message.eml", SHA256: "test", Size: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC)
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: source.ID, BlobID: blob.ID,
		MessageIDHeader: "<move-hook@example.test>", ThreadKey: "thread:move-hook", Subject: "Manual spam move",
		FromAddr: "sender@example.test", ToAddr: "move-hook@example.test", CCAddr: "copy@example.test",
		Date: date, InternalDate: date.Add(time.Minute), UID: 42, Size: 9000,
		BodyText: strings.Repeat("b", store.DefaultMessageBodyPreviewBytes+64), BodyHTML: "<p>body</p>",
		IsRead: true, IsStarred: true, HasAttachments: true, IsSigned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &moveTestFetcher{}
	return moveTestFixture{
		store: db, service: &Service{Store: db, Fetcher: fetcher}, fetcher: fetcher,
		userID: user.ID, account: account, source: source, destination: destination, message: message,
	}
}

func TestMoveMessageNotifiesAfterRemoteSuccessBeforeCleanup(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	var observed plugins.MessageMoveContext
	notifications := 0
	localRowPresentDuringHook := false
	err := fixture.service.moveMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID, func(hookCtx context.Context, event plugins.MessageMoveContext) {
		notifications++
		observed = event
		_, rowErr := fixture.store.GetMessageForUser(hookCtx, fixture.userID, fixture.message.ID)
		localRowPresentDuringHook = rowErr == nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if notifications != 1 || !localRowPresentDuringHook {
		t.Fatalf("notifications=%d local_row_present=%t", notifications, localRowPresentDuringHook)
	}
	if len(fixture.fetcher.moveCalls) != 1 {
		t.Fatalf("remote move calls = %d, want 1", len(fixture.fetcher.moveCalls))
	}
	remote := fixture.fetcher.moveCalls[0]
	if remote.account.ID != fixture.account.ID || remote.source != fixture.source.Name ||
		remote.destination != fixture.destination.Name || remote.uid != fixture.message.UID {
		t.Fatalf("remote move context = %+v", remote)
	}
	if observed.UserID != fixture.userID || observed.MessageID != fixture.message.ID ||
		observed.MessageIDHeader != fixture.message.MessageIDHeader || observed.ThreadKey != fixture.message.ThreadKey ||
		observed.AccountID != fixture.account.ID || observed.SourceMailboxID != fixture.source.ID ||
		observed.SourceMailboxName != fixture.source.Name || observed.SourceMailboxRole != fixture.source.Role ||
		observed.DestinationMailboxID != fixture.destination.ID || observed.DestinationMailboxName != fixture.destination.Name ||
		observed.DestinationMailboxRole != fixture.destination.Role || observed.UID != fixture.message.UID {
		t.Fatalf("move observation identity = %+v", observed)
	}
	if observed.From != fixture.message.FromAddr || observed.To != fixture.message.ToAddr || observed.CC != fixture.message.CCAddr ||
		observed.Subject != fixture.message.Subject || !observed.Date.Equal(fixture.message.Date) ||
		!observed.InternalDate.Equal(fixture.message.InternalDate) {
		t.Fatalf("move observation envelope = %+v", observed)
	}
	if len(observed.BodyPreview) != store.DefaultMessageBodyPreviewBytes || !observed.BodyPreviewTruncated ||
		!observed.HasHTML || !observed.IsRead || !observed.IsStarred || !observed.HasAttachments ||
		observed.IsEncrypted || !observed.IsSigned {
		t.Fatalf("move observation body/flags = %+v", observed)
	}
	if _, err := fixture.store.GetMessageForUser(ctx, fixture.userID, fixture.message.ID); !store.IsNotFound(err) {
		t.Fatalf("moved local message still exists: %v", err)
	}
}

func TestMoveMessageDoesNotNotifyFailedOrNoopMove(t *testing.T) {
	t.Run("remote failure", func(t *testing.T) {
		fixture := newMoveTestFixture(t)
		remoteErr := errors.New("remote move failed")
		fixture.fetcher.moveErr = remoteErr
		notifications := 0
		err := fixture.service.moveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID, func(context.Context, plugins.MessageMoveContext) {
			notifications++
		})
		if !errors.Is(err, remoteErr) || notifications != 0 || len(fixture.fetcher.moveCalls) != 1 {
			t.Fatalf("err=%v notifications=%d move_calls=%d", err, notifications, len(fixture.fetcher.moveCalls))
		}
		if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); err != nil {
			t.Fatalf("failed move removed local message: %v", err)
		}
	})

	t.Run("same mailbox no-op", func(t *testing.T) {
		fixture := newMoveTestFixture(t)
		notifications := 0
		err := fixture.service.moveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.source.ID, func(context.Context, plugins.MessageMoveContext) {
			notifications++
		})
		if err != nil || notifications != 0 || len(fixture.fetcher.moveCalls) != 0 {
			t.Fatalf("err=%v notifications=%d move_calls=%d", err, notifications, len(fixture.fetcher.moveCalls))
		}
		if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); err != nil {
			t.Fatalf("no-op move removed local message: %v", err)
		}
	})
}

func TestMessageMoveContextOmitsEncryptedBodyPreview(t *testing.T) {
	event := messageMoveContext(store.MessageRecord{
		BodyText: "decrypted secret body", BodyHTML: "<p>secret</p>", IsEncrypted: true,
	}, store.Mailbox{}, store.Mailbox{})
	if event.BodyPreview != "" || event.BodyPreviewTruncated || !event.HasHTML || !event.IsEncrypted {
		t.Fatalf("encrypted move context = %+v", event)
	}
}

type moveTestObserver struct {
	plugins.NoopBackendPlugin
	err         error
	panicOnCall bool
	calls       int
}

func (o *moveTestObserver) ObserveMessageMove(context.Context, plugins.BackendHost, plugins.MessageMoveContext) error {
	o.calls++
	if o.panicOnCall {
		panic("sensitive panic body")
	}
	return o.err
}

func TestDispatchMessageMoveObserversIsolatesAndSafelyLogsFailures(t *testing.T) {
	var logs bytes.Buffer
	previousWriter, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	failing := &moveTestObserver{
		NoopBackendPlugin: plugins.NoopBackendPlugin{PluginID: "failing-observer"},
		err:               errors.New("sensitive returned body"),
	}
	panicking := &moveTestObserver{
		NoopBackendPlugin: plugins.NoopBackendPlugin{PluginID: "panicking-observer"},
		panicOnCall:       true,
	}
	succeeding := &moveTestObserver{
		NoopBackendPlugin: plugins.NoopBackendPlugin{PluginID: "succeeding-observer"},
	}
	event := plugins.MessageMoveContext{UserID: 7, MessageID: 11, AccountID: 13, SourceMailboxID: 17, DestinationMailboxID: 19}
	dispatchMessageMoveObservers(context.Background(), syncPluginHost{s: &Service{}}, []plugins.BackendPlugin{
		failing, panicking, succeeding,
	}, event)
	if failing.calls != 1 || panicking.calls != 1 || succeeding.calls != 1 {
		t.Fatalf("observer calls failing=%d panicking=%d succeeding=%d", failing.calls, panicking.calls, succeeding.calls)
	}
	output := logs.String()
	if !strings.Contains(output, `plugin_id="failing-observer"`) || !strings.Contains(output, `plugin_id="panicking-observer"`) {
		t.Fatalf("safe observer failures were not logged: %s", output)
	}
	for _, sensitive := range []string{"sensitive returned body", "sensitive panic body"} {
		if strings.Contains(output, sensitive) {
			t.Fatalf("observer log leaked %q: %s", sensitive, output)
		}
	}
}
