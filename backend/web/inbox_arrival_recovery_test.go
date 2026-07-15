package web

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestServerStartupRecoversOverdueInboxArrival(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "startup-arrival@example.test", "Startup Arrival", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	message := createNotificationTestMessage(t, ctx, db, user, 1501, "Sender <sender@example.test>", "Recovered arrival")
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, message, store.ArrivalFingerprint{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != store.ArrivalPending || decision.EventCreated {
		t.Fatalf("initial arrival decision = %+v, want pending without an event", decision)
	}
	due := time.Now().UTC().Unix() + 2
	if _, err := db.DB().ExecContext(ctx, `UPDATE pending_inbox_arrivals SET available_at = ?, updated_at = ?
		WHERE user_id = ? AND message_id = ?`, due, time.Now().UTC().Unix(), user.ID, message.ID); err != nil {
		t.Fatal(err)
	}

	service := &syncer.Service{Store: db}
	runner := syncer.NewRunnerWithContext(ctx, service)
	server, err := New(Options{Store: db, Syncer: service, SyncRunner: runner, PluginDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	changed, unsubscribe := server.events.Subscribe(user.ID)
	defer unsubscribe()

	select {
	case <-changed:
	case <-time.After(4 * time.Second):
		t.Fatal("startup recovery did not notify the user")
	}
	events, count, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(events) != 1 || events[0].MessageID != message.ID || events[0].Subject != "Recovered arrival" {
		t.Fatalf("events visible when notified = %+v count=%d, want committed recovered event", events, count)
	}
}
