package syncer

import (
	"context"
	"testing"

	"rolltop/backend/store"
)

func TestUpdateSyncProgressUsesProgressNotificationOnly(t *testing.T) {
	fixture := newMoveTestFixture(t)
	run, err := fixture.store.CreateSyncRun(context.Background(), fixture.userID, fixture.account.ID)
	if err != nil {
		t.Fatal(err)
	}
	contentNotifications := 0
	progressNotifications := 0
	fixture.service.Notify = func(userID int64) {
		contentNotifications++
		if userID != fixture.userID {
			t.Errorf("content notification user = %d, want %d", userID, fixture.userID)
		}
	}
	fixture.service.NotifyProgress = func(userID int64) {
		progressNotifications++
		if userID != fixture.userID {
			t.Errorf("progress notification user = %d, want %d", userID, fixture.userID)
		}
	}

	progress := store.SyncProgress{MessagesSeen: 1, MessagesTotal: 5, CurrentMailbox: "INBOX", CurrentUID: 42}
	if err := fixture.service.updateSyncProgress(context.Background(), fixture.userID, run.ID, progress); err != nil {
		t.Fatal(err)
	}
	if progressNotifications != 1 {
		t.Fatalf("progress notifications = %d, want 1", progressNotifications)
	}
	if contentNotifications != 0 {
		t.Fatalf("content notifications = %d, want 0", contentNotifications)
	}
}
