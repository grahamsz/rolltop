package web

import (
	"context"
	"testing"

	"rolltop/backend/syncer"
)

func TestPluginForegroundOperationDelegatesToSyncRunner(t *testing.T) {
	runner := syncer.NewRunner(nil)
	server := &Server{syncRunner: runner}
	const userID int64 = 71

	release, err := server.BeginForegroundOperation(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.IsRunning(userID) {
		t.Fatal("plugin foreground operation was not visible to the sync runner")
	}
	release()
	release()
	if runner.IsRunning(userID) {
		t.Fatal("plugin foreground operation remained active after release")
	}
}

func TestPluginForegroundOperationRequiresRunner(t *testing.T) {
	server := &Server{}
	if _, err := server.BeginForegroundOperation(context.Background(), 71); err == nil {
		t.Fatal("plugin foreground operation succeeded without a sync runner")
	}
}
