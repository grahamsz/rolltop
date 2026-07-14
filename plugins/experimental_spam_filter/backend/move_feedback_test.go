package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"rolltop/backend/plugins"
)

func TestMoveIntoAndOutOfJunkBecomePendingExplicitFeedback(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	message := createSpamTestMessage(t, ctx, st, "move-label@example.test", "Move label", 1)
	inbox, err := st.GetMailboxForUser(ctx, message.UserID, message.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	junk, err := st.GetOrCreateMailbox(ctx, message.UserID, message.AccountID, "Spam")
	if err != nil {
		t.Fatal(err)
	}
	host := &spamTestHost{st: st, userID: message.UserID}
	p := &spamFilterPlugin{}
	event := plugins.MessageMoveContext{
		UserID: message.UserID, MessageID: message.ID, MessageIDHeader: message.MessageIDHeader,
		AccountID: message.AccountID, SourceMailboxID: inbox.ID, DestinationMailboxID: junk.ID,
		From: message.FromAddr, To: message.ToAddr, Subject: message.Subject, Date: message.Date,
	}
	if err := p.ObserveMessageMove(ctx, host, event); err != nil {
		t.Fatal(err)
	}
	identity := messageIdentityKey(message.MessageIDHeader, message.FromAddr, message.ToAddr, message.Subject, message.Date)
	var label string
	if err := st.DB().QueryRowContext(ctx, `SELECT label FROM plugin_experimental_spam_pending_move_labels
		WHERE user_id = ? AND account_id = ? AND identity_key = ?`, message.UserID, message.AccountID, identity).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != feedbackSpam {
		t.Fatalf("move into Junk label = %q, want spam", label)
	}

	event.SourceMailboxID, event.DestinationMailboxID = junk.ID, inbox.ID
	if err := p.ObserveMessageMove(ctx, host, event); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT label FROM plugin_experimental_spam_pending_move_labels
		WHERE user_id = ? AND account_id = ? AND identity_key = ?`, message.UserID, message.AccountID, identity).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != feedbackHam {
		t.Fatalf("move out of Junk label = %q, want ham", label)
	}
}

func TestPendingMoveLabelsPruneExpiredAndBoundEachTenant(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "pending-bound-first@example.test", "First", 1)
	second := createSpamTestMessage(t, ctx, st, "pending-bound-second@example.test", "Second", 2)
	now := time.Now().UTC().Truncate(time.Second)
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO plugin_experimental_spam_pending_move_labels
		(user_id, account_id, identity_key, label, source_mailbox_id, destination_mailbox_id, created_at, expires_at)
		VALUES (?, ?, ?, 'spam', 1, 2, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < pendingMoveMaximumPerUser+1; index++ {
		expires := now.Add(24 * time.Hour).Unix()
		if index == 0 {
			expires = now.Add(-time.Second).Unix()
		}
		if _, err := statement.ExecContext(ctx, first.UserID, first.AccountID, fmt.Sprintf("first:%04d", index), now.Add(-time.Duration(index)*time.Second).Unix(), expires); err != nil {
			statement.Close()
			t.Fatal(err)
		}
	}
	if _, err := statement.ExecContext(ctx, second.UserID, second.AccountID, "second:keep", now.Unix(), now.Add(24*time.Hour).Unix()); err != nil {
		statement.Close()
		t.Fatal(err)
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := savePendingMoveLabel(ctx, st.DB(), first.UserID, first.AccountID, "first:new", feedbackHam, 3, 4, now); err != nil {
		t.Fatal(err)
	}
	var firstCount, expired, newest, secondCount int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_pending_move_labels WHERE user_id = ?`, first.UserID).Scan(&firstCount); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_pending_move_labels WHERE user_id = ? AND identity_key = 'first:0000'`, first.UserID).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_pending_move_labels WHERE user_id = ? AND identity_key = 'first:new'`, first.UserID).Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_pending_move_labels WHERE user_id = ?`, second.UserID).Scan(&secondCount); err != nil {
		t.Fatal(err)
	}
	if firstCount != pendingMoveMaximumPerUser || expired != 0 || newest != 1 || secondCount != 1 {
		t.Fatalf("bounded pending labels first=%d expired=%d newest=%d second=%d", firstCount, expired, newest, secondCount)
	}
}

func TestPendingMoveFeedbackIsConsumedAfterDestinationClassification(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	message := createSpamTestMessage(t, ctx, st, "consume-label@example.test", "Consume label", 1)
	identity := messageIdentityKey(message.MessageIDHeader, message.FromAddr, message.ToAddr, message.Subject, message.Date)
	if err := savePendingMoveLabel(ctx, st.DB(), message.UserID, message.AccountID, identity, feedbackSpam,
		message.MailboxID, message.MailboxID+1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	input := plugins.MessageClassificationInput{
		UserID: message.UserID, MessageID: message.ID, MessageIDHeader: message.MessageIDHeader,
		AccountID: message.AccountID, MailboxID: message.MailboxID, Date: message.Date,
		From: message.FromAddr, To: message.ToAddr, Subject: message.Subject, BodyText: "full destination content",
	}
	if err := applyPendingMoveFeedback(ctx, st.DB(), input); err != nil {
		t.Fatal(err)
	}
	if label, err := getFeedback(ctx, st.DB(), message.UserID, message.ID); err != nil || label != feedbackSpam {
		t.Fatalf("destination feedback = %q error=%v, want spam", label, err)
	}
	var pending int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_experimental_spam_pending_move_labels
		WHERE user_id = ?`, message.UserID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending labels after consume = %d", pending)
	}
	if err := applyPendingMoveFeedback(ctx, st.DB(), input); err != nil {
		t.Fatal(err)
	}
}
