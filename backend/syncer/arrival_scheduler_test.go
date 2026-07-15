package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestInboxArrivalSchedulerKeepsEarliestTimerPerUserAccount(t *testing.T) {
	now := time.Date(2026, time.July, 14, 18, 30, 0, 0, time.UTC)
	clock := newFakeInboxArrivalClock(now)
	scheduler := newInboxArrivalScheduler(context.Background(), func(context.Context, int64, int64, time.Time) (int, time.Time, error) {
		t.Fatal("finalizer ran before its timer fired")
		return 0, time.Time{}, nil
	}, nil)
	scheduler.now = clock.Now
	scheduler.after = clock.AfterFunc

	if !scheduler.schedule(7, 11, now.Add(10*time.Minute)) {
		t.Fatal("initial schedule was rejected")
	}
	if scheduler.schedule(7, 11, now.Add(20*time.Minute)) {
		t.Fatal("later duplicate replaced the earlier deadline")
	}
	if !scheduler.schedule(7, 11, now.Add(5*time.Minute)) {
		t.Fatal("earlier duplicate did not replace the deadline")
	}
	if !scheduler.schedule(7, 12, now.Add(8*time.Minute)) || !scheduler.schedule(8, 11, now.Add(9*time.Minute)) {
		t.Fatal("independent user/account schedules were rejected")
	}
	if scheduler.schedule(0, 11, now) || scheduler.schedule(7, 0, now) || scheduler.schedule(7, 11, time.Time{}) {
		t.Fatal("invalid schedule scope was accepted")
	}

	timers := clock.Timers()
	if len(timers) != 4 {
		t.Fatalf("timer count = %d, want 4 (initial, replacement, two independent)", len(timers))
	}
	if !timers[0].Stopped() {
		t.Fatal("replaced timer was not stopped")
	}
	if timers[1].delay != 5*time.Minute || timers[2].delay != 8*time.Minute || timers[3].delay != 9*time.Minute {
		t.Fatalf("timer delays = %s, %s, %s; want 5m, 8m, 9m", timers[1].delay, timers[2].delay, timers[3].delay)
	}
	scheduler.mu.Lock()
	scheduleCount := len(scheduler.schedules)
	scheduler.mu.Unlock()
	if scheduleCount != 3 {
		t.Fatalf("deduplicated schedule count = %d, want 3", scheduleCount)
	}
}

func TestInboxArrivalSchedulerSerializesFinalizationAndReschedulesEarliest(t *testing.T) {
	now := time.Date(2026, time.July, 14, 18, 30, 0, 0, time.UTC)
	clock := newFakeInboxArrivalClock(now)
	started := make(chan struct{})
	release := make(chan struct{})
	var finalizeMu sync.Mutex
	finalizeCalls := 0
	var finalizedUser, finalizedAccount int64
	var finalizedAt time.Time
	var notified []int64
	scheduler := newInboxArrivalScheduler(context.Background(), func(_ context.Context, userID, accountID int64, at time.Time) (int, time.Time, error) {
		finalizeMu.Lock()
		finalizeCalls++
		call := finalizeCalls
		finalizedUser, finalizedAccount, finalizedAt = userID, accountID, at
		finalizeMu.Unlock()
		if call == 1 {
			close(started)
			<-release
			return 2, now.Add(4 * time.Minute), nil
		}
		return 0, time.Time{}, nil
	}, func(userID int64) {
		notified = append(notified, userID)
	})
	scheduler.now = clock.Now
	scheduler.after = clock.AfterFunc

	scheduler.schedule(7, 11, now.Add(time.Minute))
	timer := clock.Timers()[0]
	fired := make(chan struct{})
	go func() {
		timer.Fire()
		close(fired)
	}()
	<-started
	if !scheduler.schedule(7, 11, now.Add(2*time.Minute)) {
		t.Fatal("deadline discovered during finalization was not queued")
	}
	if scheduler.schedule(7, 11, now.Add(3*time.Minute)) {
		t.Fatal("later deadline replaced queued earlier deadline during finalization")
	}
	if got := len(clock.Timers()); got != 1 {
		t.Fatalf("timer count while finalizer runs = %d, want 1", got)
	}
	close(release)
	<-fired

	timers := clock.Timers()
	if len(timers) != 2 || timers[1].delay != 2*time.Minute {
		t.Fatalf("follow-up timers = %+v, want one timer at queued 2m deadline", timerDelays(timers))
	}
	finalizeMu.Lock()
	calls := finalizeCalls
	userID, accountID, at := finalizedUser, finalizedAccount, finalizedAt
	finalizeMu.Unlock()
	if calls != 1 || userID != 7 || accountID != 11 || !at.Equal(now) {
		t.Fatalf("finalizer calls=%d scope=%d/%d at=%s", calls, userID, accountID, at)
	}
	if len(notified) != 1 || notified[0] != 7 {
		t.Fatalf("notifications = %#v, want user 7 once", notified)
	}

	timers[1].Fire()
	finalizeMu.Lock()
	calls = finalizeCalls
	finalizeMu.Unlock()
	if calls != 2 {
		t.Fatalf("follow-up finalizer calls = %d, want 2", calls)
	}
	scheduler.mu.Lock()
	remaining := len(scheduler.schedules)
	scheduler.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining schedules = %d, want 0", remaining)
	}
}

func TestInboxArrivalSchedulerTimerRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	finalized := make(chan struct{}, 1)
	scheduler := newInboxArrivalScheduler(ctx, func(context.Context, int64, int64, time.Time) (int, time.Time, error) {
		finalized <- struct{}{}
		return 0, time.Time{}, nil
	}, nil)
	scheduler.schedule(7, 11, time.Now().Add(200*time.Millisecond))
	cancel()

	deadline := time.Now().Add(time.Second)
	for {
		scheduler.mu.Lock()
		remaining := len(scheduler.schedules)
		scheduler.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not clear timers after context cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-finalized:
		t.Fatal("canceled scheduler ran the finalizer")
	case <-time.After(250 * time.Millisecond):
	}
	if scheduler.schedule(7, 11, time.Now()) {
		t.Fatal("canceled scheduler accepted new work")
	}
}

func TestInboxArrivalSchedulerNotifiesCommittedEventsBeforeErrorRetry(t *testing.T) {
	now := time.Date(2026, time.July, 14, 18, 30, 0, 0, time.UTC)
	clock := newFakeInboxArrivalClock(now)
	var notified []int64
	scheduler := newInboxArrivalScheduler(context.Background(), func(context.Context, int64, int64, time.Time) (int, time.Time, error) {
		return 1, time.Time{}, errors.New("next deadline unavailable")
	}, func(userID int64) {
		notified = append(notified, userID)
	})
	scheduler.now = clock.Now
	scheduler.after = clock.AfterFunc
	scheduler.schedule(7, 11, now)
	clock.Timers()[0].Fire()

	if len(notified) != 1 || notified[0] != 7 {
		t.Fatalf("notifications = %#v, want committed event notification for user 7", notified)
	}
	timers := clock.Timers()
	if len(timers) != 2 || timers[1].delay != inboxArrivalRetryDelay {
		t.Fatalf("retry timer delays = %v, want %s", timerDelays(timers), inboxArrivalRetryDelay)
	}
}

func TestInboxArrivalSchedulerPreservesEarlierConcurrentDeadlineOnError(t *testing.T) {
	now := time.Date(2026, time.July, 14, 18, 30, 0, 0, time.UTC)
	clock := newFakeInboxArrivalClock(now)
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler := newInboxArrivalScheduler(context.Background(), func(context.Context, int64, int64, time.Time) (int, time.Time, error) {
		close(started)
		<-release
		return 0, time.Time{}, errors.New("probe unavailable")
	}, nil)
	scheduler.now = clock.Now
	scheduler.after = clock.AfterFunc
	scheduler.schedule(7, 11, now)
	fired := make(chan struct{})
	go func() {
		clock.Timers()[0].Fire()
		close(fired)
	}()
	<-started
	concurrentDue := now.Add(5 * time.Second)
	if !scheduler.schedule(7, 11, concurrentDue) {
		t.Fatal("concurrent earlier deadline was not retained")
	}
	close(release)
	<-fired
	timers := clock.Timers()
	if len(timers) != 2 || timers[1].delay != 5*time.Second {
		t.Fatalf("post-error timer delays=%v, want concurrent 5s deadline instead of retry delay", timerDelays(timers))
	}
}

func TestRunnerRecoversEarliestDurableInboxArrivalSchedule(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "arrival-recovery@example.test", "Arrival Recovery", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival-recovery", EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailboxWithRole(ctx, user.ID, account.ID, "INBOX", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("Message-ID: <recovery@example.test>\r\nSubject: Recovery\r\n\r\nbody")
	internalDate := time.Date(2026, time.July, 14, 18, 30, 0, 0, time.UTC)
	fingerprint := store.MessageArrivalFingerprint(raw, "<recovery@example.test>", internalDate, int64(len(raw)))
	blob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message", Path: "users/1/recovery.eml", SHA256: fingerprint.RawSHA256, Size: int64(len(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: "<recovery@example.test>", CanonicalSHA256: fingerprint.CanonicalSHA256,
		MessageIDHash: fingerprint.MessageIDHash, Subject: "Recovery", FromAddr: "sender@example.test",
		Date: internalDate, InternalDate: internalDate, UID: 42, Size: int64(len(raw)), BlobPath: blob.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, message, fingerprint, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != store.ArrivalPending {
		t.Fatalf("arrival classification = %q, want pending", decision.Arrival.Classification)
	}

	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.ScheduleInboxArrival(user.ID, account.ID, decision.Arrival.AvailableAt.Add(time.Hour))
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	key := inboxArrivalKey{userID: user.ID, accountID: account.ID}
	runner.arrivalScheduler.mu.Lock()
	entry := runner.arrivalScheduler.schedules[key]
	runner.arrivalScheduler.mu.Unlock()
	if entry == nil || !entry.due.Equal(decision.Arrival.AvailableAt) {
		t.Fatalf("recovered schedule = %+v, want due %s", entry, decision.Arrival.AvailableAt)
	}
}

type fakeInboxArrivalClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeInboxArrivalTimer
}

func newFakeInboxArrivalClock(now time.Time) *fakeInboxArrivalClock {
	return &fakeInboxArrivalClock{now: now}
}

func (c *fakeInboxArrivalClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeInboxArrivalClock) AfterFunc(delay time.Duration, callback func()) inboxArrivalTimer {
	timer := &fakeInboxArrivalTimer{delay: delay, callback: callback}
	c.mu.Lock()
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	return timer
}

func (c *fakeInboxArrivalClock) Timers() []*fakeInboxArrivalTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*fakeInboxArrivalTimer(nil), c.timers...)
}

type fakeInboxArrivalTimer struct {
	mu       sync.Mutex
	delay    time.Duration
	callback func()
	stopped  bool
	fired    bool
}

func (t *fakeInboxArrivalTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

func (t *fakeInboxArrivalTimer) Stopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

func (t *fakeInboxArrivalTimer) Fire() {
	t.mu.Lock()
	if t.stopped || t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	callback := t.callback
	t.mu.Unlock()
	callback()
}

func timerDelays(timers []*fakeInboxArrivalTimer) []time.Duration {
	delays := make([]time.Duration, 0, len(timers))
	for _, timer := range timers {
		delays = append(delays, timer.delay)
	}
	return delays
}
