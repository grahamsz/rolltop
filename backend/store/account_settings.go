// File overview: CRUD and validation for IMAP accounts, SMTP servers, and outgoing identities.

package store

import (
	"context"
	"errors"
	"strings"
)

func prepareSMTPAccount(a SMTPAccount) (SMTPAccount, error) {
	if a.UserID == 0 || strings.TrimSpace(a.Host) == "" || a.Port <= 0 {
		return SMTPAccount{}, errors.New("SMTP account fields are incomplete")
	}
	a.Label = trimLimit(a.Label, 240)
	a.Host = strings.TrimSpace(a.Host)
	a.Username = strings.TrimSpace(a.Username)
	if a.Label == "" {
		a.Label = firstNonEmpty(a.Username, a.Host)
	}
	return a, nil
}

// CreateSMTPAccount inserts a new outgoing server for one user.
func (s *Store) CreateSMTPAccount(ctx context.Context, a SMTPAccount) (SMTPAccount, error) {
	a, err := prepareSMTPAccount(a)
	if err != nil {
		return SMTPAccount{}, err
	}
	ts := nowUnix()
	res, err := s.mustDataDB(ctx, a.UserID).ExecContext(ctx, `INSERT INTO smtp_accounts (user_id, label, host, port, username, encrypted_password, use_tls, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.UserID, a.Label, a.Host, a.Port, a.Username, a.EncryptedPassword, boolInt(a.UseTLS), ts, ts)
	if err != nil {
		return SMTPAccount{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return SMTPAccount{}, err
	}
	return s.GetSMTPAccountForUser(ctx, a.UserID, id)
}

// UpsertSMTPAccount creates or updates an outgoing server while preserving user ownership.
func (s *Store) UpsertSMTPAccount(ctx context.Context, a SMTPAccount) (SMTPAccount, error) {
	if a.ID == 0 {
		return s.CreateSMTPAccount(ctx, a)
	}
	a, err := prepareSMTPAccount(a)
	if err != nil {
		return SMTPAccount{}, err
	}
	res, err := s.mustDataDB(ctx, a.UserID).ExecContext(ctx, `UPDATE smtp_accounts SET label = ?, host = ?, port = ?, username = ?, encrypted_password = ?, use_tls = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, a.Label, a.Host, a.Port, a.Username, a.EncryptedPassword, boolInt(a.UseTLS), nowUnix(), a.UserID, a.ID)
	if err != nil {
		return SMTPAccount{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return SMTPAccount{}, err
	}
	if n == 0 {
		return SMTPAccount{}, ErrNotFound
	}
	return s.GetSMTPAccountForUser(ctx, a.UserID, a.ID)
}

// GetSMTPAccountForUser loads one outgoing server scoped to the signed-in user.
func (s *Store) GetSMTPAccountForUser(ctx context.Context, userID, id int64) (SMTPAccount, error) {
	if id <= 0 {
		return SMTPAccount{}, ErrNotFound
	}
	return scanSMTPAccount(s.mustDataDB(ctx, userID).QueryRowContext(ctx, smtpAccountSelectSQL()+` WHERE user_id = ? AND id = ?`, userID, id))
}

// ListSMTPAccountsForUser returns all outgoing servers available to one user.
func (s *Store) ListSMTPAccountsForUser(ctx context.Context, userID int64) ([]SMTPAccount, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, smtpAccountSelectSQL()+` WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SMTPAccount
	for rows.Next() {
		item, err := scanSMTPAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// DeleteSMTPAccountForUser removes one outgoing server and clears identity assignments that pointed at it.
func (s *Store) DeleteSMTPAccountForUser(ctx context.Context, userID, id int64) error {
	if id <= 0 {
		return ErrNotFound
	}
	tx, err := s.mustDataDB(ctx, userID).BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func() error {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mail_identities SET smtp_account_id = 0, updated_at = ? WHERE user_id = ? AND smtp_account_id = ?`, nowUnix(), userID, id); err != nil {
		return rollback()
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM smtp_accounts WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return rollback()
	}
	n, err := res.RowsAffected()
	if err != nil {
		return rollback()
	}
	if n == 0 {
		err = ErrNotFound
		return rollback()
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) firstSMTPAccountID(ctx context.Context, userID int64) int64 {
	var id int64
	_ = s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id FROM smtp_accounts WHERE user_id = ? ORDER BY id LIMIT 1`, userID).Scan(&id)
	return id
}

func smtpAccountSelectSQL() string {
	return `SELECT id, user_id, label, host, port, username, encrypted_password, use_tls, created_at, updated_at FROM smtp_accounts`
}

func scanSMTPAccount(row rowScanner) (SMTPAccount, error) {
	var a SMTPAccount
	var useTLS int
	var created, updated int64
	err := row.Scan(&a.ID, &a.UserID, &a.Label, &a.Host, &a.Port, &a.Username, &a.EncryptedPassword, &useTLS, &created, &updated)
	a.UseTLS = useTLS != 0
	a.CreatedAt = unixTime(created)
	a.UpdatedAt = unixTime(updated)
	return a, err
}

// SyncMailIdentitiesForMeContacts keeps outgoing identity rows aligned with the user's Me contact emails.
func (s *Store) SyncMailIdentitiesForMeContacts(ctx context.Context, userID int64) error {
	contacts, err := s.ListMeContactsForUser(ctx, userID)
	if err != nil {
		return err
	}
	defaultSMTPID := s.firstSMTPAccountID(ctx, userID)
	ts := nowUnix()
	for _, contact := range contacts {
		display := contactIdentityName(contact)
		for _, email := range contact.Emails {
			address := strings.TrimSpace(email.Email)
			if address == "" || email.ID == 0 {
				continue
			}
			primary := contact.IsPrimary && email.IsPrimary
			defaults := s.identityMailboxDefaults(ctx, userID, address, defaultSMTPID, 0)
			if _, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `INSERT INTO mail_identities
					(user_id, contact_id, contact_email_id, smtp_account_id, imap_account_id, sent_mailbox_id, drafts_mailbox_id, email, display_name, signature, is_primary, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)
				ON CONFLICT(user_id, contact_email_id) DO UPDATE SET
					contact_id = excluded.contact_id,
					smtp_account_id = CASE WHEN mail_identities.smtp_account_id = 0 THEN excluded.smtp_account_id ELSE mail_identities.smtp_account_id END,
					imap_account_id = CASE WHEN mail_identities.imap_account_id = 0 THEN excluded.imap_account_id ELSE mail_identities.imap_account_id END,
					sent_mailbox_id = CASE WHEN mail_identities.sent_mailbox_id = 0 AND mail_identities.imap_account_id = 0 THEN excluded.sent_mailbox_id ELSE mail_identities.sent_mailbox_id END,
					drafts_mailbox_id = CASE WHEN mail_identities.drafts_mailbox_id = 0 AND mail_identities.imap_account_id = 0 THEN excluded.drafts_mailbox_id ELSE mail_identities.drafts_mailbox_id END,
					email = excluded.email,
					display_name = excluded.display_name,
					is_primary = excluded.is_primary,
					updated_at = excluded.updated_at`, userID, contact.ID, email.ID, defaultSMTPID, defaults.IMAPAccountID, defaults.SentMailboxID, defaults.DraftsMailboxID, address, display, boolInt(primary), ts, ts); err != nil {
				return err
			}
		}
	}
	if _, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `DELETE FROM mail_identities
		WHERE user_id = ? AND NOT EXISTS (
			SELECT 1 FROM contacts c
			JOIN contact_emails e ON e.user_id = c.user_id AND e.contact_id = c.id
			WHERE c.user_id = mail_identities.user_id AND c.is_me = 1 AND e.id = mail_identities.contact_email_id
		)`, userID); err != nil {
		return err
	}
	if err := s.ensurePrimaryMailIdentity(ctx, userID); err != nil {
		return err
	}
	return s.EnsureMailIdentityMailboxDefaults(ctx, userID)
}

// ListMailIdentitiesForUser returns identity rows joined with Me contact emails for settings and compose.
func (s *Store) ListMailIdentitiesForUser(ctx context.Context, userID int64) ([]MailIdentity, error) {
	if err := s.SyncMailIdentitiesForMeContacts(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, mailIdentitySelectSQL()+` WHERE user_id = ? ORDER BY is_primary DESC, lower(display_name), lower(email), id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailIdentity
	for rows.Next() {
		item, err := scanMailIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CreateMailIdentityForUser creates or promotes a Me-contact email, then applies
// identity-level server, folder, primary, and signature settings to the synced row.
func (s *Store) CreateMailIdentityForUser(ctx context.Context, userID int64, in MailIdentity) (MailIdentity, error) {
	email := strings.TrimSpace(in.Email)
	key := NormalizeContactEmail(email)
	if key == "" {
		return MailIdentity{}, errors.New("identity email is required")
	}
	display := trimLimit(in.DisplayName, 240)
	if display == "" {
		display = email
	}
	if _, err := s.EnsureMeContactForEmail(ctx, userID, email, display); err != nil {
		return MailIdentity{}, err
	}
	identities, err := s.ListMailIdentitiesForUser(ctx, userID)
	if err != nil {
		return MailIdentity{}, err
	}
	for _, identity := range identities {
		if NormalizeContactEmail(identity.Email) != key {
			continue
		}
		next := identity
		next.DisplayName = display
		next.Signature = in.Signature
		next.IsPrimary = in.IsPrimary || identity.IsPrimary
		if in.SMTPAccountID > 0 {
			next.SMTPAccountID = in.SMTPAccountID
		}
		if in.IMAPAccountID > 0 {
			next.IMAPAccountID = in.IMAPAccountID
		}
		if in.SentMailboxID > 0 {
			next.SentMailboxID = in.SentMailboxID
		}
		if in.DraftsMailboxID > 0 {
			next.DraftsMailboxID = in.DraftsMailboxID
		}
		return s.UpdateMailIdentityForUser(ctx, userID, next)
	}
	return MailIdentity{}, ErrNotFound
}

// UpdateMailIdentityForUser updates server assignments, display name, signature, and primary state for one identity.
func (s *Store) UpdateMailIdentityForUser(ctx context.Context, userID int64, in MailIdentity) (MailIdentity, error) {
	if in.ID <= 0 {
		return MailIdentity{}, ErrNotFound
	}
	if in.SMTPAccountID > 0 {
		if _, err := s.GetSMTPAccountForUser(ctx, userID, in.SMTPAccountID); err != nil {
			return MailIdentity{}, err
		}
	}
	if in.IMAPAccountID > 0 {
		if _, err := s.GetMailAccountForUser(ctx, userID, in.IMAPAccountID); err != nil {
			return MailIdentity{}, err
		}
	}
	if err := s.validateIdentityMailboxRole(ctx, userID, in.SentMailboxID, "sent", in.IMAPAccountID); err != nil {
		return MailIdentity{}, err
	}
	if err := s.validateIdentityMailboxRole(ctx, userID, in.DraftsMailboxID, "drafts", in.IMAPAccountID); err != nil {
		return MailIdentity{}, err
	}
	current, err := s.GetMailIdentityForUser(ctx, userID, in.ID)
	if err != nil {
		return MailIdentity{}, err
	}
	display := trimLimit(in.DisplayName, 240)
	if display == "" {
		display = current.DisplayName
	}
	signature := trimLimit(in.Signature, 4000)
	tx, err := s.mustDataDB(ctx, userID).BeginTx(ctx, nil)
	if err != nil {
		return MailIdentity{}, err
	}
	rollback := func() (MailIdentity, error) {
		_ = tx.Rollback()
		return MailIdentity{}, err
	}
	if in.IsPrimary {
		if _, err = tx.ExecContext(ctx, `UPDATE mail_identities SET is_primary = 0, updated_at = ? WHERE user_id = ? AND id <> ?`, nowUnix(), userID, current.ID); err != nil {
			return rollback()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE contacts SET is_primary = 0, updated_at = ? WHERE user_id = ? AND id <> ? AND is_me = 1`, nowUnix(), userID, current.ContactID); err != nil {
			return rollback()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE contact_emails SET is_primary = 0, updated_at = ? WHERE user_id = ? AND contact_id = ? AND id <> ?`, nowUnix(), userID, current.ContactID, current.ContactEmailID); err != nil {
			return rollback()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE contact_emails SET is_primary = 1, updated_at = ? WHERE user_id = ? AND id = ?`, nowUnix(), userID, current.ContactEmailID); err != nil {
			return rollback()
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE contacts SET display_name = ?, is_me = 1, is_primary = CASE WHEN ? THEN 1 ELSE is_primary END, updated_at = ? WHERE user_id = ? AND id = ?`, display, boolInt(in.IsPrimary), nowUnix(), userID, current.ContactID); err != nil {
		return rollback()
	}
	res, err := tx.ExecContext(ctx, `UPDATE mail_identities SET smtp_account_id = ?, imap_account_id = ?, sent_mailbox_id = ?, drafts_mailbox_id = ?, display_name = ?, signature = ?, is_primary = ?, updated_at = ? WHERE user_id = ? AND id = ?`,
		in.SMTPAccountID, in.IMAPAccountID, in.SentMailboxID, in.DraftsMailboxID, display, signature, boolInt(in.IsPrimary), nowUnix(), userID, current.ID)
	if err != nil {
		return rollback()
	}
	n, err := res.RowsAffected()
	if err != nil {
		return rollback()
	}
	if n == 0 {
		err = ErrNotFound
		return rollback()
	}
	if err = tx.Commit(); err != nil {
		return MailIdentity{}, err
	}
	if err := s.ensurePrimaryMeContact(ctx, userID); err != nil {
		return MailIdentity{}, err
	}
	if err := s.ensurePrimaryMailIdentity(ctx, userID); err != nil {
		return MailIdentity{}, err
	}
	return s.GetMailIdentityForUser(ctx, userID, current.ID)
}

// GetMailIdentityForUser loads one outgoing identity scoped to the signed-in user.
func (s *Store) GetMailIdentityForUser(ctx context.Context, userID, id int64) (MailIdentity, error) {
	return scanMailIdentity(s.mustDataDB(ctx, userID).QueryRowContext(ctx, mailIdentitySelectSQL()+` WHERE user_id = ? AND id = ?`, userID, id))
}

func (s *Store) ensurePrimaryMailIdentity(ctx context.Context, userID int64) error {
	var n int
	if err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT count(*) FROM mail_identities WHERE user_id = ? AND is_primary = 1`, userID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE mail_identities SET is_primary = 1, updated_at = ? WHERE id = (
		SELECT id FROM mail_identities WHERE user_id = ? ORDER BY id LIMIT 1
	)`, nowUnix(), userID)
	return err
}

// EnsureMailIdentityMailboxDefaults backfills identity-level IMAP and folder choices from the
// user's current account roles. It is safe to run during startup migrations and after
// onboarding because it only fills empty IMAP/Sent/Drafts choices.
func (s *Store) EnsureMailIdentityMailboxDefaults(ctx context.Context, userID int64) error {
	identities, err := s.listMailIdentitiesForUserNoSync(ctx, userID)
	if err != nil {
		return err
	}
	for _, identity := range identities {
		defaults := s.identityMailboxDefaults(ctx, userID, identity.Email, identity.SMTPAccountID, identity.IMAPAccountID)
		imapID := defaults.IMAPAccountID
		sentID := defaults.SentMailboxID
		draftsID := defaults.DraftsMailboxID
		if identity.IMAPAccountID != 0 {
			imapID = identity.IMAPAccountID
		}
		if identity.SentMailboxID != 0 {
			sentID = identity.SentMailboxID
		}
		if identity.DraftsMailboxID != 0 {
			draftsID = identity.DraftsMailboxID
		}
		if imapID == identity.IMAPAccountID && sentID == identity.SentMailboxID && draftsID == identity.DraftsMailboxID {
			continue
		}
		if _, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE mail_identities SET imap_account_id = ?, sent_mailbox_id = ?, drafts_mailbox_id = ?, updated_at = ? WHERE user_id = ? AND id = ?`, imapID, sentID, draftsID, nowUnix(), userID, identity.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) listMailIdentitiesForUserNoSync(ctx context.Context, userID int64) ([]MailIdentity, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, mailIdentitySelectSQL()+` WHERE user_id = ? ORDER BY is_primary DESC, lower(display_name), lower(email), id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailIdentity
	for rows.Next() {
		item, err := scanMailIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type identityMailboxDefaultSet struct {
	IMAPAccountID   int64
	SentMailboxID   int64
	DraftsMailboxID int64
}

func (s *Store) identityMailboxDefaults(ctx context.Context, userID int64, email string, smtpAccountID, preferredIMAPAccountID int64) identityMailboxDefaultSet {
	accounts, err := s.ListMailAccountsForUser(ctx, userID)
	if err != nil || len(accounts) == 0 {
		return identityMailboxDefaultSet{}
	}
	var smtp SMTPAccount
	if smtpAccountID > 0 {
		smtp, _ = s.GetSMTPAccountForUser(ctx, userID, smtpAccountID)
	}
	selected := firstMailAccountByID(accounts, preferredIMAPAccountID)
	if selected.ID == 0 {
		candidates := identityMailAccountCandidates(accounts, email, smtp.Username)
		if len(candidates) == 0 {
			candidates = accounts
		}
		selected = candidates[0]
	}
	selectedList := []MailAccount{selected}
	return identityMailboxDefaultSet{
		IMAPAccountID:   selected.ID,
		SentMailboxID:   s.firstMailboxRoleID(ctx, userID, selectedList, "sent"),
		DraftsMailboxID: s.firstMailboxRoleID(ctx, userID, selectedList, "drafts"),
	}
}

func firstMailAccountByID(accounts []MailAccount, id int64) MailAccount {
	if id <= 0 {
		return MailAccount{}
	}
	for _, account := range accounts {
		if account.ID == id {
			return account
		}
	}
	return MailAccount{}
}

func identityMailAccountCandidates(accounts []MailAccount, values ...string) []MailAccount {
	keys := map[string]bool{}
	for _, value := range values {
		if key := NormalizeContactEmail(value); key != "" {
			keys[key] = true
		}
	}
	if len(keys) == 0 {
		return nil
	}
	var out []MailAccount
	for _, account := range accounts {
		for _, value := range []string{account.Email, account.Username, account.SMTPUsername} {
			if keys[NormalizeContactEmail(value)] {
				out = append(out, account)
				break
			}
		}
	}
	return out
}

func (s *Store) firstMailboxRoleID(ctx context.Context, userID int64, accounts []MailAccount, role string) int64 {
	for _, account := range accounts {
		mailbox, err := s.GetMailboxByRoleForAccount(ctx, userID, account.ID, role)
		if err == nil {
			return mailbox.ID
		}
	}
	return 0
}

func (s *Store) validateIdentityMailboxRole(ctx context.Context, userID, mailboxID int64, role string, imapAccountID int64) error {
	if mailboxID == 0 {
		return nil
	}
	mailbox, err := s.GetMailboxForUser(ctx, userID, mailboxID)
	if err != nil {
		return err
	}
	if normalizeMailboxRole(mailbox.Role) != normalizeMailboxRole(role) {
		return ErrNotFound
	}
	if imapAccountID > 0 && mailbox.AccountID != imapAccountID {
		return ErrNotFound
	}
	return nil
}

func mailIdentitySelectSQL() string {
	return `SELECT id, user_id, contact_id, contact_email_id, smtp_account_id, imap_account_id, sent_mailbox_id, drafts_mailbox_id, email, display_name, signature, is_primary, created_at, updated_at FROM mail_identities`
}

func scanMailIdentity(row rowScanner) (MailIdentity, error) {
	var ident MailIdentity
	var primary int
	var created, updated int64
	err := row.Scan(&ident.ID, &ident.UserID, &ident.ContactID, &ident.ContactEmailID, &ident.SMTPAccountID, &ident.IMAPAccountID, &ident.SentMailboxID, &ident.DraftsMailboxID, &ident.Email, &ident.DisplayName, &ident.Signature, &primary, &created, &updated)
	ident.IsPrimary = primary != 0
	ident.CreatedAt = unixTime(created)
	ident.UpdatedAt = unixTime(updated)
	return ident, err
}

func contactIdentityName(contact Contact) string {
	name := strings.TrimSpace(contact.DisplayName)
	if name == "" {
		name = strings.TrimSpace(contact.GivenName + " " + contact.FamilyName)
	}
	if name == "" {
		name = strings.TrimSpace(contact.Organization)
	}
	return name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
