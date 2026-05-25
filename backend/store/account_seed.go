// File overview: Post-schema seed logic for deriving SMTP accounts from IMAP account settings.

package store

import "context"

func (s *Store) seedSMTPAccountsFromMailAccounts(ctx context.Context) error {
	now := nowUnix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO smtp_accounts
		(user_id, label, host, port, username, encrypted_password, use_tls, created_at, updated_at)
		SELECT ma.user_id,
			CASE WHEN trim(ma.label) <> '' THEN trim(ma.label) ELSE CASE WHEN trim(ma.email) <> '' THEN trim(ma.email) ELSE CASE WHEN trim(ma.smtp_host) <> '' THEN trim(ma.smtp_host) ELSE trim(ma.host) END END END,
			CASE WHEN trim(ma.smtp_host) <> '' THEN trim(ma.smtp_host) ELSE trim(ma.host) END,
			CASE WHEN ma.smtp_port > 0 THEN ma.smtp_port ELSE 587 END,
			CASE WHEN trim(ma.smtp_username) <> '' THEN trim(ma.smtp_username) ELSE trim(ma.username) END,
			CASE WHEN trim(ma.encrypted_smtp_password) <> '' THEN ma.encrypted_smtp_password ELSE ma.encrypted_password END,
			ma.smtp_use_tls,
			?, ?
		FROM mail_accounts ma
		WHERE NOT EXISTS (
			SELECT 1 FROM smtp_accounts sa
			WHERE sa.user_id = ma.user_id
				AND lower(sa.host) = lower(CASE WHEN trim(ma.smtp_host) <> '' THEN trim(ma.smtp_host) ELSE trim(ma.host) END)
				AND sa.username = CASE WHEN trim(ma.smtp_username) <> '' THEN trim(ma.smtp_username) ELSE trim(ma.username) END
		)`, now, now)
	return err
}
