package syncer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestGenerationRecoveryDiagnosticsTrackMetadataOnlyPhase(t *testing.T) {
	startedAt := time.Now().Add(-time.Second)
	diagnostics := newGenerationRecoveryDiagnostics(startedAt)
	ctx := withGenerationRecoveryDiagnostics(context.Background(), diagnostics)

	generationRecoverySetTotal(ctx, 43153)
	generationRecoveryStartMessage(ctx, 251)
	generationRecoveryPhase(ctx, "search-index-batch", "bleve")
	generationRecoveryMessageStored(ctx, 251)
	generationRecoveryMessageCompleted(ctx, 251)
	generationRecoveryCheckpoint(ctx, 250)
	generationRecoveryPhase(ctx, "sqlite-checkpoint", "")

	snapshot := diagnostics.snapshot()
	if snapshot.phase != "sqlite-checkpoint" || snapshot.detail != "" {
		t.Fatalf("phase = %q detail = %q", snapshot.phase, snapshot.detail)
	}
	if snapshot.currentUID != 251 || snapshot.checkpointUID != 250 || snapshot.turnFetched != 1 ||
		snapshot.turnStored != 1 || snapshot.snapshotTotal != 43153 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	status := snapshot.status(time.Now())
	for _, want := range []string{`phase="sqlite-checkpoint"`, "current_uid=251", "checkpoint_uid=250",
		"turn_fetched=1", "turn_stored=1", "snapshot_total=43153"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status %q does not contain %q", status, want)
		}
	}
}

func TestGenerationRecoveryHeartbeatReportsActiveStage(t *testing.T) {
	diagnostics := newGenerationRecoveryDiagnostics(time.Now())
	ctx := withGenerationRecoveryDiagnostics(context.Background(), diagnostics)
	generationRecoveryStartMessage(ctx, 25)
	generationRecoveryPhase(ctx, "search-index-batch", "bleve")

	done := make(chan struct{})
	logs := make(chan string, 1)
	go runGenerationRecoveryHeartbeat(ctx, done, time.Millisecond, 7, 11, "Gmail Forward", diagnostics,
		func() string {
			return `pending_markers=3 active_target={account_id=11 mailbox="Gmail Forward"} queued_targets=[]`
		},
		func(format string, args ...any) {
			select {
			case logs <- fmt.Sprintf(format, args...):
			default:
			}
		})

	select {
	case line := <-logs:
		close(done)
		for _, want := range []string{"user_id=7", "account_id=11", `mailbox="Gmail Forward"`,
			`phase="search-index-batch"`, "current_uid=25", `detail="bleve"`, "pending_markers=3",
			`active_target={account_id=11 mailbox="Gmail Forward"}`} {
			if !strings.Contains(line, want) {
				t.Fatalf("heartbeat %q does not contain %q", line, want)
			}
		}
	case <-time.After(time.Second):
		close(done)
		t.Fatal("generation recovery heartbeat was not emitted")
	}
}

func TestAccountMailboxBlockReasonIncludesRecoveryStage(t *testing.T) {
	runner := NewRunner(nil)
	rebuild := store.PendingMailboxGenerationRebuild{
		UserID:      7,
		AccountID:   11,
		MailboxName: "Gmail Forward",
	}
	keys, reserved := runner.reserveGenerationRecoveryMailbox(rebuild)
	if !reserved {
		t.Fatal("generation recovery reservation was refused")
	}
	defer runner.releaseGenerationRecoveryMailbox(rebuild.UserID, keys)
	runner.mu.Lock()
	runner.generationRecoveryKnown[rebuild.UserID] = true
	runner.generationRecoveryTargets[rebuild.UserID] = map[string]bool{
		accountMailboxKey(rebuild.UserID, rebuild.AccountID, rebuild.MailboxName): true,
	}
	runner.mu.Unlock()

	diagnostics := runner.generationRecoveryDiagnosticsForUser(rebuild.UserID)
	ctx := withGenerationRecoveryDiagnostics(context.Background(), diagnostics)
	generationRecoveryStartMessage(ctx, 25)
	generationRecoveryPhase(ctx, "search-index-batch", "bleve")

	reason := runner.AccountMailboxBlockReason(rebuild.UserID, rebuild.AccountID, rebuild.MailboxName)
	for _, want := range []string{`mailbox="Gmail Forward"`, `phase="search-index-batch"`,
		"current_uid=25", `detail="bleve"`} {
		if !strings.Contains(reason, want) {
			t.Fatalf("block reason %q does not contain %q", reason, want)
		}
	}
	if unrelated := runner.AccountMailboxBlockReason(rebuild.UserID, 99, "INBOX"); strings.Contains(unrelated, "generation recovery") {
		t.Fatalf("unrelated Inbox block reason blamed generation recovery: %q", unrelated)
	}
}
