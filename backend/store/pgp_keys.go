// File overview: PGP key persistence for identity private keys and contact public keys.

package store

import (
	"context"
	"database/sql"
	"strings"
)

// ListIdentityPGPPrivateKeysForUser returns all passphrase-protected private keys scoped to one user.
func (s *Store) ListIdentityPGPPrivateKeysForUser(ctx context.Context, userID int64) ([]IdentityPGPPrivateKey, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, identityPGPPrivateKeySelectSQL()+`
		WHERE user_id = ?
		ORDER BY identity_id, is_active_signing DESC, is_active_encryption DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentityPGPPrivateKeys(rows)
}

// ListIdentityPGPPrivateKeysForIdentity returns all keys for one user-owned identity.
func (s *Store) ListIdentityPGPPrivateKeysForIdentity(ctx context.Context, userID, identityID int64) ([]IdentityPGPPrivateKey, error) {
	if _, err := s.GetMailIdentityForUser(ctx, userID, identityID); err != nil {
		return nil, err
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, identityPGPPrivateKeySelectSQL()+`
		WHERE user_id = ? AND identity_id = ?
		ORDER BY is_active_signing DESC, is_active_encryption DESC, id`, userID, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentityPGPPrivateKeys(rows)
}

// GetIdentityPGPPrivateKeyForUser loads one private key row scoped to a user.
func (s *Store) GetIdentityPGPPrivateKeyForUser(ctx context.Context, userID, id int64) (IdentityPGPPrivateKey, error) {
	return scanIdentityPGPPrivateKey(s.mustDataDB(ctx, userID).QueryRowContext(ctx, identityPGPPrivateKeySelectSQL()+` WHERE user_id = ? AND id = ?`, userID, id))
}

// UpsertIdentityPGPPrivateKey creates or updates a passphrase-protected identity key.
func (s *Store) UpsertIdentityPGPPrivateKey(ctx context.Context, key IdentityPGPPrivateKey) (IdentityPGPPrivateKey, error) {
	if key.UserID == 0 || key.IdentityID == 0 {
		return IdentityPGPPrivateKey{}, ErrNotFound
	}
	if _, err := s.GetMailIdentityForUser(ctx, key.UserID, key.IdentityID); err != nil {
		return IdentityPGPPrivateKey{}, err
	}
	key = normalizeIdentityPGPPrivateKey(key)
	ts := nowUnix()
	db := s.mustDataDB(ctx, key.UserID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return IdentityPGPPrivateKey{}, err
	}
	rollback := func() (IdentityPGPPrivateKey, error) {
		_ = tx.Rollback()
		return IdentityPGPPrivateKey{}, err
	}
	if key.IsActiveSigning {
		if _, err = tx.ExecContext(ctx, `UPDATE identity_pgp_private_keys SET is_active_signing = 0, updated_at = ? WHERE user_id = ? AND identity_id = ?`, ts, key.UserID, key.IdentityID); err != nil {
			return rollback()
		}
	}
	if key.IsActiveEncryption {
		if _, err = tx.ExecContext(ctx, `UPDATE identity_pgp_private_keys SET is_active_encryption = 0, updated_at = ? WHERE user_id = ? AND identity_id = ?`, ts, key.UserID, key.IdentityID); err != nil {
			return rollback()
		}
	}
	if key.ID == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO identity_pgp_private_keys
			(user_id, identity_id, label, fingerprint, key_id, user_ids, public_key_armored, encrypted_private_key, revocation_certificate, is_active_signing, is_active_encryption, is_decrypt_only, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key.UserID, key.IdentityID, key.Label, key.Fingerprint, key.KeyID, key.UserIDs, key.PublicKeyArmored, key.EncryptedPrivateKey, key.RevocationCertificate, boolInt(key.IsActiveSigning), boolInt(key.IsActiveEncryption), boolInt(key.IsDecryptOnly), ts, ts)
		if err != nil {
			return rollback()
		}
		key.ID, err = res.LastInsertId()
		if err != nil {
			return rollback()
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE identity_pgp_private_keys SET label = ?, fingerprint = ?, key_id = ?, user_ids = ?, public_key_armored = ?, encrypted_private_key = ?, revocation_certificate = ?, is_active_signing = ?, is_active_encryption = ?, is_decrypt_only = ?, updated_at = ?
			WHERE user_id = ? AND identity_id = ? AND id = ?`,
			key.Label, key.Fingerprint, key.KeyID, key.UserIDs, key.PublicKeyArmored, key.EncryptedPrivateKey, key.RevocationCertificate, boolInt(key.IsActiveSigning), boolInt(key.IsActiveEncryption), boolInt(key.IsDecryptOnly), ts, key.UserID, key.IdentityID, key.ID)
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
	}
	if err = tx.Commit(); err != nil {
		return IdentityPGPPrivateKey{}, err
	}
	return s.GetIdentityPGPPrivateKeyForUser(ctx, key.UserID, key.ID)
}

// DeleteIdentityPGPPrivateKeyForUser removes one identity private key row.
func (s *Store) DeleteIdentityPGPPrivateKeyForUser(ctx context.Context, userID, id int64) error {
	res, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `DELETE FROM identity_pgp_private_keys WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ActiveIdentityPGPPublicKeyForUser returns the public key used by compose public-key attachment.
func (s *Store) ActiveIdentityPGPPublicKeyForUser(ctx context.Context, userID, identityID int64) (IdentityPGPPrivateKey, error) {
	row := s.mustDataDB(ctx, userID).QueryRowContext(ctx, identityPGPPrivateKeySelectSQL()+`
		WHERE user_id = ? AND identity_id = ?
		ORDER BY is_active_signing DESC, is_active_encryption DESC, id
		LIMIT 1`, userID, identityID)
	return scanIdentityPGPPrivateKey(row)
}

// ListContactPGPPublicKeysForUser returns all contact public keys scoped to one user.
func (s *Store) ListContactPGPPublicKeysForUser(ctx context.Context, userID int64) ([]ContactPGPPublicKey, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, contactPGPPublicKeySelectSQL()+` WHERE user_id = ? ORDER BY contact_id, normalized_email, is_preferred DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanContactPGPPublicKeys(rows)
}

// ListContactPGPPublicKeysForContact returns all public keys attached to one contact.
func (s *Store) ListContactPGPPublicKeysForContact(ctx context.Context, userID, contactID int64) ([]ContactPGPPublicKey, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, contactPGPPublicKeySelectSQL()+` WHERE user_id = ? AND contact_id = ? ORDER BY normalized_email, is_preferred DESC, id`, userID, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanContactPGPPublicKeys(rows)
}

// ListContactPGPPublicKeysForEmails returns preferred public keys for recipient email addresses.
func (s *Store) ListContactPGPPublicKeysForEmails(ctx context.Context, userID int64, emails []string) ([]ContactPGPPublicKey, error) {
	seen := map[string]bool{}
	var out []ContactPGPPublicKey
	for _, email := range emails {
		normalized := NormalizeContactEmail(email)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		row := s.mustDataDB(ctx, userID).QueryRowContext(ctx, contactPGPPublicKeySelectSQL()+`
			WHERE user_id = ? AND normalized_email = ?
			ORDER BY is_preferred DESC, id
			LIMIT 1`, userID, normalized)
		key, err := scanContactPGPPublicKey(row)
		if errorsIsNoRows(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, nil
}

// UpsertContactPGPPublicKey creates or updates a contact public key row.
func (s *Store) UpsertContactPGPPublicKey(ctx context.Context, key ContactPGPPublicKey) (ContactPGPPublicKey, error) {
	if key.UserID == 0 || key.ContactID == 0 {
		return ContactPGPPublicKey{}, ErrNotFound
	}
	if err := s.requireContactForUser(ctx, key.UserID, key.ContactID); err != nil {
		return ContactPGPPublicKey{}, err
	}
	key = normalizeContactPGPPublicKey(key)
	if key.NormalizedEmail == "" || key.PublicKeyArmored == "" {
		return ContactPGPPublicKey{}, ErrNotFound
	}
	ts := nowUnix()
	db := s.mustDataDB(ctx, key.UserID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ContactPGPPublicKey{}, err
	}
	rollback := func() (ContactPGPPublicKey, error) {
		_ = tx.Rollback()
		return ContactPGPPublicKey{}, err
	}
	if key.IsPreferred {
		if _, err = tx.ExecContext(ctx, `UPDATE contact_pgp_public_keys SET is_preferred = 0, updated_at = ? WHERE user_id = ? AND normalized_email = ?`, ts, key.UserID, key.NormalizedEmail); err != nil {
			return rollback()
		}
	}
	if key.ID == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO contact_pgp_public_keys
			(user_id, contact_id, email, normalized_email, label, fingerprint, key_id, user_ids, public_key_armored, is_preferred, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key.UserID, key.ContactID, key.Email, key.NormalizedEmail, key.Label, key.Fingerprint, key.KeyID, key.UserIDs, key.PublicKeyArmored, boolInt(key.IsPreferred), ts, ts)
		if err != nil {
			return rollback()
		}
		key.ID, err = res.LastInsertId()
		if err != nil {
			return rollback()
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE contact_pgp_public_keys SET email = ?, normalized_email = ?, label = ?, fingerprint = ?, key_id = ?, user_ids = ?, public_key_armored = ?, is_preferred = ?, updated_at = ?
			WHERE user_id = ? AND contact_id = ? AND id = ?`,
			key.Email, key.NormalizedEmail, key.Label, key.Fingerprint, key.KeyID, key.UserIDs, key.PublicKeyArmored, boolInt(key.IsPreferred), ts, key.UserID, key.ContactID, key.ID)
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
	}
	if err = tx.Commit(); err != nil {
		return ContactPGPPublicKey{}, err
	}
	return s.GetContactPGPPublicKeyForUser(ctx, key.UserID, key.ID)
}

// GetContactPGPPublicKeyForUser loads one public key row scoped to a user.
func (s *Store) GetContactPGPPublicKeyForUser(ctx context.Context, userID, id int64) (ContactPGPPublicKey, error) {
	return scanContactPGPPublicKey(s.mustDataDB(ctx, userID).QueryRowContext(ctx, contactPGPPublicKeySelectSQL()+` WHERE user_id = ? AND id = ?`, userID, id))
}

// DeleteContactPGPPublicKeyForUser removes one contact public key row.
func (s *Store) DeleteContactPGPPublicKeyForUser(ctx context.Context, userID, id int64) error {
	res, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `DELETE FROM contact_pgp_public_keys WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func identityPGPPrivateKeySelectSQL() string {
	return `SELECT id, user_id, identity_id, label, fingerprint, key_id, user_ids, public_key_armored, encrypted_private_key, revocation_certificate, is_active_signing, is_active_encryption, is_decrypt_only, created_at, updated_at FROM identity_pgp_private_keys`
}

func scanIdentityPGPPrivateKeys(rows *sql.Rows) ([]IdentityPGPPrivateKey, error) {
	var out []IdentityPGPPrivateKey
	for rows.Next() {
		key, err := scanIdentityPGPPrivateKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func scanIdentityPGPPrivateKey(row rowScanner) (IdentityPGPPrivateKey, error) {
	var key IdentityPGPPrivateKey
	var activeSigning, activeEncryption, decryptOnly int
	var created, updated int64
	err := row.Scan(&key.ID, &key.UserID, &key.IdentityID, &key.Label, &key.Fingerprint, &key.KeyID, &key.UserIDs, &key.PublicKeyArmored, &key.EncryptedPrivateKey, &key.RevocationCertificate, &activeSigning, &activeEncryption, &decryptOnly, &created, &updated)
	key.IsActiveSigning = activeSigning != 0
	key.IsActiveEncryption = activeEncryption != 0
	key.IsDecryptOnly = decryptOnly != 0
	key.CreatedAt = unixTime(created)
	key.UpdatedAt = unixTime(updated)
	return key, err
}

func contactPGPPublicKeySelectSQL() string {
	return `SELECT id, user_id, contact_id, email, normalized_email, label, fingerprint, key_id, user_ids, public_key_armored, is_preferred, created_at, updated_at FROM contact_pgp_public_keys`
}

func scanContactPGPPublicKeys(rows *sql.Rows) ([]ContactPGPPublicKey, error) {
	var out []ContactPGPPublicKey
	for rows.Next() {
		key, err := scanContactPGPPublicKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func scanContactPGPPublicKey(row rowScanner) (ContactPGPPublicKey, error) {
	var key ContactPGPPublicKey
	var preferred int
	var created, updated int64
	err := row.Scan(&key.ID, &key.UserID, &key.ContactID, &key.Email, &key.NormalizedEmail, &key.Label, &key.Fingerprint, &key.KeyID, &key.UserIDs, &key.PublicKeyArmored, &preferred, &created, &updated)
	key.IsPreferred = preferred != 0
	key.CreatedAt = unixTime(created)
	key.UpdatedAt = unixTime(updated)
	return key, err
}

func normalizeIdentityPGPPrivateKey(key IdentityPGPPrivateKey) IdentityPGPPrivateKey {
	key.Label = trimLimit(key.Label, 240)
	key.Fingerprint = normalizePGPFingerprint(key.Fingerprint)
	key.KeyID = trimLimit(key.KeyID, 80)
	key.UserIDs = trimLimit(key.UserIDs, 2000)
	key.PublicKeyArmored = strings.TrimSpace(key.PublicKeyArmored)
	key.EncryptedPrivateKey = strings.TrimSpace(key.EncryptedPrivateKey)
	key.RevocationCertificate = strings.TrimSpace(key.RevocationCertificate)
	if key.Label == "" {
		key.Label = firstNonEmpty(key.KeyID, key.Fingerprint, "PGP key")
	}
	if key.IsActiveSigning || key.IsActiveEncryption {
		key.IsDecryptOnly = false
	}
	return key
}

func normalizeContactPGPPublicKey(key ContactPGPPublicKey) ContactPGPPublicKey {
	key.Email = strings.TrimSpace(key.Email)
	key.NormalizedEmail = NormalizeContactEmail(key.Email)
	key.Label = trimLimit(key.Label, 240)
	key.Fingerprint = normalizePGPFingerprint(key.Fingerprint)
	key.KeyID = trimLimit(key.KeyID, 80)
	key.UserIDs = trimLimit(key.UserIDs, 2000)
	key.PublicKeyArmored = strings.TrimSpace(key.PublicKeyArmored)
	if key.Label == "" {
		key.Label = firstNonEmpty(key.Email, key.KeyID, "PGP key")
	}
	return key
}

func normalizePGPFingerprint(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	value = strings.ReplaceAll(value, ":", "")
	return trimLimit(value, 120)
}

func errorsIsNoRows(err error) bool {
	return err == sql.ErrNoRows
}

func (s *Store) requireContactForUser(ctx context.Context, userID, contactID int64) error {
	var id int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id FROM contacts WHERE user_id = ? AND id = ?`, userID, contactID).Scan(&id)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	return err
}
