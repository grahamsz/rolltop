package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"rolltop/backend/plugins"
)

func (p *spamFilterPlugin) ObserveMessageMove(ctx context.Context, host plugins.BackendHost, event plugins.MessageMoveContext) error {
	st, db, err := pluginUserDB(ctx, host, event.UserID)
	if err != nil {
		return err
	}
	source, err := st.GetMailboxForUser(ctx, event.UserID, event.SourceMailboxID)
	if err != nil {
		return err
	}
	destination, err := st.GetMailboxForUser(ctx, event.UserID, event.DestinationMailboxID)
	if err != nil {
		return err
	}
	if source.AccountID != event.AccountID || destination.AccountID != event.AccountID || source.UserID != event.UserID || destination.UserID != event.UserID {
		return plugins.ErrUnsupported
	}
	sourceJunk := isJunkMailbox(source.Role, source.Name)
	destinationJunk := isJunkMailbox(destination.Role, destination.Name)
	label := ""
	switch {
	case !sourceJunk && destinationJunk:
		label = feedbackSpam
	case sourceJunk && !destinationJunk:
		label = feedbackHam
	default:
		return plugins.ErrUnsupported
	}
	identity := messageIdentityKey(event.MessageIDHeader, event.From, event.To, event.Subject, event.Date)
	if identity == "" {
		return plugins.ErrUnsupported
	}
	return savePendingMoveLabel(ctx, db, event.UserID, event.AccountID, identity, label,
		event.SourceMailboxID, event.DestinationMailboxID, time.Now().UTC())
}

func isJunkMailbox(role, name string) bool {
	if strings.EqualFold(strings.TrimSpace(role), "junk") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "spam", "junk", "junk e-mail", "junk email", "[gmail]/spam", "[gmail]/junk":
		return true
	default:
		return false
	}
}

func messageIdentityKey(messageID, from, to, subject string, date time.Time) string {
	messageID = strings.ToLower(strings.TrimSpace(messageID))
	if messageID != "" {
		return "message-id:" + messageID
	}
	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))
	subject = strings.ToLower(strings.Join(strings.Fields(subject), " "))
	if from == "" && to == "" && subject == "" && date.IsZero() {
		return ""
	}
	payload := fmt.Sprintf("v1\x00%s\x00%s\x00%s\x00%d", from, to, subject, date.UTC().Unix())
	digest := sha256.Sum256([]byte(payload))
	return "envelope:" + hex.EncodeToString(digest[:])
}
