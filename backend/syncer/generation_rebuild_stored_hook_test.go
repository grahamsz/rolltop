package syncer_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"rolltop/backend/syncer"
)

const captureStoredHookPluginID = "capture_stored_hook"

func TestGenerationRebuildRunsStoredMessageHookOnlyForPostFloorArrival(t *testing.T) {
	fixture := newGenerationPrewarmFixture(t, "generation-hook-floor@example.test")
	if _, err := fixture.db.DB().ExecContext(fixture.ctx, `CREATE TABLE test_stored_message_hook_calls (
		user_id INTEGER NOT NULL,
		uid INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := fixture.db.DB().ExecContext(fixture.ctx, `INSERT INTO plugin_settings
		(id, name, description, enabled, enabled_by_default, heavy, created_at, updated_at)
		VALUES (?, 'Capture stored hook', 'Test-only stored-message hook', 1, 0, 0, ?, ?)`,
		captureStoredHookPluginID, now, now); err != nil {
		t.Fatal(err)
	}

	fixture.service = &syncer.Service{
		Store:     fixture.db,
		Blobs:     fixture.service.Blobs,
		PluginDir: buildCaptureStoredHookPlugin(t),
	}
	remote := generationPrewarmMessages(fixture.firstRaw, 205)
	allMessages := generationPrewarmMessages(fixture.firstRaw, 206)
	base := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{fixture.user.ID: remote},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	fetcher := &generationPrewarmFetcher{fakeFetcher: base}
	injected := false
	fetcher.beforeSnapshot = func() {
		if injected {
			return
		}
		floor, err := fixture.db.MailboxGenerationRebuildArrivalUIDFloor(fixture.ctx, fixture.user.ID,
			fixture.account.ID, fixture.mailbox.ID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if floor != 206 {
			t.Fatalf("arrival UID floor = %d, want 206", floor)
		}
		injected = true
		base.messages[fixture.user.ID] = append(base.messages[fixture.user.ID], allMessages[205])
	}
	fixture.service.Fetcher = fetcher
	fixture.service.ScheduleInboxArrival = func(int64, int64, time.Time) {}

	if _, err := fixture.service.SyncUserAccountMailboxes(fixture.ctx, fixture.user.ID,
		fixture.account.ID, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	if !injected {
		t.Fatal("post-floor arrival was not injected")
	}
	local, err := fixture.db.CountMessagesForMailbox(fixture.ctx, fixture.user.ID, fixture.mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if local != 206 {
		t.Fatalf("stored messages = %d, want 206", local)
	}

	rows, err := fixture.db.DB().QueryContext(fixture.ctx, `SELECT uid
		FROM test_stored_message_hook_calls WHERE user_id = ? ORDER BY uid`, fixture.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var calledUIDs []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			t.Fatal(err)
		}
		calledUIDs = append(calledUIDs, uid)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(calledUIDs) != 1 || calledUIDs[0] != 206 {
		t.Fatalf("stored-message hook UIDs = %v, want only post-floor UID 206", calledUIDs)
	}
}

func buildCaptureStoredHookPlugin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	backendDir := filepath.Join(root, captureStoredHookPluginID, "backend")
	if err := os.MkdirAll(backendDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"id": "capture_stored_hook",
		"name": "Capture stored hook",
		"description": "Test-only stored-message hook",
		"backend": {"kind": "go-plugin", "binary": "backend/capture_stored_hook.so"}
	}`
	if err := os.WriteFile(filepath.Join(root, captureStoredHookPluginID, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	source := `package main

import (
	"context"
	"errors"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

type captureStoredHook struct{}

func RolltopPlugin() plugins.BackendPlugin { return captureStoredHook{} }
func (captureStoredHook) ID() string { return "capture_stored_hook" }
func (captureStoredHook) Start(plugins.BackendStartHost) error { return nil }
func (captureStoredHook) Stop(plugins.BackendStartHost) error { return nil }

func (captureStoredHook) ImportStoredMessage(ctx context.Context, host plugins.StoredMessageHost, msg plugins.StoredMessageContext) error {
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		return errors.New("capture stored hook requires a Rolltop store")
	}
	db, err := st.UserDB(ctx, msg.UserID)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO test_stored_message_hook_calls (user_id, uid) VALUES (?, ?)", msg.UserID, msg.UID)
	return err
}
`
	sourcePath := filepath.Join(backendDir, "main.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate syncer test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	binaryPath := filepath.Join(backendDir, "capture_stored_hook.so")
	cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", binaryPath, sourcePath)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build capture stored-message plugin: %v\n%s", err, out)
	}
	return root
}
