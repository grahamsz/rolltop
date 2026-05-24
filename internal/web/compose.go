package web

import (
	"context"
	"html"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"mailmirror/internal/language"
	"mailmirror/internal/search"
	"mailmirror/internal/smtpclient"
	"mailmirror/internal/store"
)

func replyComposeForm(msg store.MessageRecord, thread []store.MessageRecord, own map[string]bool) composeForm {
	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	return composeForm{
		To:          replyRecipient(msg, thread, own),
		Subject:     subject,
		Body:        quotedReplyBody(msg),
		BodyHTML:    "",
		InReplyToID: msg.ID,
	}
}

func replyRecipient(msg store.MessageRecord, thread []store.MessageRecord, own map[string]bool) string {
	if !messageFromOwnAddress(msg, own) {
		return msg.FromAddr
	}
	if recipient := firstNonOwnAddress([]string{msg.ToAddr, msg.CCAddr}, own); recipient != "" {
		return recipient
	}
	for i := len(thread) - 1; i >= 0; i-- {
		candidate := thread[i]
		if candidate.ID == msg.ID || messageFromOwnAddress(candidate, own) {
			continue
		}
		if recipient := firstNonOwnAddress([]string{candidate.FromAddr}, own); recipient != "" {
			return recipient
		}
	}
	return msg.FromAddr
}

func messageFromOwnAddress(msg store.MessageRecord, own map[string]bool) bool {
	for _, identity := range addressIdentities(msg.FromAddr) {
		if own[identity] {
			return true
		}
	}
	return false
}

func firstNonOwnAddress(values []string, own map[string]bool) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if addrs, err := mail.ParseAddressList(value); err == nil {
			for _, addr := range addrs {
				if !own[store.SenderIdentity(addr.Address)] {
					return addr.String()
				}
			}
			continue
		}
		if identity := store.SenderIdentity(value); identity != "" && !own[identity] {
			return value
		}
	}
	return ""
}

func forwardComposeForm(msg store.MessageRecord) composeForm {
	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	if !strings.HasPrefix(strings.ToLower(subject), "fwd:") && !strings.HasPrefix(strings.ToLower(subject), "fw:") {
		subject = "Fwd: " + subject
	}
	return composeForm{
		Subject:  subject,
		Body:     forwardedBodyText(msg),
		BodyHTML: forwardedBodyHTML(msg),
	}
}

func quotedReplyBody(msg store.MessageRecord) string {
	var b strings.Builder
	b.WriteString("\n\n")
	if !msg.Date.IsZero() || strings.TrimSpace(msg.FromAddr) != "" {
		b.WriteString("On ")
		if !msg.Date.IsZero() {
			b.WriteString(msg.Date.Local().Format("Jan 2, 2006 at 3:04 PM"))
		}
		if strings.TrimSpace(msg.FromAddr) != "" {
			b.WriteString(", ")
			b.WriteString(msg.FromAddr)
		}
		b.WriteString(" wrote:\n")
	}
	for _, line := range strings.Split(strings.ReplaceAll(msg.BodyText, "\r\n", "\n"), "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func forwardedBodyText(msg store.MessageRecord) string {
	var b strings.Builder
	b.WriteString("\n\n---------- Forwarded message ---------\n")
	if strings.TrimSpace(msg.FromAddr) != "" {
		b.WriteString("From: ")
		b.WriteString(msg.FromAddr)
		b.WriteString("\n")
	}
	if !msg.Date.IsZero() {
		b.WriteString("Date: ")
		b.WriteString(msg.Date.Local().Format("Mon, Jan 2, 2006 at 3:04 PM"))
		b.WriteString("\n")
	}
	if strings.TrimSpace(msg.Subject) != "" {
		b.WriteString("Subject: ")
		b.WriteString(msg.Subject)
		b.WriteString("\n")
	}
	if strings.TrimSpace(msg.ToAddr) != "" {
		b.WriteString("To: ")
		b.WriteString(msg.ToAddr)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	body := strings.TrimSpace(msg.BodyText)
	if strings.TrimSpace(msg.BodyHTML) != "" {
		body = visibleTextFromHTML(msg.BodyHTML)
	}
	b.WriteString(body)
	return b.String()
}

func forwardedBodyHTML(msg store.MessageRecord) string {
	if strings.TrimSpace(msg.BodyHTML) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<br><br><div>---------- Forwarded message ---------</div>`)
	if strings.TrimSpace(msg.FromAddr) != "" {
		b.WriteString(`<div><strong>From:</strong> `)
		b.WriteString(html.EscapeString(msg.FromAddr))
		b.WriteString(`</div>`)
	}
	if !msg.Date.IsZero() {
		b.WriteString(`<div><strong>Date:</strong> `)
		b.WriteString(html.EscapeString(msg.Date.Local().Format("Mon, Jan 2, 2006 at 3:04 PM")))
		b.WriteString(`</div>`)
	}
	if strings.TrimSpace(msg.Subject) != "" {
		b.WriteString(`<div><strong>Subject:</strong> `)
		b.WriteString(html.EscapeString(msg.Subject))
		b.WriteString(`</div>`)
	}
	if strings.TrimSpace(msg.ToAddr) != "" {
		b.WriteString(`<div><strong>To:</strong> `)
		b.WriteString(html.EscapeString(msg.ToAddr))
		b.WriteString(`</div>`)
	}
	b.WriteString(`<br><div class="mailmirror-forwarded-body">`)
	b.WriteString(sanitizeComposeHTML(msg.BodyHTML))
	b.WriteString(`</div>`)
	return b.String()
}

var (
	htmlCommentRE       = regexp.MustCompile(`(?is)<!--.*?-->`)
	blockedHTMLBlockRE  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</\s*script\s*>|<style\b[^>]*>.*?</\s*style\s*>|<head\b[^>]*>.*?</\s*head\s*>|<iframe\b[^>]*>.*?</\s*iframe\s*>|<object\b[^>]*>.*?</\s*object\s*>|<embed\b[^>]*>.*?</\s*embed\s*>|<svg\b[^>]*>.*?</\s*svg\s*>|<math\b[^>]*>.*?</\s*math\s*>`)
	blockedHTMLSingleRE = regexp.MustCompile(`(?is)</?(script|style|head|html|body|meta|link|base|iframe|object|embed|svg|math)\b[^>]*>`)
	eventAttrRE         = regexp.MustCompile(`(?is)\s+on[a-z0-9_-]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	remoteAttrRE        = regexp.MustCompile(`(?is)\s+(src|srcset|background)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	hrefAttrRE          = regexp.MustCompile(`(?is)\s+href\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	tagRE               = regexp.MustCompile(`(?is)<[^>]+>`)
)

func sanitizeComposeHTML(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	value = htmlCommentRE.ReplaceAllString(value, "")
	value = blockedHTMLBlockRE.ReplaceAllString(value, "")
	value = blockedHTMLSingleRE.ReplaceAllString(value, "")
	value = eventAttrRE.ReplaceAllString(value, "")
	value = remoteAttrRE.ReplaceAllString(value, "")
	value = hrefAttrRE.ReplaceAllStringFunc(value, func(attr string) string {
		lower := strings.ToLower(strings.TrimSpace(attr))
		if strings.Contains(lower, `"javascript:`) || strings.Contains(lower, `'javascript:`) || strings.Contains(lower, `=javascript:`) ||
			strings.Contains(lower, `"data:`) || strings.Contains(lower, `'data:`) || strings.Contains(lower, `=data:`) {
			return ` href="#"`
		}
		return attr
	})
	return strings.TrimSpace(value)
}

func visibleTextFromHTML(value string) string {
	value = sanitizeComposeHTML(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = regexp.MustCompile(`(?i)<\s*br\s*/?\s*>`).ReplaceAllString(value, "\n")
	value = regexp.MustCompile(`(?i)</\s*(p|div|tr|li|h[1-6]|blockquote)\s*>`).ReplaceAllString(value, "\n")
	value = tagRE.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func referencesForReply(msg store.MessageRecord) string {
	parts := strings.Fields(strings.TrimSpace(msg.ReferencesHeader))
	if strings.TrimSpace(msg.MessageIDHeader) != "" {
		parts = append(parts, strings.TrimSpace(msg.MessageIDHeader))
	}
	return strings.Join(parts, " ")
}

func (s *Server) composeFromLabel(ctx context.Context, cu currentUser) string {
	if account, err := s.store.GetMailAccount(ctx, cu.User.ID); err == nil && strings.TrimSpace(account.Email) != "" {
		return strings.TrimSpace(account.Email)
	}
	if strings.TrimSpace(cu.User.Email) != "" {
		return strings.TrimSpace(cu.User.Email)
	}
	return "Not configured"
}

func (s *Server) storeSentMessage(ctx context.Context, userID int64, account store.MailAccount, outgoing smtpclient.Message, form composeForm, raw []byte) (store.MessageRecord, error) {
	mailbox, err := s.store.GetOrCreateMailbox(ctx, userID, account.ID, "Sent")
	if err != nil {
		return store.MessageRecord{}, err
	}
	uid, err := s.store.NextUIDForMailbox(ctx, userID, mailbox.ID)
	if err != nil {
		return store.MessageRecord{}, err
	}
	saved, err := s.blobs.SaveRawMessage(userID, account.ID, mailbox.Name, uid, raw)
	if err != nil {
		return store.MessageRecord{}, err
	}
	blobRec, err := s.store.CreateBlob(ctx, store.BlobRecord{
		UserID: userID,
		Kind:   "message",
		Path:   saved.Path,
		SHA256: saved.SHA256,
		Size:   saved.Size,
	})
	if err != nil {
		return store.MessageRecord{}, err
	}
	now := time.Now()
	msg, err := s.store.CreateMessage(ctx, store.CreateMessage{
		UserID:           userID,
		AccountID:        account.ID,
		MailboxID:        mailbox.ID,
		BlobID:           blobRec.ID,
		MessageIDHeader:  outgoing.MessageID,
		InReplyTo:        outgoing.InReplyTo,
		ReferencesHeader: outgoing.References,
		Subject:          form.Subject,
		LanguageCode:     language.DetectCode(form.Subject, form.Body),
		FromAddr:         account.Email,
		ToAddr:           form.To,
		CCAddr:           form.Cc,
		Date:             now,
		InternalDate:     now,
		UID:              uid,
		Size:             int64(len(raw)),
		BlobPath:         saved.Path,
		BodyText:         form.Body,
		BodyHTML:         form.BodyHTML,
		IsRead:           true,
	})
	if err != nil {
		return store.MessageRecord{}, err
	}
	if err := s.store.CreateLocation(ctx, userID, msg.ID, mailbox.ID, uid); err != nil {
		return store.MessageRecord{}, err
	}
	if s.search != nil {
		if err := s.search.IndexMessage(ctx, msg, []search.AttachmentDoc{}); err != nil {
			return store.MessageRecord{}, err
		}
	}
	return msg, nil
}
