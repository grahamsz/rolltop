// File overview: Sends RFC 5546 RSVP replies through the host's durable
// outbox. The reply is built as a normal MIME message with the METHOD:REPLY
// calendar object attached, then spooled to the user's blob directory and
// enqueued with EnqueueOutboxMessage, so delivery, retries, and the Sent
// folder copy all follow the standard compose path. The plugin never touches
// SMTP directly.

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"path/filepath"
	"strings"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/smtpclient"
	"rolltop/backend/store"
)

// errNoMatchingIdentity is returned when none of the user's sending
// identities matches the invite's attendee address.
var errNoMatchingIdentity = errors.New("no sending identity matches the invite attendee")

// errStaleSequence is returned when the invite changed (newer SEQUENCE)
// between the user viewing it and submitting the RSVP.
var errStaleSequence = errors.New("a newer version of this invitation arrived; reload and answer again")

// matchIdentity finds the user's sending identity whose address matches the
// invite attendee, case-insensitively. The identity supplies the From header
// and the SMTP/IMAP/Sent wiring for the reply.
func matchIdentity(ctx context.Context, st *store.Store, userID int64, attendeeAddr string) (store.MailIdentity, error) {
	identities, err := st.ListMailIdentitiesForUser(ctx, userID)
	if err != nil {
		return store.MailIdentity{}, err
	}
	want := normalizeAddr(attendeeAddr)
	for _, identity := range identities {
		if normalizeAddr(identity.Email) == want && want != "" {
			return identity, nil
		}
	}
	return store.MailIdentity{}, errNoMatchingIdentity
}

// matchEventAttendee re-parses the stored invitation and returns the attendee
// entry belonging to one of the user's identities, preferring an exact
// address match.
func matchEventAttendee(ctx context.Context, st *store.Store, userID int64, raw []byte, wantUID string, wantSequence int) (icsEvent, icsAttendee, store.MailIdentity, error) {
	for _, ev := range parseICSEvents(raw) {
		if ev.UID != wantUID || ev.Sequence != wantSequence {
			continue
		}
		identities, err := st.ListMailIdentitiesForUser(ctx, userID)
		if err != nil {
			return icsEvent{}, icsAttendee{}, store.MailIdentity{}, err
		}
		known := map[string]store.MailIdentity{}
		for _, identity := range identities {
			if addr := normalizeAddr(identity.Email); addr != "" {
				known[addr] = identity
			}
		}
		for _, att := range ev.Attendees {
			if identity, ok := known[normalizeAddr(att.Address)]; ok && att.Address != "" {
				return ev, att, identity, nil
			}
		}
		return icsEvent{}, icsAttendee{}, store.MailIdentity{},
			fmt.Errorf("%w: %s", errNoMatchingIdentity, attendeeList(ev))
	}
	return icsEvent{}, icsAttendee{}, store.MailIdentity{}, errors.New("invitation payload no longer contains the event")
}

func attendeeList(ev icsEvent) string {
	addrs := make([]string, 0, len(ev.Attendees))
	for _, att := range ev.Attendees {
		addrs = append(addrs, att.Address)
	}
	return strings.Join(addrs, ", ")
}

// checkRSVPSequence rejects the RSVP when the caller viewed an older SEQUENCE
// than the invite's current one. wantSequence < 0 skips the check.
func checkRSVPSequence(current, wantSequence int) error {
	if wantSequence >= 0 && wantSequence != current {
		return errStaleSequence
	}
	return nil
}

// rsvpSubject builds the reply subject line.
func rsvpSubject(partstat, summary string) string {
	label := partstatLabel(partstat)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return label
	}
	return label + ": " + summary
}

// rsvpBodyText builds the human-readable reply body.
func rsvpBodyText(name, email, partstat, summary string) string {
	who := strings.TrimSpace(name)
	if who == "" {
		who = email
	}
	verb := map[string]string{
		"ACCEPTED":  "accepted",
		"TENTATIVE": "tentatively accepted",
		"DECLINED":  "declined",
	}[strings.ToUpper(partstat)]
	summary = strings.TrimSpace(summary)
	if summary != "" {
		summary = " \"" + summary + "\""
	}
	return fmt.Sprintf("%s (%s) has %s the invitation%s.\n", who, email, verb, summary)
}

// sendRSVP builds the METHOD:REPLY message and enqueues it in the host
// outbox. If wantSequence >= 0 it must equal the invite's current sequence,
// otherwise the RSVP is rejected as stale.
func sendRSVP(ctx context.Context, st *store.Store, db *sql.DB, userID, inviteID int64, partstat string, wantSequence int) error {
	partstat = strings.ToUpper(strings.TrimSpace(partstat))
	if !validPartStats[partstat] {
		return fmt.Errorf("invalid RSVP response %q", partstat)
	}
	invite, err := getInvite(ctx, db, userID, inviteID)
	if err != nil {
		return err
	}
	if err := checkRSVPSequence(invite.Sequence, wantSequence); err != nil {
		return err
	}
	if strings.TrimSpace(invite.OrganizerAddr) == "" {
		return errors.New("invitation has no organizer to reply to")
	}
	raw, err := inviteRawICS(ctx, db, userID, inviteID)
	if err != nil {
		return err
	}
	ev, attendee, identity, err := matchEventAttendee(ctx, st, userID, raw, invite.ICSUID, invite.Sequence)
	if err != nil {
		return err
	}
	status := map[string]string{"ACCEPTED": inviteStatusAccepted, "TENTATIVE": inviteStatusTentative, "DECLINED": inviteStatusDeclined}[partstat]
	if invite.Status == status {
		return nil
	}
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT rsvp_revision FROM plugin_calendar_invites_invites WHERE user_id = ? AND id = ?`, userID, inviteID).Scan(&revision); err != nil {
		return err
	}
	submissionKey := fmt.Sprintf("calendar-invites:%d:%d:%d:%d:%s", userID, inviteID, invite.Sequence, revision, partstat)
	if _, err := st.GetOutboxJobBySubmission(ctx, userID, submissionKey); err == nil {
		return setInviteStatus(ctx, db, userID, inviteID, status)
	} else if !store.IsNotFound(err) && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := time.Now().UTC()
	replyICS, err := buildReplyICS(ev, attendee, partstat, now)
	if err != nil {
		return err
	}
	from := (&mail.Address{Name: identity.DisplayName, Address: identity.Email}).String()
	msg := smtpclient.Message{
		From:      from,
		To:        []string{invite.OrganizerAddr},
		Subject:   rsvpSubject(partstat, ev.Summary),
		BodyText:  rsvpBodyText(attendee.Name, attendee.Address, partstat, ev.Summary),
		MessageID: smtpclient.NewMessageID(identity.Email),
		Date:      now,
		Attachments: []smtpclient.Attachment{
			{
				Filename:    "invite.ics",
				ContentType: "text/calendar; method=REPLY",
				Data:        replyICS,
			},
		},
	}
	mimeRaw, recipients, err := smtpclient.BuildRaw(msg)
	if err != nil {
		return fmt.Errorf("could not build RSVP message: %w", err)
	}
	recipientsJSON, err := json.Marshal(recipients)
	if err != nil {
		return err
	}
	// The blob store is rooted at the data directory; UserDataDir returns
	// <dataDir>/users/<id>, so the root is two levels up. Combined (empty
	// dataDir) stores have no blob directory and cannot spool outbox mail.
	userDir := st.UserDataDir(userID)
	if strings.TrimSpace(userDir) == "" {
		return errors.New("RSVP send needs a filesystem data directory")
	}
	blobs := blob.New(filepath.Dir(filepath.Dir(userDir)))
	saved, err := blobs.SaveOutboxMessage(userID, submissionKey, mimeRaw)
	if err != nil {
		return fmt.Errorf("could not spool RSVP message: %w", err)
	}
	blobRec, err := st.CreateBlob(ctx, store.BlobRecord{
		UserID: userID,
		Kind:   "outbox-message",
		Path:   saved.Path,
		SHA256: saved.SHA256,
		Size:   saved.Size,
	})
	if err != nil {
		return err
	}
	fingerprint := store.MessageArrivalFingerprint(mimeRaw, msg.MessageID, msg.Date, int64(len(mimeRaw)))
	created := false
	defer func() {
		if created {
			return
		}
		if deleted, cleanupErr := st.DeleteBlobIfUnreferencedForUser(context.Background(), userID, blobRec.ID); cleanupErr == nil && deleted {
			_ = blobs.DeleteUserBlob(userID, saved.Path)
		}
	}()
	_, _, created, err = st.EnqueueOutboxMessage(ctx, store.OutboxEnqueue{
		UserID:          userID,
		SubmissionKey:   submissionKey,
		SMTPAccountID:   identity.SMTPAccountID,
		IMAPAccountID:   identity.IMAPAccountID,
		SentMailboxID:   identity.SentMailboxID,
		EnvelopeFrom:    identity.Email,
		RecipientsJSON:  string(recipientsJSON),
		MessageIDHeader: msg.MessageID,
		Blob:            blobRec,
		RawSHA256:       saved.SHA256,
		RawSize:         saved.Size,
		Message: store.CreateMessage{
			MessageIDHeader: msg.MessageID,
			CanonicalSHA256: fingerprint.CanonicalSHA256,
			MessageIDHash:   fingerprint.MessageIDHash,
			Subject:         msg.Subject,
			FromAddr:        msg.From,
			ToAddr:          strings.Join(msg.To, ", "),
			Date:            msg.Date,
			InternalDate:    msg.Date,
			Size:            int64(len(mimeRaw)),
			BlobPath:        saved.Path,
			BodyText:        store.MessageBodyPreview(msg.BodyText, store.DefaultMessageBodyPreviewBytes),
			IsRead:          true,
			HasAttachments:  true,
		},
		Attachments: []store.Attachment{
			{
				UserID:      userID,
				BlobID:      blobRec.ID,
				Filename:    "invite.ics",
				ContentType: "text/calendar",
				Size:        int64(len(replyICS)),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("could not queue RSVP: %w", err)
	}
	return setInviteStatus(ctx, db, userID, inviteID, status)
}
