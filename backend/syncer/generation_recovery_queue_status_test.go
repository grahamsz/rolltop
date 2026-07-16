package syncer

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestGenerationRecoveryQueueStatusIsPerUserAndSeparatesActiveTarget(t *testing.T) {
	runner := NewRunner(nil)
	runner.mu.Lock()
	runner.generationRecoveryActive[7] = generationRecoveryActivity{
		accountID: 13,
		mailbox:   "Gmail Forward",
	}
	runner.mu.Unlock()

	rebuilds := []store.PendingMailboxGenerationRebuild{
		{UserID: 7, AccountID: 13, MailboxID: 3, MailboxName: "Gmail Forward"},
		{UserID: 7, AccountID: 11, MailboxID: 1, MailboxName: "INBOX"},
		{UserID: 7, AccountID: 13, MailboxID: 2, MailboxName: "Archive"},
		{UserID: 9, AccountID: 21, MailboxID: 4, MailboxName: "Odd\nName"},
	}
	var logs []string
	runner.updateGenerationRecoveryQueueStatuses(rebuilds, time.Unix(100, 0), func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	if len(logs) != 2 {
		t.Fatalf("queue logs=%q, want one line per user", logs)
	}

	first := logs[0]
	for _, want := range []string{
		"user_id=7",
		"pending_markers=3",
		`active_target={account_id=13 mailbox="Gmail Forward"}`,
		`queued_targets=[{account_id=11 mailbox="INBOX"} {account_id=13 mailbox="Archive"}]`,
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("first queue line %q does not contain %q", first, want)
		}
	}
	if strings.Count(first, "active_target={") != 1 {
		t.Fatalf("first queue line reports more than one active target: %q", first)
	}
	if strings.Contains(first, "Odd") {
		t.Fatalf("first tenant queue leaked another tenant target: %q", first)
	}

	second := logs[1]
	for _, want := range []string{
		"user_id=9",
		"pending_markers=1",
		"active_target=none",
		`queued_targets=[{account_id=21 mailbox="Odd\nName"}]`,
	} {
		if !strings.Contains(second, want) {
			t.Fatalf("second queue line %q does not contain %q", second, want)
		}
	}
	if strings.Contains(second, "\n") {
		t.Fatalf("mailbox control character was not escaped in queue log: %q", second)
	}
}

func TestGenerationRecoveryQueueStatusLogsChangesAndRateLimitsRepeats(t *testing.T) {
	runner := NewRunner(nil)
	rebuild := store.PendingMailboxGenerationRebuild{
		UserID:      7,
		AccountID:   11,
		MailboxID:   3,
		MailboxName: "Gmail Forward",
	}
	startedAt := time.Unix(100, 0)
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	runner.updateGenerationRecoveryQueueStatuses([]store.PendingMailboxGenerationRebuild{rebuild}, startedAt, logf)
	runner.updateGenerationRecoveryQueueStatuses([]store.PendingMailboxGenerationRebuild{rebuild}, startedAt.Add(time.Minute), logf)
	if len(logs) != 1 {
		t.Fatalf("unchanged queue logs=%q, want one rate-limited line", logs)
	}

	runner.mu.Lock()
	runner.generationRecoveryActive[7] = generationRecoveryActivity{accountID: 11, mailbox: "Gmail Forward"}
	runner.mu.Unlock()
	runner.updateGenerationRecoveryQueueStatuses([]store.PendingMailboxGenerationRebuild{rebuild}, startedAt.Add(time.Minute+time.Second), logf)
	if len(logs) != 2 || !strings.Contains(logs[1], `active_target={account_id=11 mailbox="Gmail Forward"}`) {
		t.Fatalf("active queue change logs=%q", logs)
	}

	runner.updateGenerationRecoveryQueueStatuses([]store.PendingMailboxGenerationRebuild{rebuild},
		startedAt.Add(time.Minute+time.Second+generationRecoveryQueueStatusRepeatInterval), logf)
	if len(logs) != 3 {
		t.Fatalf("unchanged queue did not repeat after rate limit: %q", logs)
	}

	runner.mu.Lock()
	delete(runner.generationRecoveryActive, 7)
	runner.mu.Unlock()
	runner.updateGenerationRecoveryQueueStatuses(nil, startedAt.Add(5*time.Minute), logf)
	if len(logs) != 4 || !strings.Contains(logs[3], "pending_markers=0 active_target=none queued_targets=[]") {
		t.Fatalf("drained queue logs=%q", logs)
	}
	runner.updateGenerationRecoveryQueueStatuses(nil, startedAt.Add(10*time.Minute), logf)
	if len(logs) != 4 {
		t.Fatalf("drained queue kept logging: %q", logs)
	}
}

func TestGenerationRecoveryQueueStatusCapsRepeatedTargetList(t *testing.T) {
	runner := NewRunner(nil)
	rebuilds := make([]store.PendingMailboxGenerationRebuild, 0, 12)
	for i := 0; i < 12; i++ {
		rebuilds = append(rebuilds, store.PendingMailboxGenerationRebuild{
			UserID: 7, AccountID: int64(i + 1), MailboxID: int64(i + 1), MailboxName: fmt.Sprintf("Folder %02d", i),
		})
	}
	var logs []string
	runner.updateGenerationRecoveryQueueStatuses(rebuilds, time.Unix(100, 0), func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	if len(logs) != 1 {
		t.Fatalf("queue logs=%q, want one line", logs)
	}
	if !strings.Contains(logs[0], "pending_markers=12") ||
		!strings.Contains(logs[0], "queued_targets_omitted=4") ||
		strings.Contains(logs[0], "Folder 08") {
		t.Fatalf("capped queue log=%q", logs[0])
	}
}

func TestGenerationRecoveryQueueStatusLogsDrainAfterMarkerClearsBeforeActiveTurn(t *testing.T) {
	runner := NewRunner(nil)
	rebuild := store.PendingMailboxGenerationRebuild{
		UserID: 7, AccountID: 11, MailboxID: 3, MailboxName: "Gmail Forward",
	}
	runner.mu.Lock()
	runner.generationRecoveryActive[7] = generationRecoveryActivity{accountID: 11, mailbox: "Gmail Forward"}
	runner.mu.Unlock()

	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	startedAt := time.Unix(100, 0)
	runner.updateGenerationRecoveryQueueStatuses([]store.PendingMailboxGenerationRebuild{rebuild}, startedAt, logf)
	runner.updateGenerationRecoveryQueueStatuses(nil, startedAt.Add(time.Second), logf)
	runner.mu.Lock()
	delete(runner.generationRecoveryActive, 7)
	runner.mu.Unlock()
	runner.updateGenerationRecoveryQueueStatuses(nil, startedAt.Add(2*time.Second), logf)

	if len(logs) != 3 {
		t.Fatalf("marker-before-active drain logs=%q, want pending, finishing, and drained lines", logs)
	}
	if !strings.Contains(logs[1], "pending_markers=0") ||
		!strings.Contains(logs[1], `active_target={account_id=11 mailbox="Gmail Forward"}`) {
		t.Fatalf("finishing queue line=%q", logs[1])
	}
	if !strings.Contains(logs[2], "pending_markers=0 active_target=none queued_targets=[]") {
		t.Fatalf("final drained queue line=%q", logs[2])
	}
}

func TestGenerationRecoveryQueueStatusDistinguishesConcurrentOrdinaryMailWork(t *testing.T) {
	runner := NewRunner(nil)
	rebuild := store.PendingMailboxGenerationRebuild{
		UserID: 7, AccountID: 11, MailboxID: 3, MailboxName: "Gmail Forward",
	}
	recoveryKeys, reserved := runner.reserveGenerationRecoveryMailbox(rebuild)
	if !reserved {
		t.Fatal("generation recovery reservation was refused")
	}
	defer runner.releaseGenerationRecoveryMailbox(rebuild.UserID, recoveryKeys)
	runner.mu.Lock()
	runner.generationRecoveryKnown[rebuild.UserID] = true
	runner.generationRecoveryTargets[rebuild.UserID] = map[string]bool{
		accountMailboxKey(rebuild.UserID, rebuild.AccountID, rebuild.MailboxName): true,
	}
	runner.mu.Unlock()

	status := runner.generationRecoveryQueueStatusForUser(rebuild.UserID)
	if !strings.Contains(status, "other_mailbox_work_active=false") {
		t.Fatalf("recovery-only status=%q", status)
	}
	runner.mu.Lock()
	runner.foregroundRunning[rebuild.UserID] = 1
	runner.mu.Unlock()
	status = runner.generationRecoveryQueueStatusForUser(rebuild.UserID)
	if !strings.Contains(status, "other_mailbox_work_active=true") {
		t.Fatalf("foreground-operation status=%q", status)
	}
	runner.mu.Lock()
	delete(runner.foregroundRunning, rebuild.UserID)
	runner.autoRunning[rebuild.UserID] = true
	runner.mu.Unlock()
	status = runner.generationRecoveryQueueStatusForUser(rebuild.UserID)
	if !strings.Contains(status, "other_mailbox_work_active=true") {
		t.Fatalf("account-wide sync status=%q", status)
	}
	runner.mu.Lock()
	delete(runner.autoRunning, rebuild.UserID)
	runner.mu.Unlock()
	ordinaryKeys, reserved := runner.reserveAccountMailboxes(rebuild.UserID, 22, []string{"INBOX"})
	if !reserved {
		t.Fatal("unrelated live Inbox reservation was refused")
	}
	defer runner.releaseAccountMailboxReservations(rebuild.UserID, 22, []string{"INBOX"}, ordinaryKeys)

	status = runner.generationRecoveryQueueStatusForUser(rebuild.UserID)
	if !strings.Contains(status, "other_mailbox_work_active=true") {
		t.Fatalf("concurrent ordinary-work status=%q", status)
	}
}

func TestGenerationRecoveryQueueStatusReportsExactBlockersAndElapsedTime(t *testing.T) {
	runner := NewRunner(nil)
	userID := int64(7)
	startedAt := time.Unix(100, 0)
	mailboxStartedAt := startedAt.Add(30 * time.Second)
	mailboxReservationKey := accountMailboxKey(userID, 11, "INBOX")
	otherTenantKey := accountMailboxKey(9, 22, "Private Folder")

	runner.mu.Lock()
	runner.autoRunning[userID] = true
	runner.startWorkActivityLocked(runnerUserWorkActivityKey(runnerWorkAccountSync, userID), runnerWorkActivity{
		kind: runnerWorkAccountSync, userID: userID, startedAt: startedAt,
	})
	runner.mailboxRunning[mailboxReservationKey] = true
	runner.startWorkActivityLocked(runnerMailboxWorkActivityKey(mailboxReservationKey), runnerWorkActivity{
		kind: runnerWorkMailboxSync, userID: userID, accountID: 11, mailbox: "INBOX", startedAt: mailboxStartedAt,
	})
	runner.mailboxRunning[otherTenantKey] = true
	runner.startWorkActivityLocked(runnerMailboxWorkActivityKey(otherTenantKey), runnerWorkActivity{
		kind: runnerWorkMailboxSync, userID: 9, accountID: 22, mailbox: "Private Folder", startedAt: startedAt,
	})
	snapshot := runner.generationRecoveryQueueSnapshotLocked(userID, nil)
	runner.mu.Unlock()

	status := snapshot.status(startedAt.Add(time.Minute))
	for _, want := range []string{
		"other_mailbox_work_active=true",
		`{kind=account_wide_sync key="user:7:account_wide_sync" phase=mailboxes elapsed=1m0s}`,
		`{kind=mailbox_sync key="7:11:inbox" account_id=11 mailbox="INBOX" elapsed=30s}`,
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("blocker status %q does not contain %q", status, want)
		}
	}
	if strings.Contains(status, "Private Folder") || strings.Contains(status, "account_id=22") {
		t.Fatalf("blocker status leaked another tenant's reservation: %q", status)
	}
}

func TestGenerationRecoveryQueueStatusTracksMailboxReservationLifecycle(t *testing.T) {
	runner := NewRunner(nil)
	userID := int64(7)
	accountID := int64(11)
	mailboxes := []string{"Gmail Forward"}
	keys, reserved := runner.reserveAccountMailboxes(userID, accountID, mailboxes)
	if !reserved {
		t.Fatal("mailbox reservation was refused")
	}

	runner.mu.Lock()
	activity, tracked := runner.workActivities[runnerMailboxWorkActivityKey(keys[0])]
	runner.mu.Unlock()
	if !tracked || activity.kind != runnerWorkMailboxSync || activity.userID != userID ||
		activity.accountID != accountID || activity.mailbox != mailboxes[0] || activity.startedAt.IsZero() {
		t.Fatalf("tracked mailbox activity=%+v, present=%t", activity, tracked)
	}

	runner.releaseAccountMailboxReservations(userID, accountID, mailboxes, keys)
	runner.mu.Lock()
	_, tracked = runner.workActivities[runnerMailboxWorkActivityKey(keys[0])]
	runner.mu.Unlock()
	if tracked {
		t.Fatal("mailbox activity remained after its reservation was released")
	}
}

func TestGenerationRecoveryQueueStatusElapsedTimeDoesNotDefeatRateLimit(t *testing.T) {
	runner := NewRunner(nil)
	rebuild := store.PendingMailboxGenerationRebuild{
		UserID: 7, AccountID: 11, MailboxID: 3, MailboxName: "Gmail Forward",
	}
	startedAt := time.Unix(100, 0)
	runner.mu.Lock()
	runner.foregroundRunning[rebuild.UserID] = 1
	runner.startWorkActivityLocked(runnerUserWorkActivityKey(runnerWorkForeground, rebuild.UserID), runnerWorkActivity{
		kind: runnerWorkForeground, userID: rebuild.UserID, startedAt: startedAt,
	})
	runner.mu.Unlock()

	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	runner.updateGenerationRecoveryQueueStatuses([]store.PendingMailboxGenerationRebuild{rebuild}, startedAt, logf)
	runner.updateGenerationRecoveryQueueStatuses([]store.PendingMailboxGenerationRebuild{rebuild}, startedAt.Add(time.Minute), logf)
	if len(logs) != 1 {
		t.Fatalf("changing elapsed time bypassed rate limit: %q", logs)
	}
	runner.updateGenerationRecoveryQueueStatuses([]store.PendingMailboxGenerationRebuild{rebuild},
		startedAt.Add(generationRecoveryQueueStatusRepeatInterval), logf)
	if len(logs) != 2 || !strings.Contains(logs[1], "elapsed=2m0s") {
		t.Fatalf("rate-limited blocker repeat=%q", logs)
	}
}
