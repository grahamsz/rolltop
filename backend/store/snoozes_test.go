package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSnoozeLifecycleVisibilityReminderAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "snooze-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "snooze-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	snoozedMessage := createNewMailEventMessage(t, ctx, db, owner, 701, "Alice <alice@example.test>", "Snooze me")
	normalMessage := createNewMailEventMessage(t, ctx, db, owner, 702, "Bob <bob@example.test>", "Keep visible")
	otherMessage := createNewMailEventMessage(t, ctx, db, other, 801, "Secret <secret@example.test>", "Other tenant")
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET date_unix = ? WHERE user_id = ? AND id = ?`, time.Now().Add(-48*time.Hour).Unix(), owner.ID, snoozedMessage.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET date_unix = ? WHERE user_id = ? AND id = ?`, time.Now().Add(-24*time.Hour).Unix(), owner.ID, normalMessage.ID); err != nil {
		t.Fatal(err)
	}

	first, err := db.SnoozeMessage(ctx, owner.ID, snoozedMessage.ID, time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.SnoozeMessage(ctx, owner.ID, snoozedMessage.ID, time.Now().Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Generation != first.Generation+1 {
		t.Fatalf("resnooze = %+v, first = %+v", second, first)
	}
	if _, err := db.SnoozeMessage(ctx, other.ID, snoozedMessage.ID, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("another user snoozed the owner's message")
	}

	visible, err := db.ListLatestThreadMessagesForUser(ctx, owner.ID, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if messageSliceContains(visible, snoozedMessage.ID) || !messageSliceContains(visible, normalMessage.ID) {
		t.Fatalf("owner visible messages = %v", messageRecordIDs(visible))
	}
	searchVisible, err := db.ListUnsnoozedMessagesByIDsForUser(ctx, owner.ID, []int64{snoozedMessage.ID, normalMessage.ID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if messageSliceContains(searchVisible, snoozedMessage.ID) || !messageSliceContains(searchVisible, normalMessage.ID) {
		t.Fatalf("owner search-visible messages = %v", messageRecordIDs(searchVisible))
	}
	otherVisible, err := db.ListLatestThreadMessagesForUser(ctx, other.ID, 20, 0)
	if err != nil || len(otherVisible) != 1 || otherVisible[0].ID != otherMessage.ID {
		t.Fatalf("other visible messages = %v err=%v", messageRecordIDs(otherVisible), err)
	}
	active, err := db.ListActiveSnoozedMessagesForUser(ctx, owner.ID, 20, 0, time.Now())
	if err != nil || len(active) != 1 || active[0].Message.ID != snoozedMessage.ID {
		t.Fatalf("active snoozes = %+v err=%v", active, err)
	}
	mailboxes, err := db.ListMailboxesForUser(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mailbox := range mailboxes {
		if mailbox.ID == snoozedMessage.MailboxID && (mailbox.MessageCount != 0 || mailbox.UnreadCount != 0) {
			t.Fatalf("snoozed mailbox counts = messages:%d unread:%d", mailbox.MessageCount, mailbox.UnreadCount)
		}
	}

	dueAt := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_snoozes SET snoozed_until = ?, reminded_at = 0 WHERE user_id = ? AND id = ?`, dueAt.Unix(), owner.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	events, err := db.RecordDueSnoozeReminderEvents(ctx, owner.ID, time.Now(), 100)
	if err != nil || len(events) != 1 || events[0].MessageID != snoozedMessage.ID || events[0].SnoozeGeneration != second.Generation {
		t.Fatalf("due events = %+v err=%v", events, err)
	}
	duplicate, err := db.RecordDueSnoozeReminderEvents(ctx, owner.ID, time.Now(), 100)
	if err != nil || len(duplicate) != 0 {
		t.Fatalf("duplicate due events = %+v err=%v", duplicate, err)
	}
	reminders, count, cursor, err := db.SnoozeReminderEventsAfter(ctx, owner.ID, 0, 5)
	if err != nil || count != 1 || len(reminders) != 1 || cursor != events[0].ID {
		t.Fatalf("owner reminders = %+v count=%d cursor=%d err=%v", reminders, count, cursor, err)
	}
	otherReminders, otherCount, _, err := db.SnoozeReminderEventsAfter(ctx, other.ID, 0, 5)
	if err != nil || otherCount != 0 || len(otherReminders) != 0 {
		t.Fatalf("other reminders = %+v count=%d err=%v", otherReminders, otherCount, err)
	}
	resurfaced, err := db.ListLatestThreadMessagesForUser(ctx, owner.ID, 20, 0)
	if err != nil || len(resurfaced) < 2 || resurfaced[0].ID != snoozedMessage.ID {
		t.Fatalf("resurfaced order = %v err=%v", messageRecordIDs(resurfaced), err)
	}
	if acknowledged, err := db.AcknowledgeDueSnoozeForUser(ctx, other.ID, snoozedMessage.ID, time.Now()); err == nil || acknowledged {
		t.Fatalf("other acknowledgment = %t err=%v", acknowledged, err)
	}
	acknowledged, err := db.AcknowledgeDueSnoozeForUser(ctx, owner.ID, snoozedMessage.ID, time.Now())
	if err != nil || !acknowledged {
		t.Fatalf("owner acknowledgment = %t err=%v", acknowledged, err)
	}
	afterAck, err := db.ListLatestThreadMessagesForUser(ctx, owner.ID, 20, 0)
	if err != nil || len(afterAck) < 2 || afterAck[0].ID != normalMessage.ID {
		t.Fatalf("post-ack order = %v err=%v", messageRecordIDs(afterAck), err)
	}
	reminders, count, _, err = db.SnoozeReminderEventsAfter(ctx, owner.ID, 0, 5)
	if err != nil || count != 0 || len(reminders) != 0 {
		t.Fatalf("obsolete reminders after acknowledgment = %+v count=%d err=%v", reminders, count, err)
	}
}

func TestSnoozeEmptyStoredThreadKeyMatchesListQueries(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "empty-thread-snooze@example.test", "Empty", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	message := createNewMailEventMessage(t, ctx, db, user, 1401, "Alice", "Legacy key")
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET thread_key = '' WHERE user_id = ? AND id = ?`, user.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	message.ThreadKey = ""
	if _, err := db.SnoozeMessage(ctx, user.ID, message.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	visible, err := db.ListLatestThreadMessagesForUser(ctx, user.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if messageSliceContains(visible, message.ID) {
		t.Fatal("empty stored thread key snooze did not hide its message")
	}
}

func TestResnoozeWithNewThreadAnchorRemovesObsoleteReminderEvent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "resnooze-anchor@example.test", "Anchor", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	root := createNewMailEventMessage(t, ctx, db, user, 1501, "Alice", "Anchor root")
	reply := createNewMailEventMessage(t, ctx, db, user, 1502, "Alice", "Re: Anchor root")
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET thread_key = ? WHERE user_id = ? AND id = ?`, root.ThreadKey, user.ID, reply.ID); err != nil {
		t.Fatal(err)
	}
	reply.ThreadKey = root.ThreadKey
	if _, err := db.SnoozeMessage(ctx, user.ID, root.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_snoozes SET snoozed_until = ? WHERE user_id = ?`, time.Now().Add(-time.Minute).Unix(), user.ID); err != nil {
		t.Fatal(err)
	}
	if events, err := db.RecordDueSnoozeReminderEvents(ctx, user.ID, time.Now(), 10); err != nil || len(events) != 1 {
		t.Fatalf("due events = %+v err=%v", events, err)
	}
	snooze, err := db.SnoozeMessage(ctx, user.ID, reply.ID, time.Now().Add(2*time.Hour))
	if err != nil || snooze.MessageID != reply.ID {
		t.Fatalf("resnooze = %+v err=%v", snooze, err)
	}
	events, count, _, err := db.SnoozeReminderEventsAfter(ctx, user.ID, 0, 5)
	if err != nil || count != 0 || len(events) != 0 {
		t.Fatalf("obsolete anchor reminders = %+v count=%d err=%v", events, count, err)
	}
}

func TestNewMailEventAtomicallyCancelsOnlyCurrentActiveConversationSnooze(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "arrival-snooze@example.test", "Arrival", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	root := createNewMailEventMessage(t, ctx, db, user, 901, "Alice <alice@example.test>", "Project")
	if _, err := db.SnoozeMessage(ctx, user.ID, root.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	reply := createNewMailEventMessage(t, ctx, db, user, 902, "Alice <alice@example.test>", "Re: Project")
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET thread_key = ? WHERE user_id = ? AND id = ?`, root.ThreadKey, user.ID, reply.ID); err != nil {
		t.Fatal(err)
	}
	reply.ThreadKey = root.ThreadKey
	if _, created, err := db.RecordNewMailEvent(ctx, user.ID, reply); err != nil || !created {
		t.Fatalf("record new reply created=%t err=%v", created, err)
	}
	if _, err := db.MessageSnoozeForUser(ctx, user.ID, root.ID); err == nil {
		t.Fatal("new reply left the active conversation snoozed")
	}

	if _, err := db.SnoozeMessage(ctx, user.ID, root.ID, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, created, err := db.RecordNewMailEvent(ctx, user.ID, reply); err != nil || created {
		t.Fatalf("duplicate reply event created=%t err=%v", created, err)
	}
	if _, err := db.MessageSnoozeForUser(ctx, user.ID, root.ID); err != nil {
		t.Fatalf("duplicate event cleared a later snooze: %v", err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_snoozes SET snoozed_until = ? WHERE user_id = ?`, time.Now().Add(-time.Minute).Unix(), user.ID); err != nil {
		t.Fatal(err)
	}
	if events, err := db.RecordDueSnoozeReminderEvents(ctx, user.ID, time.Now(), 10); err != nil || len(events) != 1 {
		t.Fatalf("due reminders before reply = %+v err=%v", events, err)
	}
	third := createNewMailEventMessage(t, ctx, db, user, 903, "Alice <alice@example.test>", "Re: Project again")
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET thread_key = ? WHERE user_id = ? AND id = ?`, root.ThreadKey, user.ID, third.ID); err != nil {
		t.Fatal(err)
	}
	third.ThreadKey = root.ThreadKey
	if _, created, err := db.RecordNewMailEvent(ctx, user.ID, third); err != nil || !created {
		t.Fatalf("third reply event created=%t err=%v", created, err)
	}
	if _, err := db.MessageSnoozeForUser(ctx, user.ID, root.ID); err == nil {
		t.Fatal("new reply left a due conversation snoozed")
	}
	if events, count, _, err := db.SnoozeReminderEventsAfter(ctx, user.ID, 0, 5); err != nil || count != 0 || len(events) != 0 {
		t.Fatalf("new reply left obsolete reminders = %+v count=%d err=%v", events, count, err)
	}
}

func TestSnoozeReminderWebPushCursorIsIndependentAndUserScoped(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "snooze-push@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "snooze-push-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	message := createNewMailEventMessage(t, ctx, db, owner, 1001, "Alice", "Reminder")
	if _, err := db.SnoozeMessage(ctx, owner.ID, message.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_snoozes SET snoozed_until = ? WHERE user_id = ?`, time.Now().Add(-time.Minute).Unix(), owner.ID); err != nil {
		t.Fatal(err)
	}
	firstEvents, err := db.RecordDueSnoozeReminderEvents(ctx, owner.ID, time.Now(), 10)
	if err != nil || len(firstEvents) != 1 {
		t.Fatalf("first events = %+v err=%v", firstEvents, err)
	}
	ownerSub, err := db.SaveWebPushSubscription(ctx, owner.ID, WebPushSubscription{
		Endpoint: "https://push.example.test/snooze-owner", P256DH: testWebPushValues(21), Auth: testWebPushAuth(21),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ownerSub.LastSnoozeReminderEventID != firstEvents[0].ID || ownerSub.LastNewMailEventID != 0 {
		t.Fatalf("subscription baselines = %+v", ownerSub)
	}
	otherSub, err := db.SaveWebPushSubscription(ctx, other.ID, WebPushSubscription{
		Endpoint: "https://push.example.test/snooze-other", P256DH: testWebPushValues(22), Auth: testWebPushAuth(22),
	})
	if err != nil {
		t.Fatal(err)
	}
	if advanced, err := db.AdvanceWebPushSubscriptionSnoozeReminderCursor(ctx, other.ID, ownerSub, firstEvents[0].ID+1); err != nil || advanced {
		t.Fatalf("cross-user cursor advanced=%t err=%v", advanced, err)
	}
	otherSubs, err := db.ListWebPushSubscriptions(ctx, other.ID)
	if err != nil || len(otherSubs) != 1 || otherSubs[0].ID != otherSub.ID || otherSubs[0].LastSnoozeReminderEventID != 0 {
		t.Fatalf("other subscriptions = %+v err=%v", otherSubs, err)
	}
}

func messageSliceContains(messages []MessageRecord, id int64) bool {
	for _, message := range messages {
		if message.ID == id {
			return true
		}
	}
	return false
}

func messageRecordIDs(messages []MessageRecord) []int64 {
	out := make([]int64, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.ID)
	}
	return out
}
