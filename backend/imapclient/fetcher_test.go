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

func TestAddSyncHeadersRecordsUTCTransferTimeAndPreservesRaw(t *testing.T) {
	marker := "v1.task.0000000001.0000000002"
	zone := time.FixedZone("source", -6*60*60)
	syncedAt := time.Date(2026, time.July, 14, 13, 42, 9, 987654321, zone)
	raw := []byte("From: sender@example.test\r\nSubject: Test\r\n\r\nbody\r\n")
	original := append([]byte(nil), raw...)

	marked, err := AddSyncHeaders(raw, marker, syncedAt)
	if err != nil {
		t.Fatal(err)
	}
	value := syncedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	wantPrefix := []byte(SyncTimestampHeader + ": " + value + "\r\n" + SyncMarkerHeader + ": " + marker + "\r\n")
	if !bytes.HasPrefix(marked, wantPrefix) {
		t.Fatalf("sync headers prefix = %q, want %q", marked[:min(len(marked), len(wantPrefix))], wantPrefix)
	}
	if !bytes.Equal(marked[len(wantPrefix):], original) {
		t.Fatal("sync header insertion changed the original raw message")
	}
	if !bytes.Equal(raw, original) {
		t.Fatal("sync header insertion mutated the caller's raw message")
	}
	gotTime, ok := SyncTimestampForMarker(marked, marker)
	if !ok || !gotTime.Equal(syncedAt.UTC().Truncate(time.Second)) {
		t.Fatalf("SyncTimestampForMarker() = %s, %t, want %s, true", gotTime, ok, syncedAt.UTC().Truncate(time.Second))
	}
}

func TestAddSyncHeadersPreservesFirstTimestampForExistingMarker(t *testing.T) {
	marker := "v1.task.0000000001.0000000002"
	firstTime := time.Date(2026, time.July, 14, 19, 42, 9, 0, time.UTC)
	first, err := AddSyncHeaders([]byte("Subject: Test\r\n\r\nbody"), marker, firstTime)
	if err != nil {
		t.Fatal(err)
	}

	again, err := AddSyncHeaders(first, marker, firstTime.Add(8*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, first) {
		t.Fatal("reapplying an existing marker changed its original transfer timestamp")
	}
	if got := bytes.Count(again, []byte(SyncMarkerHeader+":")); got != 1 {
		t.Fatalf("marker header count = %d, want 1", got)
	}
	if got := bytes.Count(again, []byte(SyncTimestampHeader+":")); got != 1 {
		t.Fatalf("timestamp header count = %d, want 1", got)
	}
}

func TestAddSyncHeadersDoesNotReuseTimestampWithoutMarker(t *testing.T) {
	marker := "v1.task.0000000001.0000000002"
	syncedAt := time.Date(2026, time.July, 14, 19, 42, 9, 0, time.UTC)
	value := syncedAt.Format(time.RFC3339)
	raw := []byte(SyncTimestampHeader + ": " + value + "\r\nSubject: Unrelated header\r\n\r\nbody")

	marked, err := AddSyncHeaders(raw, marker, syncedAt)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []byte(SyncTimestampHeader + ": " + value + "\r\n" + SyncMarkerHeader + ": " + marker + "\r\n")
	if !bytes.HasPrefix(marked, wantPrefix) {
		t.Fatalf("sync headers prefix = %q, want %q", marked[:min(len(marked), len(wantPrefix))], wantPrefix)
	}
	if got := bytes.Count(marked, []byte(SyncTimestampHeader+":")); got != 2 {
		t.Fatalf("timestamp header count = %d, want the new and unrelated headers", got)
	}
	gotTime, ok := SyncTimestampForMarker(marked, marker)
	if !ok || !gotTime.Equal(syncedAt) {
		t.Fatalf("SyncTimestampForMarker() = %s, %t, want %s, true", gotTime, ok, syncedAt)
	}

	again, err := AddSyncHeaders(marked, marker, syncedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, marked) {
		t.Fatal("reapplying the new marker changed its timestamp")
	}
}

func TestAddSyncHeadersRepairsMarkerWithoutValidTimestamp(t *testing.T) {
	marker := "v1.task.0000000001.0000000002"
	syncedAt := time.Date(2026, time.July, 14, 19, 42, 9, 0, time.UTC)
	raw := []byte(SyncMarkerHeader + ": " + marker + "\n" + SyncTimestampHeader + ": not-a-date\nSubject: Legacy\n\nbody")

	marked, err := AddSyncHeaders(raw, marker, syncedAt)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []byte(SyncTimestampHeader + ": " + syncedAt.Format(time.RFC3339) + "\n")
	if !bytes.HasPrefix(marked, wantPrefix) {
		t.Fatalf("repaired LF message = %q", marked)
	}
	if got := bytes.Count(marked, []byte(SyncMarkerHeader+":")); got != 1 {
		t.Fatalf("marker header count = %d, want 1", got)
	}
	gotTime, ok := SyncTimestampForMarker(marked, marker)
	if !ok || !gotTime.Equal(syncedAt) {
		t.Fatalf("SyncTimestampForMarker() = %s, %t, want %s, true", gotTime, ok, syncedAt)
	}
}

func TestAddSyncHeadersRejectsInvalidInputs(t *testing.T) {
	raw := []byte("Subject: Test\r\n\r\nbody")
	marker := "v1.task.0000000001.0000000002"
	for _, syncedAt := range []time.Time{
		time.Time{},
		time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1, time.January, 1, 0, 0, 0, 0, time.FixedZone("+14", 14*60*60)),
		time.Date(9999, time.December, 31, 23, 59, 59, 0, time.FixedZone("-12", -12*60*60)),
	} {
		if _, err := AddSyncHeaders(raw, marker, syncedAt); err == nil {
			t.Fatalf("AddSyncHeaders accepted invalid timestamp %v", syncedAt)
		}
	}
	if _, err := AddSyncHeaders(raw, "bad\r\nInjected: value", time.Now()); err == nil {
		t.Fatal("AddSyncHeaders accepted a header-injection marker")
	}
}

func TestAddSyncHeadersAcceptsRFC3339UTCYearBoundaries(t *testing.T) {
	marker := "v1.task.0000000001.0000000002"
	for _, syncedAt := range []time.Time{
		time.Date(1, time.January, 1, 14, 0, 1, 0, time.FixedZone("+14", 14*60*60)),
		time.Date(9999, time.December, 31, 11, 59, 59, 0, time.FixedZone("-12", -12*60*60)),
	} {
		marked, err := AddSyncHeaders([]byte("Subject: Boundary\r\n\r\nbody"), marker, syncedAt)
		if err != nil {
			t.Fatalf("AddSyncHeaders(%v): %v", syncedAt, err)
		}
		got, ok := SyncTimestampForMarker(marked, marker)
		if !ok || !got.Equal(syncedAt.UTC()) {
			t.Fatalf("SyncTimestampForMarker() = %s, %t, want %s, true", got, ok, syncedAt.UTC())
		}
	}
}

func TestSyncTimestampForMarkerRequiresExactAdjacentPair(t *testing.T) {
	marker := "v1.task.0000000001.0000000002"
	want := time.Date(2026, time.July, 14, 19, 42, 9, 0, time.UTC)
	raw := []byte(SyncTimestampHeader + ": 2025-01-01T00:00:00Z\r\n" +
		"Subject: unrelated timestamp\r\n" +
		strings.ToLower(SyncTimestampHeader) + ": 2026-07-14T13:42:09-06:00\r\n" +
		strings.ToLower(SyncMarkerHeader) + ": " + marker + "\r\n\r\nbody")
	got, ok := SyncTimestampForMarker(raw, marker)
	if !ok || !got.Equal(want) {
		t.Fatalf("SyncTimestampForMarker() = %s, %t, want %s, true", got, ok, want)
	}
}

func TestSyncTimestampForMarkerRejectsSpoofedAndLegacyLayouts(t *testing.T) {
	marker := "v1.task.0000000001.0000000002"
	otherMarker := "v1.other.0000000001.0000000002"
	timestamp := SyncTimestampHeader + ": 2026-07-14T19:42:09Z\r\n"
	markerLine := SyncMarkerHeader + ": " + marker + "\r\n"
	otherMarkerLine := SyncMarkerHeader + ": " + otherMarker + "\r\n"
	for name, raw := range map[string][]byte{
		"orphan timestamp":       []byte(timestamp + "Subject: Test\r\n\r\nbody"),
		"legacy marker only":     []byte(markerLine + "Subject: Test\r\n\r\nbody"),
		"wrong marker pair":      []byte(timestamp + otherMarkerLine + markerLine + "\r\nbody"),
		"separated pair":         []byte(timestamp + "Subject: gap\r\n" + markerLine + "\r\nbody"),
		"reversed pair":          []byte(markerLine + timestamp + "\r\nbody"),
		"malformed timestamp":    []byte(SyncTimestampHeader + ": no\r\n" + markerLine + "\r\nbody"),
		"out-of-range timestamp": []byte(SyncTimestampHeader + ": 0000-01-01T00:00:00Z\r\n" + markerLine + "\r\nbody"),
		"body-only pair":         []byte("Subject: Test\r\n\r\n" + timestamp + markerLine),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := SyncTimestampForMarker(raw, marker); ok || !got.IsZero() {
				t.Fatalf("SyncTimestampForMarker() = %s, %t, want zero, false", got, ok)
			}
		})
	}
	if got, ok := SyncTimestampForMarker([]byte(timestamp+markerLine+"\r\nbody"), "bad\r\nInjected: value"); ok || !got.IsZero() {
		t.Fatalf("invalid marker SyncTimestampForMarker() = %s, %t, want zero, false", got, ok)
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

func TestSyncDestinationSessionUIDValidityIsNilSafeAndPersistent(t *testing.T) {
	var nilSession *SyncDestinationSession
	if got := nilSession.UIDValidity(); got != 0 {
		t.Fatalf("nil session UIDValidity() = %d, want 0", got)
	}

	session := &SyncDestinationSession{uidValidity: 987654321}
	if got := session.UIDValidity(); got != 987654321 {
		t.Fatalf("UIDValidity() = %d, want 987654321", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := session.UIDValidity(); got != 987654321 {
		t.Fatalf("closed session UIDValidity() = %d, want 987654321", got)
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
