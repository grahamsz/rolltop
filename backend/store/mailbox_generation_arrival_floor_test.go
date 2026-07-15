package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMailboxGenerationResetPersistsArrivalUIDFloorAndNeverMovesItForward(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, account, mailbox := createMailboxGenerationLifecycleFixture(t, db, "arrival-floor")
	createMailboxGenerationLifecycleMessage(t, db, user.ID, account.ID, mailbox.ID, 10, 100, "arrival-floor")
	if _, reset, err := db.ResetMailboxForRemoteGeneration(ctx, user.ID, account.ID, mailbox.ID, 200, 41); err != nil || !reset {
		t.Fatalf("reset=%t err=%v, want true/nil", reset, err)
	}
	floor, err := db.MailboxGenerationRebuildArrivalUIDFloor(ctx, user.ID, account.ID, mailbox.ID, 200)
	if err != nil || floor != 41 {
		t.Fatalf("arrival floor=%d err=%v, want 41/nil", floor, err)
	}
	floor, err = db.InitializeMailboxGenerationRebuildArrivalUIDFloor(ctx, user.ID, account.ID, mailbox.ID, 200, 99)
	if err != nil || floor != 41 {
		t.Fatalf("reinitialized arrival floor=%d err=%v, want durable 41/nil", floor, err)
	}
}

func TestMailboxGenerationResetRejectsZeroArrivalUIDFloorWithoutMutation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, account, mailbox := createMailboxGenerationLifecycleFixture(t, db, "zero-arrival-floor")
	message := createMailboxGenerationLifecycleMessage(t, db, user.ID, account.ID, mailbox.ID, 10, 100, "zero-arrival-floor")
	stale, reset, err := db.ResetMailboxForRemoteGeneration(ctx, user.ID, account.ID, mailbox.ID, 200, 0)
	if !errors.Is(err, ErrMailboxGenerationArrivalUIDFloorRequired) || reset || len(stale) != 0 {
		t.Fatalf("zero-floor reset stale=%d reset=%t err=%v", len(stale), reset, err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 100 || mailbox.LastUID != 0 {
		t.Fatalf("rejected reset mutated mailbox uidvalidity=%d last_uid=%d", mailbox.UIDValidity, mailbox.LastUID)
	}
	if _, err := db.GetMessageForUser(ctx, user.ID, message.ID); err != nil {
		t.Fatalf("rejected reset removed message: %v", err)
	}
	if exists, err := db.MailboxGenerationRebuildExists(ctx, user.ID, account.ID, mailbox.ID); err != nil || exists {
		t.Fatalf("rejected reset marker exists=%t err=%v", exists, err)
	}
}

func TestMailboxGenerationArrivalUIDFloorInitializesLegacyMarkerWithinTenant(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, account, mailbox := createMailboxGenerationLifecycleFixture(t, db, "arrival-floor-owner")
	other, _, _ := createMailboxGenerationLifecycleFixture(t, db, "arrival-floor-other")
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO mailbox_generation_rebuilds
		(user_id, account_id, mailbox_id, target_uid_validity, created_at, updated_at)
		VALUES (?, ?, ?, 300, 1, 1)`, owner.ID, account.ID, mailbox.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.InitializeMailboxGenerationRebuildArrivalUIDFloor(ctx, other.ID, account.ID, mailbox.ID, 300, 51); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant floor initialization error=%v, want not found", err)
	}
	floor, err := db.MailboxGenerationRebuildArrivalUIDFloor(ctx, owner.ID, account.ID, mailbox.ID, 300)
	if err != nil || floor != 0 {
		t.Fatalf("owner floor after cross-tenant attempt=%d err=%v, want 0/nil", floor, err)
	}
	floor, err = db.InitializeMailboxGenerationRebuildArrivalUIDFloor(ctx, owner.ID, account.ID, mailbox.ID, 300, 51)
	if err != nil || floor != 51 {
		t.Fatalf("owner initialized floor=%d err=%v, want 51/nil", floor, err)
	}
	if _, err := db.MailboxGenerationRebuildArrivalUIDFloor(ctx, other.ID, account.ID, mailbox.ID, 300); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant floor lookup error=%v, want not found", err)
	}
}

func TestMailboxGenerationArrivalCandidatesRequireExactTenantAndGeneration(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, account, mailbox := createMailboxGenerationLifecycleFixture(t, db, "arrival-candidates-owner")
	other, _, _ := createMailboxGenerationLifecycleFixture(t, db, "arrival-candidates-other")
	createMailboxGenerationLifecycleMessage(t, db, owner.ID, account.ID, mailbox.ID, 10, 100, "arrival-candidate-old")
	if _, reset, err := db.ResetMailboxForRemoteGeneration(ctx, owner.ID, account.ID, mailbox.ID, 200, 41); err != nil || !reset {
		t.Fatalf("reset=%t err=%v, want true/nil", reset, err)
	}
	message := createMailboxGenerationLifecycleMessage(t, db, owner.ID, account.ID, mailbox.ID, 41, 200, "arrival-candidate")

	candidates, err := db.ListMailboxGenerationArrivalCandidates(ctx, owner.ID, account.ID, mailbox.ID, 200, 41)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ID != message.ID {
		t.Fatalf("owner candidates=%+v, want message %d", candidates, message.ID)
	}
	for name, scope := range map[string]struct {
		userID      int64
		uidValidity uint32
		floor       uint32
	}{
		"other tenant":     {userID: other.ID, uidValidity: 200, floor: 41},
		"other generation": {userID: owner.ID, uidValidity: 201, floor: 41},
		"other floor":      {userID: owner.ID, uidValidity: 200, floor: 42},
	} {
		candidates, err := db.ListMailboxGenerationArrivalCandidates(ctx, scope.userID,
			account.ID, mailbox.ID, scope.uidValidity, scope.floor)
		if err != nil {
			t.Fatalf("%s candidates: %v", name, err)
		}
		if len(candidates) != 0 {
			t.Fatalf("%s candidates=%+v, want none", name, candidates)
		}
	}
}
