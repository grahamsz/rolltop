// File overview: Swipe-preference defaults, persistence, validation, and tenant isolation tests.

package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSwipePreferencesDefaultsAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "swipes@example.test", "Swipes", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	defaults, err := db.GetSwipePreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantDefaults := DefaultSwipePreferences(user.ID)
	if !reflect.DeepEqual(defaults, wantDefaults) {
		t.Fatalf("default swipe preferences = %+v, want %+v", defaults, wantDefaults)
	}

	firstAccount, firstArchive := createSwipeTestAccount(t, ctx, db, user, "first")
	secondAccount, secondArchive := createSwipeTestAccount(t, ctx, db, user, "second")
	saved, err := db.SaveSwipePreferences(ctx, SwipePreferences{
		UserID:            user.ID,
		LeftAction:        " ARCHIVE ",
		LeftSnoozePreset:  " NEXT_WEEK ",
		RightAction:       SwipeActionMarkUnread,
		RightSnoozePreset: SwipeSnoozeLaterToday,
		ArchiveMailboxes: []SwipeArchiveMailbox{
			{AccountID: secondAccount.ID, MailboxID: secondArchive.ID},
			{AccountID: firstAccount.ID, MailboxID: firstArchive.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := SwipePreferences{
		UserID:            user.ID,
		LeftAction:        SwipeActionArchive,
		LeftSnoozePreset:  SwipeSnoozeNextWeek,
		RightAction:       SwipeActionMarkUnread,
		RightSnoozePreset: SwipeSnoozeLaterToday,
		ArchiveMailboxes: []SwipeArchiveMailbox{
			{AccountID: firstAccount.ID, MailboxID: firstArchive.ID},
			{AccountID: secondAccount.ID, MailboxID: secondArchive.ID},
		},
	}
	if !reflect.DeepEqual(saved, want) {
		t.Fatalf("saved swipe preferences = %+v, want %+v", saved, want)
	}
	reloaded, err := db.GetSwipePreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, want) {
		t.Fatalf("reloaded swipe preferences = %+v, want %+v", reloaded, want)
	}
	if err := db.UpdateMailboxSettings(ctx, user.ID, firstArchive.ID, MailboxSettings{
		SyncMode: "manual", Role: "trash", Icon: "delete", ShowInSidebar: true,
	}); err != nil {
		t.Fatal(err)
	}
	roleChanged, err := db.GetSwipePreferences(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roleChanged.ArchiveMailboxes, []SwipeArchiveMailbox{{AccountID: secondAccount.ID, MailboxID: secondArchive.ID}}) {
		t.Fatalf("role-changed archive mapping remained active: %+v", roleChanged.ArchiveMailboxes)
	}

	replaced, err := db.SaveSwipePreferences(ctx, SwipePreferences{
		UserID:            user.ID,
		LeftAction:        SwipeActionSnooze,
		LeftSnoozePreset:  SwipeSnoozeTomorrow,
		RightAction:       SwipeActionMarkRead,
		RightSnoozePreset: SwipeSnoozeTomorrow,
		ArchiveMailboxes:  []SwipeArchiveMailbox{{AccountID: secondAccount.ID, MailboxID: secondArchive.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replaced.ArchiveMailboxes, []SwipeArchiveMailbox{{AccountID: secondAccount.ID, MailboxID: secondArchive.ID}}) {
		t.Fatalf("replacement archive mailboxes = %+v", replaced.ArchiveMailboxes)
	}
}

func TestSaveSwipePreferencesRejectsInvalidValues(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "invalid-swipes@example.test", "Invalid Swipes", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, archive := createSwipeTestAccount(t, ctx, db, user, "invalid")
	secondAccount, secondArchive := createSwipeTestAccount(t, ctx, db, user, "invalid-second")
	inbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	valid := SwipePreferences{
		UserID:            user.ID,
		LeftAction:        SwipeActionSnooze,
		LeftSnoozePreset:  SwipeSnoozeTomorrow,
		RightAction:       SwipeActionMarkRead,
		RightSnoozePreset: SwipeSnoozeTomorrow,
		ArchiveMailboxes:  []SwipeArchiveMailbox{{AccountID: account.ID, MailboxID: archive.ID}},
	}

	tests := []struct {
		name   string
		mutate func(*SwipePreferences)
	}{
		{name: "left action", mutate: func(p *SwipePreferences) { p.LeftAction = "delete_forever" }},
		{name: "right action", mutate: func(p *SwipePreferences) { p.RightAction = "toggle_read" }},
		{name: "left preset", mutate: func(p *SwipePreferences) { p.LeftSnoozePreset = "some_day" }},
		{name: "right preset", mutate: func(p *SwipePreferences) { p.RightSnoozePreset = "one_year" }},
		{name: "trash without role folders", mutate: func(p *SwipePreferences) { p.LeftAction = SwipeActionTrash }},
		{name: "archive without folder", mutate: func(p *SwipePreferences) {
			p.LeftAction = SwipeActionArchive
			p.ArchiveMailboxes = nil
		}},
		{name: "archive missing an account", mutate: func(p *SwipePreferences) {
			p.LeftAction = SwipeActionArchive
		}},
		{name: "special-role archive folder", mutate: func(p *SwipePreferences) {
			p.ArchiveMailboxes = []SwipeArchiveMailbox{
				{AccountID: account.ID, MailboxID: inbox.ID},
				{AccountID: secondAccount.ID, MailboxID: secondArchive.ID},
			}
		}},
		{name: "duplicate account targets", mutate: func(p *SwipePreferences) {
			p.ArchiveMailboxes = append(p.ArchiveMailboxes, SwipeArchiveMailbox{AccountID: account.ID, MailboxID: secondArchive.ID})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefs := valid
			prefs.ArchiveMailboxes = append([]SwipeArchiveMailbox(nil), valid.ArchiveMailboxes...)
			tt.mutate(&prefs)
			if _, err := db.SaveSwipePreferences(ctx, prefs); !errors.Is(err, ErrInvalidSwipePreferences) {
				t.Fatalf("SaveSwipePreferences error = %v, want ErrInvalidSwipePreferences", err)
			}
		})
	}
}

func TestSaveSwipePreferencesRejectsForeignAndCrossAccountMailboxes(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "owner-swipes@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other-swipes@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	firstAccount, _ := createSwipeTestAccount(t, ctx, db, user, "owner-first")
	_, secondArchive := createSwipeTestAccount(t, ctx, db, user, "owner-second")
	otherAccount, otherArchive := createSwipeTestAccount(t, ctx, db, other, "other")
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO swipe_archive_mailboxes
			(user_id, account_id, mailbox_id, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`,
		user.ID, otherAccount.ID, otherArchive.ID); err == nil {
		t.Fatal("schema accepted a cross-user archive mapping")
	}
	base := SwipePreferences{
		UserID:            user.ID,
		LeftAction:        SwipeActionArchive,
		LeftSnoozePreset:  SwipeSnoozeTomorrow,
		RightAction:       SwipeActionMarkRead,
		RightSnoozePreset: SwipeSnoozeTomorrow,
	}

	tests := []struct {
		name   string
		target SwipeArchiveMailbox
	}{
		{name: "foreign account and mailbox", target: SwipeArchiveMailbox{AccountID: otherAccount.ID, MailboxID: otherArchive.ID}},
		{name: "foreign mailbox", target: SwipeArchiveMailbox{AccountID: firstAccount.ID, MailboxID: otherArchive.ID}},
		{name: "mailbox from another owned account", target: SwipeArchiveMailbox{AccountID: firstAccount.ID, MailboxID: secondArchive.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefs := base
			prefs.ArchiveMailboxes = []SwipeArchiveMailbox{tt.target}
			if _, err := db.SaveSwipePreferences(ctx, prefs); !errors.Is(err, ErrInvalidSwipePreferences) {
				t.Fatalf("SaveSwipePreferences error = %v, want ErrInvalidSwipePreferences", err)
			}
		})
	}
}

func createSwipeTestAccount(t *testing.T, ctx context.Context, db *Store, user User, suffix string) (MailAccount, Mailbox) {
	t.Helper()
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID:            user.ID,
		Email:             suffix + "@example.test",
		Host:              "imap.example.test",
		Port:              993,
		Username:          suffix,
		EncryptedPassword: "secret",
		UseTLS:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	return account, archive
}
