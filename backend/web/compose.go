// File overview: Compose/reply MIME construction and recipient selection helpers.

package web

import (
	"context"
	"fmt"
	"html"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"mailmirror/backend/mailparse"
	"mailmirror/backend/plugins"
	languagesearch "mailmirror/backend/plugins/language_search"
	"mailmirror/backend/search"
	"mailmirror/backend/smtpclient"
	"mailmirror/backend/store"
)

// replyComposeForm builds the server default for reply compose. The frontend may
// refine recipient/identity hints, but the backend still owns quoted body and
// message threading fields.
func replyComposeForm(msg store.MessageRecord, thread []store.MessageRecord, own map[string]bool) composeForm {
	return replyComposeFormWithRecipients(msg, replyRecipient(msg, thread, own), "")
}

// replyAllComposeForm includes the sender plus every non-Me To/Cc recipient,
// preserving the same quoted body and threading fields as a normal reply.
func replyAllComposeForm(msg store.MessageRecord, thread []store.MessageRecord, own map[string]bool) composeForm {
	to, cc := replyAllRecipients(msg, thread, own)
	return replyComposeFormWithRecipients(msg, to, cc)
}

func replyComposeFormWithRecipients(msg store.MessageRecord, to, cc string) composeForm {
	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	return composeForm{
		To:          to,
		Cc:          cc,
		Subject:     subject,
		Body:        quotedReplyBody(msg),
		BodyHTML:    "",
		InReplyToID: msg.ID,
	}
}

// replyRecipient avoids replying to the user's own address. For sent/self messages
// it prefers external To/Cc recipients, then walks backward to the latest external
// sender in the thread.
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

func replyAllRecipients(msg store.MessageRecord, thread []store.MessageRecord, own map[string]bool) (string, string) {
	seen := map[string]bool{}
	var to []string
	var cc []string
	if messageFromOwnAddress(msg, own) {
		appendNonOwnAddresses(&to, seen, own, msg.ToAddr)
		appendNonOwnAddresses(&cc, seen, own, msg.CCAddr)
	} else {
		appendNonOwnAddresses(&to, seen, own, msg.FromAddr)
		appendNonOwnAddresses(&cc, seen, own, msg.ToAddr, msg.CCAddr)
	}
	if len(to) == 0 {
		appendNonOwnAddresses(&to, seen, own, replyRecipient(msg, thread, own))
	}
	return strings.Join(to, ", "), strings.Join(cc, ", ")
}

func canReplyAll(msg store.MessageRecord, thread []store.MessageRecord, own map[string]bool) bool {
	to, cc := replyAllRecipients(msg, thread, own)
	return replyAddressCount(to)+replyAddressCount(cc) > 1 || strings.TrimSpace(cc) != ""
}

func replyAddressCount(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	if addrs, err := mail.ParseAddressList(value); err == nil {
		return len(addrs)
	}
	return len(addressIdentities(value))
}

func appendNonOwnAddresses(out *[]string, seen map[string]bool, own map[string]bool, values ...string) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if addrs, err := mail.ParseAddressList(value); err == nil {
			for _, addr := range addrs {
				identity := store.SenderIdentity(addr.Address)
				if identity == "" || own[identity] || seen[identity] {
					continue
				}
				seen[identity] = true
				*out = append(*out, addr.String())
			}
			continue
		}
		identity := store.SenderIdentity(value)
		if identity == "" || own[identity] || seen[identity] {
			continue
		}
		seen[identity] = true
		*out = append(*out, value)
	}
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

// sanitizeComposeHTML strips active/remote content from HTML being embedded into
// forwarded mail while preserving ordinary formatting and safe links.
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
	if identity, err := s.selectedComposeIdentity(ctx, cu, 0); err == nil && strings.TrimSpace(identity.Header) != "" {
		return identity.Header
	}
	if account, err := s.store.GetMailAccount(ctx, cu.User.ID); err == nil && strings.TrimSpace(account.Email) != "" {
		return strings.TrimSpace(account.Email)
	}
	if strings.TrimSpace(cu.User.Email) != "" {
		return strings.TrimSpace(cu.User.Email)
	}
	return "Not configured"
}

type composeIdentity struct {
	ID        int64
	Label     string
	Email     string
	Header    string
	IconURL   string
	IsPrimary bool
}

func (s *Server) composeIdentities(ctx context.Context, cu currentUser) []apiComposeIdentity {
	choices := s.composeIdentityChoices(ctx, cu)
	out := make([]apiComposeIdentity, 0, len(choices))
	for _, choice := range choices {
		out = append(out, apiComposeIdentity{
			ID:        choice.ID,
			Label:     choice.Label,
			Email:     choice.Email,
			Header:    choice.Header,
			IconURL:   choice.IconURL,
			IsPrimary: choice.IsPrimary,
		})
	}
	return out
}

// composeIdentityChoices derives outgoing identities from Me contacts first. If
// contacts are not configured yet, it falls back to the IMAP account/user email so
// first-run compose still has a usable From address.
func (s *Server) composeIdentityChoices(ctx context.Context, cu currentUser) []composeIdentity {
	me, err := s.store.ListMeContactsForUser(ctx, cu.User.ID)
	if err == nil {
		var out []composeIdentity
		for _, contact := range me {
			label := strings.TrimSpace(contact.DisplayName)
			if label == "" {
				label = strings.TrimSpace(contact.GivenName + " " + contact.FamilyName)
			}
			iconURL := ""
			if contact.Icon != nil {
				iconURL = fmt.Sprintf("/contacts/%d/icon", contact.ID)
			}
			for _, contactEmail := range contact.Emails {
				email := strings.TrimSpace(contactEmail.Email)
				if email == "" {
					continue
				}
				out = append(out, composeIdentity{
					ID:        contactEmail.ID,
					Label:     firstNonEmpty(label, email),
					Email:     email,
					Header:    contactAddressHeader(label, email),
					IconURL:   iconURL,
					IsPrimary: contact.IsPrimary && contactEmail.IsPrimary,
				})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	email := ""
	if account, err := s.store.GetMailAccount(ctx, cu.User.ID); err == nil && strings.TrimSpace(account.Email) != "" {
		email = strings.TrimSpace(account.Email)
	} else {
		email = strings.TrimSpace(cu.User.Email)
	}
	if email == "" {
		return nil
	}
	label := strings.TrimSpace(cu.User.Name)
	return []composeIdentity{{
		ID:        0,
		Label:     firstNonEmpty(label, email),
		Email:     email,
		Header:    contactAddressHeader(label, email),
		IsPrimary: true,
	}}
}

// selectedComposeIdentity resolves a requested identity ID, otherwise primary,
// otherwise first available identity. It rejects unknown IDs instead of accepting
// arbitrary user input.
func (s *Server) selectedComposeIdentity(ctx context.Context, cu currentUser, id int64) (composeIdentity, error) {
	choices := s.composeIdentityChoices(ctx, cu)
	if len(choices) == 0 {
		return composeIdentity{}, store.ErrNotFound
	}
	if id > 0 {
		for _, choice := range choices {
			if choice.ID == id {
				return choice, nil
			}
		}
		return composeIdentity{}, store.ErrNotFound
	}
	for _, choice := range choices {
		if choice.IsPrimary {
			return choice, nil
		}
	}
	return choices[0], nil
}

func (s *Server) replyFromIdentityID(ctx context.Context, cu currentUser, msg store.MessageRecord, thread []store.MessageRecord) int64 {
	choices := s.composeIdentityChoices(ctx, cu)
	if len(choices) == 0 {
		return 0
	}
	own := s.ownAddresses(ctx, cu.User)
	if !messageFromOwnAddress(msg, own) {
		if id := identityIDForAddressValues(choices, msg.ToAddr, msg.CCAddr); id > 0 {
			return id
		}
	}
	if id := identityIDForAddressValues(choices, msg.FromAddr); id > 0 {
		return id
	}
	if id := identityIDForAddressValues(choices, msg.ToAddr, msg.CCAddr); id > 0 {
		return id
	}
	for i := len(thread) - 1; i >= 0; i-- {
		candidate := thread[i]
		if candidate.ID == msg.ID {
			continue
		}
		if messageFromOwnAddress(candidate, own) {
			if id := identityIDForAddressValues(choices, candidate.FromAddr); id > 0 {
				return id
			}
			continue
		}
		if id := identityIDForAddressValues(choices, candidate.ToAddr, candidate.CCAddr); id > 0 {
			return id
		}
	}
	return 0
}

func identityIDForAddressValues(choices []composeIdentity, values ...string) int64 {
	ids := map[string]int64{}
	for _, choice := range choices {
		if key := store.NormalizeContactEmail(choice.Email); key != "" {
			ids[key] = choice.ID
		}
	}
	for _, value := range values {
		for _, identity := range addressIdentities(value) {
			if id := ids[identity]; id > 0 {
				return id
			}
		}
	}
	return 0
}

func primaryContactEmail(contact store.Contact) string {
	for _, email := range contact.Emails {
		if email.IsPrimary && strings.TrimSpace(email.Email) != "" {
			return strings.TrimSpace(email.Email)
		}
	}
	for _, email := range contact.Emails {
		if strings.TrimSpace(email.Email) != "" {
			return strings.TrimSpace(email.Email)
		}
	}
	return ""
}

func contactAddressHeader(label, email string) string {
	email = strings.TrimSpace(email)
	label = strings.TrimSpace(label)
	if email == "" {
		return ""
	}
	if label == "" || strings.EqualFold(label, email) {
		return email
	}
	return (&mail.Address{Name: label, Address: email}).String()
}

func smtpHasVisibleAttachments(attachments []smtpclient.Attachment) bool {
	for _, attachment := range attachments {
		if !attachment.Inline {
			return true
		}
	}
	return false
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
	languageCode := ""
	if s.pluginEnabled(ctx, plugins.LanguageSearch) {
		languageCode = languagesearch.DetectCode(form.Subject, form.Body)
	}
	msg, err := s.store.CreateMessage(ctx, store.CreateMessage{
		UserID:           userID,
		AccountID:        account.ID,
		MailboxID:        mailbox.ID,
		BlobID:           blobRec.ID,
		MessageIDHeader:  outgoing.MessageID,
		InReplyTo:        outgoing.InReplyTo,
		ReferencesHeader: outgoing.References,
		Subject:          form.Subject,
		LanguageCode:     languageCode,
		FromAddr:         outgoing.From,
		ToAddr:           form.To,
		CCAddr:           form.Cc,
		Date:             now,
		InternalDate:     now,
		UID:              uid,
		Size:             int64(len(raw)),
		BlobPath:         saved.Path,
		BodyText:         form.Body,
		BodyHTML:         form.BodyHTML,
		HasAttachments:   smtpHasVisibleAttachments(outgoing.Attachments),
		IsRead:           true,
	})
	if err != nil {
		return store.MessageRecord{}, err
	}
	if err := s.store.CreateLocation(ctx, userID, msg.ID, mailbox.ID, uid); err != nil {
		return store.MessageRecord{}, err
	}
	attachmentDocs := []search.AttachmentDoc{}
	if len(outgoing.Attachments) > 0 {
		parsed, err := mailparse.Parse(raw)
		if err != nil {
			return store.MessageRecord{}, err
		}
		visibleAttachmentCount := 0
		for _, file := range parsed.Files {
			if _, err := s.store.CreateAttachment(ctx, store.Attachment{
				UserID:      userID,
				MessageID:   msg.ID,
				BlobID:      blobRec.ID,
				Filename:    file.Filename,
				ContentType: file.ContentType,
				ContentID:   file.ContentID,
				IsInline:    file.IsInline,
				Size:        int64(len(file.Data)),
				BlobPath:    "",
			}); err != nil {
				return store.MessageRecord{}, err
			}
			if !file.IsInline {
				visibleAttachmentCount++
				attachmentDocs = append(attachmentDocs, search.AttachmentDoc{
					Filename:    file.Filename,
					ContentType: file.ContentType,
					Text:        file.SearchableText(),
				})
			}
		}
		msg.HasAttachments = visibleAttachmentCount > 0
		if err := s.store.MarkMessageAttachmentIndexed(ctx, userID, msg.ID, msg.HasAttachments); err != nil {
			return store.MessageRecord{}, err
		}
	}
	if s.search != nil {
		if err := s.search.IndexMessage(ctx, msg, attachmentDocs); err != nil {
			return store.MessageRecord{}, err
		}
	}
	return msg, nil
}
