// File overview: Compose/reply MIME construction and recipient selection helpers.

package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"rolltop/backend/mailparse"
	"rolltop/backend/plugins"
	"rolltop/backend/search"
	"rolltop/backend/smtpclient"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
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

func (s *Server) replyComposeFormForMessage(ctx context.Context, cu currentUser, msg store.MessageRecord, thread []store.MessageRecord, own map[string]bool) composeForm {
	bodyHTML, bodyText, _ := s.displayBodiesForMessage(ctx, cu.User.ID, msg)
	if strings.TrimSpace(bodyHTML) != "" {
		msg.BodyHTML = bodyHTML
	}
	if strings.TrimSpace(bodyText) != "" {
		msg.BodyText = bodyText
	}
	return replyComposeForm(msg, thread, own)
}

func (s *Server) replyAllComposeFormForMessage(ctx context.Context, cu currentUser, msg store.MessageRecord, thread []store.MessageRecord, own map[string]bool) composeForm {
	bodyHTML, bodyText, _ := s.displayBodiesForMessage(ctx, cu.User.ID, msg)
	if strings.TrimSpace(bodyHTML) != "" {
		msg.BodyHTML = bodyHTML
	}
	if strings.TrimSpace(bodyText) != "" {
		msg.BodyText = bodyText
	}
	return replyAllComposeForm(msg, thread, own)
}

func (s *Server) applyReplyComposeDefaults(ctx context.Context, cu currentUser, msg store.MessageRecord, thread []store.MessageRecord, form *composeForm) {
	if form == nil {
		return
	}
	form.FromIdentityID = s.replyFromIdentityID(ctx, cu, msg, thread)
	if !(msg.IsEncrypted || msg.IsSigned) {
		return
	}
	identity, err := s.selectedComposeIdentity(ctx, cu, form.FromIdentityID)
	if err != nil {
		return
	}
	form.SecurityEncrypted, form.SecuritySigned = replySecurityDefaults(identity, msg)
}

func replySecurityDefaults(identity composeIdentity, msg store.MessageRecord) (bool, bool) {
	if !identity.HasSecurityPrivateKey {
		return false, false
	}
	if msg.IsEncrypted && strings.TrimSpace(identity.SecurityPublicMaterial) != "" {
		return true, true
	}
	if msg.IsSigned {
		return false, true
	}
	return false, false
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
		BodyHTML:    quotedReplyBodyHTML(msg),
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

func (s *Server) forwardAsAttachmentComposeForm(msg store.MessageRecord) composeForm {
	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	if !strings.HasPrefix(strings.ToLower(subject), "fwd:") && !strings.HasPrefix(strings.ToLower(subject), "fw:") {
		subject = "Fwd: " + subject
	}
	return composeForm{
		Subject:              subject,
		ForwardAttachmentID:  msg.ID,
		ForwardAttachment:    forwardAttachmentMetadata(msg),
		AvailableAttachments: nil,
	}
}

// forwardComposeFormForMessage hydrates the message body from the retained raw
// blob or IMAP before building the forward draft. Message display already does
// this, and forwarding needs the same source so rich HTML mail does not fall
// back to indexed preview text or markdown-like link soup.
func (s *Server) forwardComposeFormForMessage(ctx context.Context, userID int64, msg store.MessageRecord) composeForm {
	bodyHTML, bodyText, _ := s.displayBodiesForMessage(ctx, userID, msg)
	if strings.TrimSpace(bodyHTML) != "" {
		msg.BodyHTML = bodyHTML
	}
	if strings.TrimSpace(bodyText) != "" {
		msg.BodyText = bodyText
	}
	return forwardComposeForm(msg)
}

func (s *Server) draftComposeFormForMessage(ctx context.Context, cu currentUser, msg store.MessageRecord) composeForm {
	bodyHTML, bodyText, _ := s.displayBodiesForMessage(ctx, cu.User.ID, msg)
	choices := s.composeIdentityChoices(ctx, cu)
	return composeForm{
		To:             msg.ToAddr,
		Cc:             msg.CCAddr,
		Bcc:            s.draftBccHeader(ctx, cu.User.ID, msg),
		Subject:        msg.Subject,
		Body:           bodyText,
		BodyHTML:       bodyHTML,
		DraftMessageID: msg.ID,
		FromIdentityID: identityIDForAddressValues(choices, msg.FromAddr),
	}
}

func (s *Server) draftBccHeader(ctx context.Context, userID int64, msg store.MessageRecord) string {
	raw, err := s.rawMessageBytes(ctx, userID, msg)
	if err != nil {
		return ""
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Header.Get("Bcc"))
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

func quotedReplyBodyHTML(msg store.MessageRecord) string {
	var b strings.Builder
	b.WriteString(`<br><br><div>`)
	if !msg.Date.IsZero() || strings.TrimSpace(msg.FromAddr) != "" {
		b.WriteString("On ")
		if !msg.Date.IsZero() {
			b.WriteString(html.EscapeString(msg.Date.Local().Format("Jan 2, 2006 at 3:04 PM")))
		}
		if strings.TrimSpace(msg.FromAddr) != "" {
			b.WriteString(", ")
			b.WriteString(html.EscapeString(msg.FromAddr))
		}
		b.WriteString(" wrote:")
	}
	b.WriteString(`</div><blockquote class="rolltop-reply-body">`)
	if strings.TrimSpace(msg.BodyHTML) != "" {
		b.WriteString(sanitizeComposeHTML(msg.BodyHTML))
	} else {
		b.WriteString(plainTextBodyHTML(msg.BodyText))
	}
	b.WriteString(`</blockquote>`)
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
	b.WriteString(`<br><div class="rolltop-forwarded-body">`)
	b.WriteString(sanitizeComposeHTML(msg.BodyHTML))
	b.WriteString(`</div>`)
	return b.String()
}

var (
	htmlCommentRE       = regexp.MustCompile(`(?is)<!--.*?-->`)
	blockedHTMLBlockRE  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</\s*script\s*>|<style\b[^>]*>.*?</\s*style\s*>|<head\b[^>]*>.*?</\s*head\s*>|<iframe\b[^>]*>.*?</\s*iframe\s*>|<object\b[^>]*>.*?</\s*object\s*>|<embed\b[^>]*>.*?</\s*embed\s*>|<svg\b[^>]*>.*?</\s*svg\s*>|<math\b[^>]*>.*?</\s*math\s*>`)
	blockedHTMLSingleRE = regexp.MustCompile(`(?is)</?(script|style|head|html|body|meta|link|base|iframe|object|embed|svg|math)\b[^>]*>`)
	eventAttrRE         = regexp.MustCompile(`(?is)\s+on[a-z0-9_-]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	srcAttrRE           = regexp.MustCompile(`(?is)\s+src\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	srcsetAttrRE        = regexp.MustCompile(`(?is)\s+srcset\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	backgroundAttrRE    = regexp.MustCompile(`(?is)\s+background\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	hrefAttrRE          = regexp.MustCompile(`(?is)\s+href\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	tagRE               = regexp.MustCompile(`(?is)<[^>]+>`)
)

// sanitizeComposeHTML strips active content from HTML being embedded into
// forwarded mail while preserving ordinary formatting, links, and image src
// attributes so rich forwarded messages do not collapse into garbled text.
func sanitizeComposeHTML(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	value = htmlCommentRE.ReplaceAllString(value, "")
	value = blockedHTMLBlockRE.ReplaceAllString(value, "")
	value = blockedHTMLSingleRE.ReplaceAllString(value, "")
	value = eventAttrRE.ReplaceAllString(value, "")
	value = srcsetAttrRE.ReplaceAllString(value, "")
	value = backgroundAttrRE.ReplaceAllString(value, "")
	value = srcAttrRE.ReplaceAllStringFunc(value, func(attr string) string {
		lower := strings.ToLower(strings.TrimSpace(attr))
		if strings.Contains(lower, `"javascript:`) || strings.Contains(lower, `'javascript:`) || strings.Contains(lower, `=javascript:`) ||
			strings.Contains(lower, `"data:text`) || strings.Contains(lower, `'data:text`) || strings.Contains(lower, `=data:text`) {
			return ""
		}
		return attr
	})
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
	ID                     int64
	SecurityIdentityID     int64
	SecurityPublicMaterial string
	HasSecurityPrivateKey  bool
	SMTPAccountID          int64
	IMAPAccountID          int64
	SentMailboxID          int64
	DraftsMailboxID        int64
	Signature              string
	Label                  string
	Email                  string
	Header                 string
	IconURL                string
	IsPrimary              bool
	AutocryptEnabled       bool
}

func pluginMailIdentityContext(identity composeIdentity) plugins.MailIdentityContext {
	preferences := map[string]string{
		"autocrypt_enabled": "false",
	}
	if identity.AutocryptEnabled {
		preferences["autocrypt_enabled"] = "true"
	}
	return plugins.MailIdentityContext{
		ID:                identity.SecurityIdentityID,
		Email:             identity.Email,
		HeaderDisplayName: identity.Label,
		Preferences:       preferences,
	}
}

func (s *Server) composeIdentities(ctx context.Context, cu currentUser) []apiComposeIdentity {
	choices := s.composeIdentityChoices(ctx, cu)
	out := make([]apiComposeIdentity, 0, len(choices))
	for _, choice := range choices {
		out = append(out, apiComposeIdentity{
			ID:                     choice.ID,
			SecurityIdentityID:     choice.SecurityIdentityID,
			Label:                  choice.Label,
			Email:                  choice.Email,
			Header:                 choice.Header,
			Signature:              choice.Signature,
			IconURL:                choice.IconURL,
			IsPrimary:              choice.IsPrimary,
			AutocryptEnabled:       choice.AutocryptEnabled,
			HasSecurityPrivateKey:  choice.HasSecurityPrivateKey,
			SecurityPublicMaterial: choice.SecurityPublicMaterial,
		})
	}
	return out
}

// composeIdentityChoices returns the Me-contact-backed outgoing identities that
// settings can assign to SMTP servers. Compose keeps using contact-email IDs for
// From selections, while the synchronized identity rows provide the SMTP link and
// signature data. If setup has not produced identities yet, a first-account
// fallback keeps compose readable but send will still require SMTP and Sent-folder
// configuration before it leaves the machine.
func (s *Server) composeIdentityChoices(ctx context.Context, cu currentUser) []composeIdentity {
	icons := map[int64]string{}
	if contacts, err := s.store.ListMeContactsForUser(ctx, cu.User.ID); err == nil {
		for _, contact := range contacts {
			if contact.Icon != nil {
				icons[contact.ID] = fmt.Sprintf("/contacts/%d/icon", contact.ID)
			}
		}
	}
	identities, err := s.store.ListMailIdentitiesForUser(ctx, cu.User.ID)
	if err == nil {
		out := make([]composeIdentity, 0, len(identities))
		backendPlugins, _ := s.enabledBackendPlugins(ctx)
		for _, identity := range identities {
			email := strings.TrimSpace(identity.Email)
			if email == "" {
				continue
			}
			label := firstNonEmpty(identity.DisplayName, email)
			securityPublic := ""
			hasSecurity := false
			identityCtx := plugins.MailIdentityContext{
				ID:                identity.ID,
				Email:             email,
				HeaderDisplayName: label,
				Preferences: map[string]string{
					"autocrypt_enabled": fmt.Sprintf("%t", identity.AutocryptEnabled),
				},
			}
			for _, backendPlugin := range backendPlugins {
				provider, ok := backendPlugin.(plugins.IdentitySecurityProvider)
				if !ok {
					continue
				}
				info, securityErr := provider.ComposeIdentitySecurity(ctx, s, cu.User.ID, identityCtx)
				if securityErr == nil {
					securityPublic = info.PublicMaterial
					hasSecurity = info.HasSecret
					break
				}
			}
			out = append(out, composeIdentity{
				ID:                     identity.ContactEmailID,
				SecurityIdentityID:     identity.ID,
				SecurityPublicMaterial: securityPublic,
				HasSecurityPrivateKey:  hasSecurity,
				SMTPAccountID:          identity.SMTPAccountID,
				IMAPAccountID:          identity.IMAPAccountID,
				SentMailboxID:          identity.SentMailboxID,
				DraftsMailboxID:        identity.DraftsMailboxID,
				Signature:              identity.Signature,
				Label:                  label,
				Email:                  email,
				Header:                 contactAddressHeader(label, email),
				IconURL:                icons[identity.ContactID],
				IsPrimary:              identity.IsPrimary,
				AutocryptEnabled:       identity.AutocryptEnabled,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	email := ""
	var imapAccountID int64
	if account, err := s.store.GetMailAccount(ctx, cu.User.ID); err == nil && strings.TrimSpace(account.Email) != "" {
		email = strings.TrimSpace(account.Email)
		imapAccountID = account.ID
	} else {
		email = strings.TrimSpace(cu.User.Email)
	}
	if email == "" {
		return nil
	}
	label := strings.TrimSpace(cu.User.Name)
	return []composeIdentity{{
		ID:               0,
		IMAPAccountID:    imapAccountID,
		Label:            firstNonEmpty(label, email),
		Email:            email,
		Header:           contactAddressHeader(label, email),
		IsPrimary:        true,
		AutocryptEnabled: true,
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

func (s *Server) smtpAccountForIdentity(ctx context.Context, userID int64, identity composeIdentity) (store.SMTPAccount, error) {
	if identity.SMTPAccountID > 0 {
		return s.store.GetSMTPAccountForUser(ctx, userID, identity.SMTPAccountID)
	}
	accounts, err := s.store.ListSMTPAccountsForUser(ctx, userID)
	if err != nil {
		return store.SMTPAccount{}, err
	}
	if len(accounts) == 0 {
		return store.SMTPAccount{}, fmt.Errorf("no SMTP server is configured for %s", identity.Email)
	}
	key := store.NormalizeContactEmail(identity.Email)
	for _, account := range accounts {
		if key != "" && store.NormalizeContactEmail(account.Username) == key {
			return account, nil
		}
	}
	if len(accounts) == 1 {
		return accounts[0], nil
	}
	return store.SMTPAccount{}, fmt.Errorf("no SMTP server is assigned to %s", identity.Email)
}

func (s *Server) applyPluginMailHeaders(ctx context.Context, userID int64, msg *smtpclient.Message, identity composeIdentity) {
	backendPlugins, err := s.enabledBackendPlugins(ctx)
	if err != nil {
		return
	}
	identityCtx := pluginMailIdentityContext(identity)
	for _, backendPlugin := range backendPlugins {
		provider, ok := backendPlugin.(plugins.OutboundMailHeaderProvider)
		if !ok {
			continue
		}
		headers, headerErr := provider.OutboundMailHeaders(ctx, s, userID, identityCtx)
		if headerErr != nil {
			continue
		}
		for _, header := range headers {
			msg.ExtraHeaders = append(msg.ExtraHeaders, smtpclient.Header{
				Name:  header.Name,
				Value: header.Value,
			})
		}
	}
}

func (s *Server) applyPluginMIMEBodyOverride(ctx context.Context, userID int64, msg *smtpclient.Message, identity composeIdentity, form composeForm) error {
	if !form.SecurityMIME {
		return nil
	}
	backendPlugins, err := s.enabledBackendPlugins(ctx)
	if err != nil {
		return err
	}
	identityCtx := pluginMailIdentityContext(identity)
	bodyCtx := plugins.ComposeMessageBodyContext{
		MessageID: msg.MessageID,
		BodyText:  msg.BodyText,
		BodyHTML:  msg.BodyHTML,
		Metadata:  composePluginMIMEMetadata(form),
	}
	for _, backendPlugin := range backendPlugins {
		provider, ok := backendPlugin.(plugins.ComposeMIMEBodyProvider)
		if !ok {
			continue
		}
		override, overrideErr := provider.ComposeMIMEBodyOverride(ctx, s, userID, identityCtx, bodyCtx)
		if errors.Is(overrideErr, plugins.ErrUnsupported) {
			continue
		}
		if overrideErr != nil {
			return overrideErr
		}
		if override == nil || strings.TrimSpace(override.ContentType) == "" {
			continue
		}
		msg.MIMEBodyOverride = &smtpclient.MIMEBodyOverride{
			ContentType: override.ContentType,
			Body:        override.Body,
		}
		return nil
	}
	return errors.New("requested plugin MIME body is unavailable")
}

func composePluginMIMEMetadata(form composeForm) map[string]string {
	metadata := map[string]string{
		"security_mime":      "false",
		"security_encrypted": "false",
		"security_signed":    "false",
		"security_signature": form.SecuritySignature,
		"pgp_mime":           "false",
		"pgp_encrypted":      "false",
		"pgp_signed":         "false",
		"pgp_signature":      form.SecuritySignature,
	}
	if form.SecurityMIME {
		metadata["security_mime"] = "true"
		metadata["pgp_mime"] = "true"
	}
	if form.SecurityEncrypted {
		metadata["security_encrypted"] = "true"
		metadata["pgp_encrypted"] = "true"
	}
	if form.SecuritySigned {
		metadata["security_signed"] = "true"
		metadata["pgp_signed"] = "true"
	}
	return metadata
}

func (s *Server) sentMailboxForIdentity(ctx context.Context, userID int64, identity composeIdentity, smtpAccount store.SMTPAccount) (store.MailAccount, store.Mailbox, error) {
	accounts, err := s.store.ListMailAccountsForUser(ctx, userID)
	if err != nil {
		return store.MailAccount{}, store.Mailbox{}, err
	}
	if len(accounts) == 0 {
		return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("no IMAP account is configured for %s", identity.Email)
	}
	if identity.SentMailboxID > 0 {
		mailbox, err := s.store.GetMailboxForUser(ctx, userID, identity.SentMailboxID)
		if err != nil {
			return store.MailAccount{}, store.Mailbox{}, err
		}
		if mailbox.Role != "sent" {
			return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("selected Sent folder for %s is no longer marked as Sent", identity.Email)
		}
		if identity.IMAPAccountID > 0 && mailbox.AccountID != identity.IMAPAccountID {
			return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("selected Sent folder for %s is not on the identity IMAP server", identity.Email)
		}
		account, err := s.store.GetMailAccountForUser(ctx, userID, mailbox.AccountID)
		if err != nil {
			return store.MailAccount{}, store.Mailbox{}, err
		}
		return account, mailbox, nil
	}
	candidates := mailAccountCandidatesForIdentity(accounts, identity, smtpAccount)
	if len(candidates) == 0 {
		return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("no IMAP account matches the %s identity", identity.Email)
	}
	for _, account := range candidates {
		mailbox, err := s.store.GetMailboxByRoleForAccount(ctx, userID, account.ID, "sent")
		if err == nil {
			return account, mailbox, nil
		}
		if !store.IsNotFound(err) {
			return store.MailAccount{}, store.Mailbox{}, err
		}
	}
	return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("choose a Sent folder role for the IMAP account used by %s before sending", identity.Email)
}

func (s *Server) draftsMailboxForIdentity(ctx context.Context, userID int64, identity composeIdentity) (store.MailAccount, store.Mailbox, error) {
	accounts, err := s.store.ListMailAccountsForUser(ctx, userID)
	if err != nil {
		return store.MailAccount{}, store.Mailbox{}, err
	}
	if len(accounts) == 0 {
		return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("no IMAP account is configured for %s", identity.Email)
	}
	if identity.DraftsMailboxID > 0 {
		mailbox, err := s.store.GetMailboxForUser(ctx, userID, identity.DraftsMailboxID)
		if err != nil {
			return store.MailAccount{}, store.Mailbox{}, err
		}
		if mailbox.Role != "drafts" {
			return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("selected Drafts folder for %s is no longer marked as Drafts", identity.Email)
		}
		if identity.IMAPAccountID > 0 && mailbox.AccountID != identity.IMAPAccountID {
			return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("selected Drafts folder for %s is not on the identity IMAP server", identity.Email)
		}
		account, err := s.store.GetMailAccountForUser(ctx, userID, mailbox.AccountID)
		if err != nil {
			return store.MailAccount{}, store.Mailbox{}, err
		}
		return account, mailbox, nil
	}
	candidates := mailAccountCandidatesForIdentity(accounts, identity, store.SMTPAccount{})
	if len(candidates) == 0 {
		return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("no IMAP account matches the %s identity", identity.Email)
	}
	for _, account := range candidates {
		mailbox, err := s.store.GetMailboxByRoleForAccount(ctx, userID, account.ID, "drafts")
		if err == nil {
			return account, mailbox, nil
		}
		if !store.IsNotFound(err) {
			return store.MailAccount{}, store.Mailbox{}, err
		}
	}
	return store.MailAccount{}, store.Mailbox{}, fmt.Errorf("choose a Drafts folder role for the IMAP account used by %s before saving drafts", identity.Email)
}

func mailAccountCandidatesForIdentity(accounts []store.MailAccount, identity composeIdentity, smtpAccount store.SMTPAccount) []store.MailAccount {
	if identity.IMAPAccountID > 0 {
		for _, account := range accounts {
			if account.ID == identity.IMAPAccountID {
				return []store.MailAccount{account}
			}
		}
		return nil
	}
	keys := map[string]bool{}
	for _, value := range []string{identity.Email, smtpAccount.Username} {
		if key := store.NormalizeContactEmail(value); key != "" {
			keys[key] = true
		}
	}
	seen := map[int64]bool{}
	var out []store.MailAccount
	for _, account := range accounts {
		for _, value := range []string{account.Email, account.Username} {
			if keys[store.NormalizeContactEmail(value)] && !seen[account.ID] {
				seen[account.ID] = true
				out = append(out, account)
				break
			}
		}
	}
	if len(out) == 0 && len(accounts) == 1 {
		out = append(out, accounts[0])
	}
	return out
}

func mailAccountCandidatesForAddress(accounts []store.MailAccount, email string) []store.MailAccount {
	key := store.NormalizeContactEmail(email)
	seen := map[int64]bool{}
	var out []store.MailAccount
	for _, account := range accounts {
		for _, value := range []string{account.Email, account.Username} {
			if key != "" && store.NormalizeContactEmail(value) == key && !seen[account.ID] {
				seen[account.ID] = true
				out = append(out, account)
				break
			}
		}
	}
	if len(out) == 0 && len(accounts) == 1 {
		out = append(out, accounts[0])
	}
	return out
}

func appendIdentitySignature(bodyHTML, bodyText, signature string) (string, string) {
	signature = sanitizeComposeHTML(signature)
	if strings.TrimSpace(signature) == "" {
		return bodyHTML, bodyText
	}
	if strings.TrimSpace(bodyHTML) != "" {
		bodyHTML = strings.TrimRight(bodyHTML, " \t\r\n") + `<div><br></div><div class="rolltop-signature">` + signature + `</div>`
	}
	plain := htmlSignatureText(signature)
	if plain != "" {
		if strings.TrimSpace(bodyText) != "" {
			bodyText = strings.TrimRight(bodyText, " \t\r\n") + "\n\n" + plain
		} else {
			bodyText = plain
		}
	}
	return bodyHTML, bodyText
}

var signatureBreakRE = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li)\b[^>]*>`)
var signatureTagRE = regexp.MustCompile(`(?s)<[^>]+>`)

func htmlSignatureText(signature string) string {
	text := signatureBreakRE.ReplaceAllString(signature, "\n")
	text = signatureTagRE.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func smtpEnvelopeForIdentity(identity composeIdentity, account store.SMTPAccount) store.MailAccount {
	return store.MailAccount{
		UserID:                account.UserID,
		Email:                 identity.Email,
		SMTPHost:              account.Host,
		SMTPPort:              account.Port,
		SMTPUsername:          account.Username,
		EncryptedSMTPPassword: account.EncryptedPassword,
		SMTPUseTLS:            account.UseTLS,
	}
}

func forwardAttachmentMetadata(msg store.MessageRecord) *composeExistingAttachment {
	size := msg.Size
	return &composeExistingAttachment{
		ID:          msg.ID,
		Filename:    forwardedMessageFilename(msg),
		ContentType: "message/rfc822",
		Size:        size,
	}
}

func (s *Server) composeForwardMessageAttachment(ctx context.Context, userID, messageID int64, remaining int64) (*smtpclient.Attachment, error) {
	if messageID <= 0 {
		return nil, nil
	}
	if remaining <= 0 {
		return nil, fmt.Errorf("attachments exceed compose limit")
	}
	msg, err := s.store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return nil, err
	}
	raw, err := s.rawMessageBytes(ctx, userID, msg)
	if err != nil {
		return nil, fmt.Errorf("load original message: %w", err)
	}
	if int64(len(raw)) > remaining {
		return nil, fmt.Errorf("attachments exceed compose limit")
	}
	return &smtpclient.Attachment{
		Filename:    forwardedMessageFilename(msg),
		ContentType: "message/rfc822",
		Inline:      false,
		Data:        raw,
	}, nil
}

func forwardedMessageFilename(msg store.MessageRecord) string {
	name := strings.TrimSpace(msg.Subject)
	if name == "" {
		name = "forwarded-message"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ', r == '\t':
			b.WriteByte('-')
		}
		if b.Len() >= 80 {
			break
		}
	}
	out := strings.Trim(b.String(), ".- _")
	if out == "" {
		out = "forwarded-message"
	}
	if !strings.HasSuffix(strings.ToLower(out), ".eml") {
		out += ".eml"
	}
	return out
}

func (s *Server) saveComposeDraft(ctx context.Context, cu currentUser, form composeForm) (store.MessageRecord, error) {
	identity, err := s.selectedComposeIdentity(ctx, cu, form.FromIdentityID)
	if err != nil {
		return store.MessageRecord{}, err
	}
	imapAccount, draftsMailbox, err := s.draftsMailboxForIdentity(ctx, cu.User.ID, identity)
	if err != nil {
		return store.MessageRecord{}, err
	}
	attachments, err := s.composeMessageAttachments(ctx, cu.User.ID, form)
	if err != nil {
		return store.MessageRecord{}, err
	}
	if form.SecurityEncrypted || form.SecuritySigned {
		if len(attachments) > 0 || form.AttachPublicKey {
			return store.MessageRecord{}, errors.New("message security does not support attachments yet")
		}
	} else if form.AttachPublicKey {
		attachment, err := s.composePublicKeyAttachment(ctx, cu.User.ID, identity)
		if err != nil {
			return store.MessageRecord{}, err
		}
		attachments = append(attachments, attachment)
	}
	messageID := smtpclient.NewMessageID(identity.Email)
	messageDate := time.Now()
	if form.DraftMessageID > 0 {
		if existing, err := s.store.GetMessageForUser(ctx, cu.User.ID, form.DraftMessageID); err == nil && strings.TrimSpace(existing.MessageIDHeader) != "" {
			messageID = existing.MessageIDHeader
		}
	}
	msg := smtpclient.Message{
		From:        identity.Header,
		To:          []string{form.To},
		Cc:          []string{form.Cc},
		Bcc:         []string{form.Bcc},
		Subject:     form.Subject,
		BodyText:    form.Body,
		BodyHTML:    form.BodyHTML,
		MessageID:   messageID,
		Date:        messageDate,
		Attachments: attachments,
	}
	if err := s.applyPluginMIMEBodyOverride(ctx, cu.User.ID, &msg, identity, form); err != nil {
		return store.MessageRecord{}, err
	}
	s.applyPluginMailHeaders(ctx, cu.User.ID, &msg, identity)
	raw, err := smtpclient.BuildDraftRaw(msg)
	if err != nil {
		return store.MessageRecord{}, err
	}
	fetched, err := s.appendDraftMessage(ctx, imapAccount, draftsMailbox, raw, msg.MessageID, msg.Date)
	if err != nil {
		return store.MessageRecord{}, fmt.Errorf("could not save draft to %s: %w", draftsMailbox.Name, err)
	}
	return s.storeSentMessage(ctx, cu.User.ID, imapAccount, draftsMailbox, msg, form, fetched)
}

func (s *Server) appendSentMessage(ctx context.Context, account store.MailAccount, mailbox store.Mailbox, raw []byte, messageID string, date time.Time) (syncer.FetchedMessage, error) {
	if s.syncer == nil || s.syncer.Fetcher == nil {
		return syncer.FetchedMessage{}, fmt.Errorf("IMAP sync is not configured")
	}
	fetched, err := s.syncer.Fetcher.AppendMessage(ctx, account, mailbox.Name, raw, messageID, date)
	if err != nil {
		return syncer.FetchedMessage{}, err
	}
	if fetched.UID == 0 {
		return syncer.FetchedMessage{}, fmt.Errorf("IMAP server did not confirm a UID for %s", mailbox.Name)
	}
	if len(fetched.Raw) == 0 {
		fetched.Raw = raw
	}
	if fetched.Mailbox == "" {
		fetched.Mailbox = mailbox.Name
	}
	if fetched.InternalDate.IsZero() {
		fetched.InternalDate = date
	}
	return fetched, nil
}

type flagAppendingFetcher interface {
	AppendMessageWithFlags(ctx context.Context, account store.MailAccount, mailbox string, raw []byte, messageID string, date time.Time, flags []string) (syncer.FetchedMessage, error)
}

func (s *Server) appendDraftMessage(ctx context.Context, account store.MailAccount, mailbox store.Mailbox, raw []byte, messageID string, date time.Time) (syncer.FetchedMessage, error) {
	if s.syncer == nil || s.syncer.Fetcher == nil {
		return syncer.FetchedMessage{}, fmt.Errorf("IMAP sync is not configured")
	}
	appender, ok := s.syncer.Fetcher.(flagAppendingFetcher)
	if !ok {
		return syncer.FetchedMessage{}, fmt.Errorf("IMAP sync does not support draft append")
	}
	fetched, err := appender.AppendMessageWithFlags(ctx, account, mailbox.Name, raw, messageID, date, []string{"\\Draft"})
	if err != nil {
		return syncer.FetchedMessage{}, err
	}
	if fetched.UID == 0 {
		return syncer.FetchedMessage{}, fmt.Errorf("IMAP server did not confirm a UID for %s", mailbox.Name)
	}
	if len(fetched.Raw) == 0 {
		fetched.Raw = raw
	}
	if fetched.Mailbox == "" {
		fetched.Mailbox = mailbox.Name
	}
	if fetched.InternalDate.IsZero() {
		fetched.InternalDate = date
	}
	return fetched, nil
}

func smtpHasVisibleAttachments(attachments []smtpclient.Attachment) bool {
	for _, attachment := range attachments {
		if !attachment.Inline {
			return true
		}
	}
	return false
}

func (s *Server) storeSentMessage(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox, outgoing smtpclient.Message, form composeForm, fetched syncer.FetchedMessage) (store.MessageRecord, error) {
	uid := fetched.UID
	if uid == 0 {
		return store.MessageRecord{}, fmt.Errorf("sent IMAP copy is missing a UID")
	}
	raw := fetched.Raw
	if len(raw) == 0 {
		return store.MessageRecord{}, fmt.Errorf("sent IMAP copy is missing raw message data")
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
	messageDate := outgoing.Date
	if messageDate.IsZero() {
		messageDate = time.Now()
	}
	internalDate := fetched.InternalDate
	if internalDate.IsZero() {
		internalDate = messageDate
	}
	languageCode := ""
	if !form.SecurityEncrypted && s.pluginEnabled(ctx, plugins.LanguageSearch) {
		languageCode = detectLanguageCode(form.Subject, outgoing.BodyText)
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
		Date:             messageDate,
		InternalDate:     internalDate,
		UID:              uid,
		Size:             int64(len(raw)),
		BlobPath:         saved.Path,
		BodyText:         outgoing.BodyText,
		BodyHTML:         outgoing.BodyHTML,
		HasAttachments:   smtpHasVisibleAttachments(outgoing.Attachments),
		IsEncrypted:      form.SecurityEncrypted,
		IsSigned:         form.SecuritySigned,
		IsRead:           true,
	})
	if err != nil {
		return store.MessageRecord{}, err
	}
	if err := s.store.CreateLocation(ctx, userID, msg.ID, mailbox.ID, uid); err != nil {
		return store.MessageRecord{}, err
	}
	if err := s.store.UpdateMailboxLastUID(ctx, userID, mailbox.ID, uid); err != nil {
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
