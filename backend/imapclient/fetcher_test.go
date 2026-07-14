package imapclient

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"

	"rolltop/backend/syncer"
)

func TestRawBodySectionUsesPeek(t *testing.T) {
	section := rawBodySection()
	if !section.Peek {
		t.Fatal("raw body section does not use PEEK")
	}
	if got, want := section.FetchItem(), imap.FetchItem("BODY.PEEK[]"); got != want {
		t.Fatalf("raw body fetch item = %q, want %q", got, want)
	}
}

func TestFetcherCommandTimeoutUsesBoundedDefault(t *testing.T) {
	if got := (*Fetcher)(nil).commandTimeout(); got != 60*time.Second {
		t.Fatalf("nil fetcher timeout = %s", got)
	}
	if got := (&Fetcher{}).commandTimeout(); got != 60*time.Second {
		t.Fatalf("default fetcher timeout = %s", got)
	}
	if got := (&Fetcher{Timeout: 17 * time.Second}).commandTimeout(); got != 17*time.Second {
		t.Fatalf("configured fetcher timeout = %s", got)
	}
}

func TestProbeCapabilitiesReportsAuthenticatedServerSupport(t *testing.T) {
	supporter := &fakeCapabilitySupporter{supported: map[string]bool{"IDLE": true}}
	got, err := probeCapabilities(supporter)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IDLE || got.UIDPlus {
		t.Fatalf("capabilities = %+v, want IDLE only", got)
	}
	if !reflect.DeepEqual(supporter.calls, []string{"IDLE", "UIDPLUS"}) {
		t.Fatalf("Support calls = %#v", supporter.calls)
	}
}

func TestProbeCapabilitiesReturnsSupportError(t *testing.T) {
	want := errors.New("capability failed")
	_, err := probeCapabilities(&fakeCapabilitySupporter{errFor: "UIDPLUS", err: want})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "UIDPLUS") {
		t.Fatalf("probeCapabilities error = %v", err)
	}
}

type fakeCapabilitySupporter struct {
	supported map[string]bool
	errFor    string
	err       error
	calls     []string
}

func (f *fakeCapabilitySupporter) Support(capability string) (bool, error) {
	f.calls = append(f.calls, capability)
	if capability == f.errFor {
		return false, f.err
	}
	return f.supported[capability], nil
}

func TestOrderFetchedUIDBatchSortsServerResponses(t *testing.T) {
	fetched := []syncer.FetchedMessage{{UID: 9}, {UID: 3}, {UID: 7}}
	got, err := orderFetchedUIDBatch([]uint32{3, 7, 9}, fetched)
	if err != nil {
		t.Fatal(err)
	}
	if gotUIDs := []uint32{got[0].UID, got[1].UID, got[2].UID}; !reflect.DeepEqual(gotUIDs, []uint32{3, 7, 9}) {
		t.Fatalf("ordered UIDs = %#v", gotUIDs)
	}
}

func TestOrderFetchedUIDBatchRejectsMissingUIDBeforeDelivery(t *testing.T) {
	got, err := orderFetchedUIDBatch([]uint32{3, 7, 9}, []syncer.FetchedMessage{{UID: 9}, {UID: 3}})
	if err == nil || !strings.Contains(err.Error(), "UID batch 7") {
		t.Fatalf("missing UID error = %v", err)
	}
	if got != nil {
		t.Fatalf("partial ordered batch = %#v, want nil", got)
	}
}

func TestOrderFetchedUIDBatchIgnoresUnsolicitedUIDAndRejectsDuplicates(t *testing.T) {
	got, err := orderFetchedUIDBatch([]uint32{3}, []syncer.FetchedMessage{{UID: 99}, {UID: 3}})
	if err != nil || len(got) != 1 || got[0].UID != 3 {
		t.Fatalf("unsolicited UID result = %#v, %v", got, err)
	}
	if _, err := orderFetchedUIDBatch([]uint32{3}, []syncer.FetchedMessage{{UID: 3}, {UID: 3}}); err == nil {
		t.Fatal("duplicate requested UID was accepted")
	}
}

func TestStopIdleSessionStopsCleanly(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan error, 1)
	terminated := false
	go func() {
		<-stop
		done <- nil
	}()

	if err := stopIdleSession(stop, done, func() error {
		terminated = true
		return nil
	}, time.Second); err != nil {
		t.Fatalf("stopIdleSession error = %v", err)
	}
	if terminated {
		t.Fatalf("terminate called for clean IDLE stop")
	}
}

func TestStopIdleSessionTerminatesStuckIdle(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan error)
	terminated := false

	err := stopIdleSession(stop, done, func() error {
		terminated = true
		return nil
	}, 10*time.Millisecond)
	if !errors.Is(err, errIdleStopTimeout) {
		t.Fatalf("stopIdleSession error = %v, want errIdleStopTimeout", err)
	}
	if !terminated {
		t.Fatalf("terminate was not called for stuck IDLE stop")
	}
	select {
	case <-stop:
	default:
		t.Fatalf("stop channel was not closed")
	}
}

func TestMailboxUIDSearchCriteriaCombinesUIDAndSince(t *testing.T) {
	since := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	criteria, ok := mailboxUIDSearchCriteria(41, since)
	if !ok || criteria == nil {
		t.Fatal("mailboxUIDSearchCriteria returned no criteria")
	}
	if criteria.Uid == nil || !criteria.Uid.Contains(42) || !criteria.Uid.Contains(900) || criteria.Uid.Contains(41) {
		t.Fatalf("UID criteria = %v, want 42:*", criteria.Uid)
	}
	if !criteria.Since.Equal(since) {
		t.Fatalf("Since = %s, want %s", criteria.Since, since)
	}
	if criteria, ok := mailboxUIDSearchCriteria(^uint32(0), since); ok || criteria != nil {
		t.Fatalf("maximum UID criteria = %#v, %t, want nil, false", criteria, ok)
	}
}

func TestMessageSyncMarkerIsDeterministicAndDelimited(t *testing.T) {
	got, err := MessageSyncMarker("task_abc-123", 7, 42)
	if err != nil {
		t.Fatal(err)
	}
	if want := "v1.task_abc-123.0000000007.0000000042"; got != want {
		t.Fatalf("marker = %q, want %q", got, want)
	}
	other, err := MessageSyncMarker("task_abc-123", 7, 420)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(other, got) || strings.Contains(got, other) {
		t.Fatalf("markers are ambiguous substrings: %q and %q", got, other)
	}
	for _, tc := range []struct {
		token       string
		uidValidity uint32
		uid         uint32
	}{
		{"", 1, 1},
		{"bad token", 1, 1},
		{"bad\r\ntoken", 1, 1},
		{"task", 0, 1},
		{"task", 1, 0},
	} {
		if _, err := MessageSyncMarker(tc.token, tc.uidValidity, tc.uid); err == nil {
			t.Fatalf("MessageSyncMarker(%q, %d, %d) succeeded", tc.token, tc.uidValidity, tc.uid)
		}
	}
}

func TestAddSyncMarkerHeaderPreservesRawMessageAndIsIdempotent(t *testing.T) {
	marker, err := MessageSyncMarker("task", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: sender@example.test\r\nSubject: Test\r\n\r\nbody\r\n")
	marked, err := AddSyncMarkerHeader(raw, marker)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []byte(SyncMarkerHeader + ": " + marker + "\r\n")
	if !bytes.HasPrefix(marked, wantPrefix) {
		t.Fatalf("marked message prefix = %q, want %q", marked[:len(wantPrefix)], wantPrefix)
	}
	if !bytes.Equal(marked[len(wantPrefix):], raw) {
		t.Fatal("marker insertion changed the original raw message")
	}
	again, err := AddSyncMarkerHeader(marked, marker)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, marked) {
		t.Fatal("adding the same marker twice was not idempotent")
	}
	if _, err := AddSyncMarkerHeader(raw, "bad\r\nInjected: value"); err == nil {
		t.Fatal("header-injection marker was accepted")
	}
}

func TestAddSyncMarkerHeaderUsesSourceLineEndings(t *testing.T) {
	raw := []byte("From: sender@example.test\nSubject: Test\n\nbody\n")
	marked, err := AddSyncMarkerHeader(raw, "v1.task.0000000001.0000000002")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(marked, []byte(SyncMarkerHeader+": v1.task.0000000001.0000000002\n")) {
		t.Fatalf("marked LF message = %q", marked)
	}
}

func TestHasSyncMarkerForTaskMatchesOnlyValidHeaderMarkers(t *testing.T) {
	raw := []byte("Subject: Test\r\nX-Rolltop-Sync-ID: v1.task_abc.0000000007.0000000042\r\n\r\nbody\r\n")
	if !HasSyncMarkerForTask(raw, "task_abc") {
		t.Fatal("valid task marker was not detected")
	}
	if HasSyncMarkerForTask(raw, "task") {
		t.Fatal("a marker for another task was accepted")
	}
	invalid := []byte("X-Rolltop-Sync-ID: v1.task_abc.not-a-uid.0000000042\r\n\r\nbody")
	if HasSyncMarkerForTask(invalid, "task_abc") {
		t.Fatal("an invalid marker was accepted")
	}
	bodyOnly := []byte("Subject: Test\r\n\r\nX-Rolltop-Sync-ID: v1.task_abc.0000000007.0000000042")
	if HasSyncMarkerForTask(bodyOnly, "task_abc") {
		t.Fatal("a marker in the message body was accepted")
	}
}

func TestSafeAppendFlagsKeepsOnlyPortableNonDestructiveFlags(t *testing.T) {
	got := SafeAppendFlags([]string{
		imap.SeenFlag,
		"\\seen",
		imap.AnsweredFlag,
		imap.FlaggedFlag,
		imap.DraftFlag,
		imap.DeletedFlag,
		imap.RecentFlag,
		"custom-keyword",
		"",
	})
	want := []string{imap.SeenFlag, imap.AnsweredFlag, imap.FlaggedFlag, imap.DraftFlag}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SafeAppendFlags() = %#v, want %#v", got, want)
	}
}

func TestClosedSyncDestinationSessionRejectsOperations(t *testing.T) {
	session := &SyncDestinationSession{}
	if _, _, err := session.FindMessageBySyncMarker(context.Background(), "v1.task.0000000001.0000000002"); err == nil {
		t.Fatal("closed destination session searched for a marker")
	}
	if _, err := session.AppendMessageWithSyncMarker(context.Background(), []byte("Subject: Test\r\n\r\nbody"), "v1.task.0000000001.0000000002", time.Time{}, nil); err == nil {
		t.Fatal("closed destination session appended a message")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() on closed destination session = %v", err)
	}
	if err := (*SyncDestinationSession)(nil).Close(); err != nil {
		t.Fatalf("Close() on nil destination session = %v", err)
	}
}

func TestSyncDestinationSessionHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session := &SyncDestinationSession{}
	if _, _, err := session.FindMessageBySyncMarker(ctx, "v1.task.0000000001.0000000002"); !errors.Is(err, context.Canceled) {
		t.Fatalf("FindMessageBySyncMarker() error = %v, want context.Canceled", err)
	}
}
