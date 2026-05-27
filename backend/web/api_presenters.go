// File overview: Converters from store models into frontend API response shapes.

package web

import (
	"context"
	"fmt"
	"strings"
	"time"

	mmcrypto "mailmirror/backend/crypto"
	"mailmirror/backend/plugins"
	"mailmirror/backend/store"
)

func safeUser(user store.User) apiUser {
	return apiUser{
		ID:                     user.ID,
		Email:                  user.Email,
		Name:                   user.Name,
		IsAdmin:                user.IsAdmin,
		DateLocale:             user.DateLocale,
		DateFormat:             user.DateFormat,
		Theme:                  user.Theme,
		SearchPreset:           user.SearchPreset,
		SearchRecencyBias:      user.SearchRecencyBias,
		SearchFuzzy:            user.SearchFuzzy,
		SearchSenderBoost:      user.SearchSenderBoost,
		SearchSenderHistory:    user.SearchSenderHistory,
		SearchContactBoost:     user.SearchContactBoost,
		SearchAttachmentWeight: user.SearchAttachmentWeight,
		SearchCompactSplitting: user.SearchCompactSplitting,
	}
}

func apiMailboxes(boxes []store.MailboxSummary) []apiMailbox {
	out := make([]apiMailbox, 0, len(boxes))
	for _, box := range boxes {
		out = append(out, apiMailboxFromSummary(box))
	}
	return out
}

func apiMailboxFromSummary(box store.MailboxSummary) apiMailbox {
	return apiMailbox{
		ID:                 box.ID,
		AccountID:          box.AccountID,
		AccountEmail:       box.AccountEmail,
		AccountLabel:       box.AccountLabel,
		Name:               box.Name,
		MessageCount:       box.MessageCount,
		UnreadCount:        box.UnreadCount,
		SyncMode:           box.SyncMode,
		Role:               box.Role,
		Icon:               box.Icon,
		ShowInSidebar:      box.ShowInSidebar,
		ShowInAllMail:      box.ShowInAllMail,
		IncludeInSearch:    box.IncludeInSearch,
		LastUID:            box.LastUID,
		RemoteMessageCount: box.RemoteMessageCount,
		RemoteUnreadCount:  box.RemoteUnreadCount,
		RemoteUIDNext:      box.RemoteUIDNext,
		SyncPercent:        box.SyncPercent,
		LocalMessageCount:  box.LocalMessageCount,
		LocalSyncPercent:   box.LocalSyncPercent,
		SearchIndexedCount: box.SearchIndexedCount,
		SearchIndexTotal:   box.SearchIndexTotal,
		SearchIndexPercent: box.SearchIndexPercent,
	}
}

func apiMailboxFromStore(box store.Mailbox) apiMailbox {
	return apiMailbox{
		ID:                 box.ID,
		AccountID:          box.AccountID,
		AccountLabel:       "",
		Name:               box.Name,
		SyncMode:           box.SyncMode,
		Role:               box.Role,
		Icon:               box.Icon,
		ShowInSidebar:      box.ShowInSidebar,
		ShowInAllMail:      box.ShowInAllMail,
		IncludeInSearch:    box.IncludeInSearch,
		LastUID:            box.LastUID,
		RemoteMessageCount: box.RemoteMessageCount,
		RemoteUnreadCount:  box.RemoteUnreadCount,
		RemoteUIDNext:      box.RemoteUIDNext,
	}
}

func apiAccountFromStore(account store.MailAccount) apiAccount {
	return apiAccount{
		ID:                  account.ID,
		Email:               account.Email,
		Label:               account.Label,
		Host:                account.Host,
		Port:                account.Port,
		Username:            account.Username,
		UseTLS:              account.UseTLS,
		SMTPHost:            account.SMTPHost,
		SMTPPort:            account.SMTPPort,
		SMTPUsername:        account.SMTPUsername,
		SMTPUseTLS:          account.SMTPUseTLS,
		SMTPSameAsIMAP:      sameSMTPSettings(account),
		Mailbox:             account.Mailbox,
		SyncIntervalMinutes: account.SyncIntervalMinutes,
	}
}

func apiAccountsFromStore(accounts []store.MailAccount) []apiAccount {
	out := make([]apiAccount, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, apiAccountFromStore(account))
	}
	return out
}

func apiSMTPAccountFromStore(account store.SMTPAccount) apiSMTPAccount {
	return apiSMTPAccount{
		ID:       account.ID,
		Label:    account.Label,
		Host:     account.Host,
		Port:     account.Port,
		Username: account.Username,
		UseTLS:   account.UseTLS,
	}
}

func apiSMTPAccountsFromStore(accounts []store.SMTPAccount) []apiSMTPAccount {
	out := make([]apiSMTPAccount, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, apiSMTPAccountFromStore(account))
	}
	return out
}

func apiMailIdentityFromStore(identity store.MailIdentity) apiMailIdentity {
	return apiMailIdentity{
		ID:              identity.ID,
		ContactID:       identity.ContactID,
		ContactEmailID:  identity.ContactEmailID,
		SMTPAccountID:   identity.SMTPAccountID,
		IMAPAccountID:   identity.IMAPAccountID,
		SentMailboxID:   identity.SentMailboxID,
		DraftsMailboxID: identity.DraftsMailboxID,
		Email:           identity.Email,
		DisplayName:     identity.DisplayName,
		Signature:       identity.Signature,
		IsPrimary:       identity.IsPrimary,
	}
}

func apiMailIdentitiesFromStore(identities []store.MailIdentity) []apiMailIdentity {
	out := make([]apiMailIdentity, 0, len(identities))
	for _, identity := range identities {
		out = append(out, apiMailIdentityFromStore(identity))
	}
	return out
}

func sameSMTPSettings(account store.MailAccount) bool {
	return strings.EqualFold(strings.TrimSpace(account.SMTPHost), strings.TrimSpace(account.Host)) &&
		strings.TrimSpace(account.SMTPUsername) == strings.TrimSpace(account.Username) &&
		account.EncryptedSMTPPassword == account.EncryptedPassword &&
		account.SMTPUseTLS == account.UseTLS
}

func (s *Server) accountCredentialNotice(ctx context.Context, userID int64) (bool, string) {
	accounts, err := s.store.ListMailAccountsForUser(ctx, userID)
	if err != nil || len(accounts) == 0 {
		return false, ""
	}
	for _, account := range accounts {
		if strings.TrimSpace(account.EncryptedPassword) == "" {
			continue
		}
		if _, err := mmcrypto.DecryptString(s.masterKey, account.EncryptedPassword); err != nil {
			label := strings.TrimSpace(account.Email)
			if label == "" {
				label = strings.TrimSpace(account.Username)
			}
			if label == "" {
				label = "one IMAP account"
			}
			return true, fmt.Sprintf("IMAP password required for %s: the saved password cannot be decrypted with the current MAILMIRROR_MASTER_KEY. Re-enter the IMAP password to restore sync and full-message loading.", label)
		}
	}
	return false, ""
}

func apiMessageFromRecord(msg store.MessageRecord, snippet string) apiMessage {
	return apiMessage{
		ID:             msg.ID,
		AccountID:      msg.AccountID,
		MailboxID:      msg.MailboxID,
		Subject:        msg.Subject,
		FromAddr:       msg.FromAddr,
		ToAddr:         msg.ToAddr,
		CCAddr:         msg.CCAddr,
		Date:           timeString(msg.Date),
		DateShort:      shortDateString(msg.Date),
		IsRead:         msg.IsRead,
		IsStarred:      msg.IsStarred,
		HasAttachments: msg.HasAttachments,
		Snippet:        snippet,
	}
}

func apiConversations(conversations []conversationView) []apiConversation {
	out := make([]apiConversation, 0, len(conversations))
	for _, conv := range conversations {
		out = append(out, apiConversation{
			Message:                  apiMessageFromRecord(conv.Message, conv.Snippet),
			StarredMessageID:         conv.StarredMessageID,
			Participants:             conv.Participants,
			RecipientParticipants:    conv.RecipientParticipants,
			Count:                    conv.Count,
			IsRead:                   conv.IsRead,
			HasAttachments:           conv.HasAttachments,
			AttachmentNames:          conv.AttachmentNames,
			AttachmentMatches:        conv.AttachmentMatches,
			AttachmentContentMatched: conv.AttachmentContentMatched,
			Snippet:                  conv.Snippet,
			MatchTerms:               conv.MatchTerms,
			MatchQueryTerms:          conv.MatchQueryTerms,
		})
	}
	return out
}

func (s *Server) apiThreadMessages(ctx context.Context, userID int64, views []threadMessageView) []apiThreadMessage {
	out := make([]apiThreadMessage, 0, len(views))
	attachmentPreviewEnabled := s.pluginEnabled(ctx, plugins.AttachmentPreview)
	for _, view := range views {
		atts := make([]apiAttachment, 0, len(view.Attachments))
		for _, att := range view.Attachments {
			name := attachmentDisplayName(att)
			nameMatched := stringInSliceFold(name, view.AttachmentMatches)
			contentMatched := view.AttachmentContentMatched
			var matchTerms []string
			if nameMatched || contentMatched {
				matchTerms = view.AttachmentMatchTerms
			}
			var preview *apiAttachmentPreview
			if attachmentPreviewEnabled {
				preview = s.attachmentPreview(att)
			}
			atts = append(atts, apiAttachment{
				ID:             att.ID,
				Filename:       att.Filename,
				ContentType:    att.ContentType,
				Size:           att.Size,
				DownloadURL:    fmt.Sprintf("/attachments/%d/download", att.ID),
				Matched:        nameMatched || contentMatched,
				ContentMatched: contentMatched,
				MatchTerms:     matchTerms,
				Preview:        preview,
			})
		}
		var senderVisual *apiSenderVisual
		if visual, ok := s.senderVisual(ctx, userID, view.SenderEmail); ok {
			v := visual
			senderVisual = &v
		}
		fullDoc := ""
		if view.HasHiddenQuoted {
			fullDoc = emailDocumentWithInlineAttachments(view.Message.BodyHTML, view.Message.BodyText, view.ImagesAllowed, view.ImageBlockRules, view.InlineAttachments)
		}
		out = append(out, apiThreadMessage{
			Message:         apiMessageFromRecord(view.Message, view.Snippet),
			Attachments:     atts,
			HeaderDetails:   apiHeaderDetails(view.HeaderDetails),
			OneClickUnsub:   view.OneClickUnsub,
			OneClickSentAt:  timeString(view.OneClickSentAt),
			SenderName:      view.SenderName,
			SenderEmail:     view.SenderEmail,
			SenderInitial:   view.SenderInitial,
			SenderVisual:    senderVisual,
			RecipientLine:   view.RecipientLine,
			Snippet:         view.Snippet,
			BodyDoc:         emailDocumentWithInlineAttachments(view.DisplayBodyHTML, view.DisplayBodyText, view.ImagesAllowed, view.ImageBlockRules, view.InlineAttachments),
			FullBodyDoc:     fullDoc,
			HasHiddenQuoted: view.HasHiddenQuoted,
			HasDisplayBody:  view.HasDisplayBody,
			BodyPreviewOnly: view.BodyPreviewOnly,
			HasRemoteImages: view.HasRemoteImages,
			ImagesAllowed:   view.ImagesAllowed,
			Expanded:        view.Expanded,
			ReplySubject:    replySubject(view.Message.Subject),
			CanReplyAll:     view.CanReplyAll,
		})
	}
	return out
}

func apiContactFromStore(c store.Contact) apiContact {
	out := apiContact{
		ID:             c.ID,
		NamePrefix:     c.NamePrefix,
		GivenName:      c.GivenName,
		AdditionalName: c.AdditionalName,
		FamilyName:     c.FamilyName,
		NameSuffix:     c.NameSuffix,
		DisplayName:    c.DisplayName,
		Nickname:       c.Nickname,
		Organization:   c.Organization,
		Department:     c.Department,
		JobTitle:       c.JobTitle,
		Birthday:       c.Birthday,
		Notes:          c.Notes,
		Categories:     c.Categories,
		IsMe:           c.IsMe,
		IsPrimary:      c.IsPrimary,
		Emails:         make([]apiContactEmail, 0, len(c.Emails)),
		Phones:         make([]apiContactPhone, 0, len(c.Phones)),
		Addresses:      make([]apiContactAddress, 0, len(c.Addresses)),
		URLs:           make([]apiContactURL, 0, len(c.URLs)),
	}
	if c.Icon != nil {
		out.IconURL = fmt.Sprintf("/contacts/%d/icon", c.ID)
	}
	for _, email := range c.Emails {
		out.Emails = append(out.Emails, apiContactEmail{ID: email.ID, Label: email.Label, Email: email.Email, IsPrimary: email.IsPrimary})
	}
	for _, phone := range c.Phones {
		out.Phones = append(out.Phones, apiContactPhone{ID: phone.ID, Label: phone.Label, Number: phone.Number, IsPrimary: phone.IsPrimary})
	}
	for _, addr := range c.Addresses {
		out.Addresses = append(out.Addresses, apiContactAddress{
			ID: addr.ID, Label: addr.Label, Street: addr.Street, Locality: addr.Locality, Region: addr.Region,
			PostalCode: addr.PostalCode, Country: addr.Country, IsPrimary: addr.IsPrimary,
		})
	}
	for _, url := range c.URLs {
		out.URLs = append(out.URLs, apiContactURL{ID: url.ID, Label: url.Label, URL: url.URL, IsPrimary: url.IsPrimary})
	}
	return out
}

func apiContactsFromStore(contacts []store.Contact) []apiContact {
	out := make([]apiContact, 0, len(contacts))
	for _, contact := range contacts {
		out = append(out, apiContactFromStore(contact))
	}
	return out
}

func apiHeaderDetails(details []messageHeaderDetail) []apiHeaderItem {
	out := make([]apiHeaderItem, 0, len(details))
	for _, detail := range details {
		out = append(out, apiHeaderItem{Label: detail.Label, Value: detail.Value})
	}
	return out
}

func apiSyncRunPtr(run *store.SyncRun) *apiSyncRun {
	if run == nil {
		return nil
	}
	out := apiSyncRunFrom(*run)
	return &out
}

func apiSyncRunFrom(run store.SyncRun) apiSyncRun {
	return apiSyncRun{
		ID:               run.ID,
		AccountID:        run.AccountID,
		Status:           run.Status,
		StartedAt:        timeString(run.StartedAt),
		FinishedAt:       timeString(run.FinishedAt),
		UpdatedAt:        timeString(run.UpdatedAt),
		MessagesSeen:     run.MessagesSeen,
		MessagesStored:   run.MessagesStored,
		MessagesSkipped:  run.MessagesSkipped,
		NewMessages:      run.NewMessages,
		LatestNewFrom:    run.LatestNewFrom,
		LatestNewSubject: run.LatestNewSubject,
		MessagesTotal:    run.MessagesTotal,
		MailboxesDone:    run.MailboxesDone,
		MailboxesTotal:   run.MailboxesTotal,
		CurrentMailbox:   run.CurrentMailbox,
		CurrentUID:       run.CurrentUID,
		Error:            run.Error,
	}
}

func apiSyncRuns(runs []store.SyncRun) []apiSyncRun {
	out := make([]apiSyncRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, apiSyncRunFrom(run))
	}
	return out
}

func apiSyncFolders(views []syncFolderView) []apiSyncFolder {
	out := make([]apiSyncFolder, 0, len(views))
	for _, view := range views {
		out = append(out, apiSyncFolder{
			Mailbox:    apiMailboxFromSummary(view.Mailbox),
			IsRunning:  view.IsRunning,
			LastRun:    apiSyncRunPtr(view.LastRun),
			CanSyncNow: view.CanSyncNow,
		})
	}
	return out
}

func apiPluginSettings(settings []store.PluginSetting) []apiPluginSetting {
	out := make([]apiPluginSetting, 0, len(settings))
	for _, setting := range settings {
		out = append(out, apiPluginSetting{
			ID:               setting.ID,
			Name:             setting.Name,
			Description:      setting.Description,
			Enabled:          setting.Enabled,
			EnabledByDefault: setting.EnabledByDefault,
			Heavy:            setting.Heavy,
		})
	}
	return out
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(time.RFC3339)
}

func shortDateString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	local := t.Local()
	now := time.Now().Local()
	if local.Year() == now.Year() && local.YearDay() == now.YearDay() {
		return local.Format("3:04 PM")
	}
	if local.Year() == now.Year() {
		return local.Format("Jan 2")
	}
	return local.Format("1/2/06")
}
