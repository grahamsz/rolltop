// File overview: SQLite persistence for the client-side PGP plugin's identity
// private keys and contact public keys. The tables are created by this plugin's
// user-scoped migrations; the core store only provides tenant DB routing.

package keystore

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"rolltop/backend/store"
)

// ListIdentityPrivateKeysForUser returns all passphrase-protected private keys scoped to one user.
func ListIdentityPrivateKeysForUser(ctx context.Context, db *store.Store, userID int64) ([]store.IdentityPGPPrivateKey, error) {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := userDB.QueryContext(ctx, identityPrivateKeySelectSQL()+`
		WHERE user_id = ?
		ORDER BY identity_id, is_active_signing DESC, is_active_encryption DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentityPrivateKeys(rows)
}

// ListIdentityPrivateKeysForIdentity returns all keys for one user-owned identity.
func ListIdentityPrivateKeysForIdentity(ctx context.Context, db *store.Store, userID, identityID int64) ([]store.IdentityPGPPrivateKey, error) {
	if _, err := db.GetMailIdentityForUser(ctx, userID, identityID); err != nil {
		return nil, err
	}
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := userDB.QueryContext(ctx, identityPrivateKeySelectSQL()+`
		WHERE user_id = ? AND identity_id = ?
		ORDER BY is_active_signing DESC, is_active_encryption DESC, id`, userID, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentityPrivateKeys(rows)
}

// GetIdentityPrivateKeyForUser loads one private key row scoped to a user.
func GetIdentityPrivateKeyForUser(ctx context.Context, db *store.Store, userID, id int64) (store.IdentityPGPPrivateKey, error) {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return store.IdentityPGPPrivateKey{}, err
	}
	return scanIdentityPrivateKey(userDB.QueryRowContext(ctx, identityPrivateKeySelectSQL()+` WHERE user_id = ? AND id = ?`, userID, id))
}

// UpsertIdentityPrivateKey creates or updates a passphrase-protected identity key.
func UpsertIdentityPrivateKey(ctx context.Context, db *store.Store, key store.IdentityPGPPrivateKey) (store.IdentityPGPPrivateKey, error) {
	if key.UserID == 0 || key.IdentityID == 0 {
		return store.IdentityPGPPrivateKey{}, store.ErrNotFound
	}
	if _, err := db.GetMailIdentityForUser(ctx, key.UserID, key.IdentityID); err != nil {
		return store.IdentityPGPPrivateKey{}, err
	}
	key = normalizeIdentityPrivateKey(key)
	userDB, err := db.UserDB(ctx, key.UserID)
	if err != nil {
		return store.IdentityPGPPrivateKey{}, err
	}
	ts := nowUnix()
	tx, err := userDB.BeginTx(ctx, nil)
	if err != nil {
		return store.IdentityPGPPrivateKey{}, err
	}
	rollback := func() (store.IdentityPGPPrivateKey, error) {
		_ = tx.Rollback()
		return store.IdentityPGPPrivateKey{}, err
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
			(user_id, identity_id, label, fingerprint, key_id, user_ids, public_key_armored, encrypted_private_key, private_key_storage, revocation_certificate, is_active_signing, is_active_encryption, is_decrypt_only, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key.UserID, key.IdentityID, key.Label, key.Fingerprint, key.KeyID, key.UserIDs, key.PublicKeyArmored, key.EncryptedPrivateKey, key.PrivateKeyStorage, key.RevocationCertificate, boolInt(key.IsActiveSigning), boolInt(key.IsActiveEncryption), boolInt(key.IsDecryptOnly), ts, ts)
		if err != nil {
			return rollback()
		}
		key.ID, err = res.LastInsertId()
		if err != nil {
			return rollback()
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE identity_pgp_private_keys SET label = ?, fingerprint = ?, key_id = ?, user_ids = ?, public_key_armored = ?, encrypted_private_key = ?, private_key_storage = ?, revocation_certificate = ?, is_active_signing = ?, is_active_encryption = ?, is_decrypt_only = ?, updated_at = ?
			WHERE user_id = ? AND identity_id = ? AND id = ?`,
			key.Label, key.Fingerprint, key.KeyID, key.UserIDs, key.PublicKeyArmored, key.EncryptedPrivateKey, key.PrivateKeyStorage, key.RevocationCertificate, boolInt(key.IsActiveSigning), boolInt(key.IsActiveEncryption), boolInt(key.IsDecryptOnly), ts, key.UserID, key.IdentityID, key.ID)
		if err != nil {
			return rollback()
		}
		n, err := res.RowsAffected()
		if err != nil {
			return rollback()
		}
		if n == 0 {
			err = store.ErrNotFound
			return rollback()
		}
	}
	if err = tx.Commit(); err != nil {
		return store.IdentityPGPPrivateKey{}, err
	}
	return GetIdentityPrivateKeyForUser(ctx, db, key.UserID, key.ID)
}

// DeleteIdentityPrivateKeyForUser removes one identity private key row.
func DeleteIdentityPrivateKeyForUser(ctx context.Context, db *store.Store, userID, id int64) error {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := userDB.ExecContext(ctx, `DELETE FROM identity_pgp_private_keys WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ActiveIdentityPublicKeyForUser returns the active encryption public key used by compose and Autocrypt.
func ActiveIdentityPublicKeyForUser(ctx context.Context, db *store.Store, userID, identityID int64) (store.IdentityPGPPrivateKey, error) {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return store.IdentityPGPPrivateKey{}, err
	}
	row := userDB.QueryRowContext(ctx, identityPrivateKeySelectSQL()+`
		WHERE user_id = ? AND identity_id = ?
			AND is_active_encryption = 1
		ORDER BY is_active_signing DESC, id
		LIMIT 1`, userID, identityID)
	return scanIdentityPrivateKey(row)
}

// ListPublicKeysForContact returns all public keys attached to one contact.
func ListPublicKeysForContact(ctx context.Context, db *store.Store, userID, contactID int64) ([]store.ContactPGPPublicKey, error) {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := userDB.QueryContext(ctx, contactPublicKeySelectSQL()+` WHERE user_id = ? AND contact_id = ? ORDER BY normalized_email, is_preferred DESC, id`, userID, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanContactPublicKeys(rows)
}

// ListPublicKeysForEmails returns preferred public keys for recipient email addresses.
func ListPublicKeysForEmails(ctx context.Context, db *store.Store, userID int64, emails []string) ([]store.ContactPGPPublicKey, error) {
	return listPublicKeysForEmails(ctx, db, userID, emails, true)
}

// ListAllPublicKeysForEmails returns every public key for the supplied email addresses.
func ListAllPublicKeysForEmails(ctx context.Context, db *store.Store, userID int64, emails []string) ([]store.ContactPGPPublicKey, error) {
	return listPublicKeysForEmails(ctx, db, userID, emails, false)
}

func listPublicKeysForEmails(ctx context.Context, db *store.Store, userID int64, emails []string, preferredOnly bool) ([]store.ContactPGPPublicKey, error) {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []store.ContactPGPPublicKey
	for _, email := range emails {
		normalized := store.NormalizeContactEmail(email)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		if preferredOnly {
			row := userDB.QueryRowContext(ctx, contactPublicKeySelectSQL()+`
				WHERE user_id = ? AND normalized_email = ?
				ORDER BY is_preferred DESC, id
				LIMIT 1`, userID, normalized)
			key, err := scanContactPublicKey(row)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, key)
			continue
		}
		rows, err := userDB.QueryContext(ctx, contactPublicKeySelectSQL()+`
			WHERE user_id = ? AND normalized_email = ?
			ORDER BY is_preferred DESC, id`, userID, normalized)
		if err != nil {
			return nil, err
		}
		keys, err := scanContactPublicKeys(rows)
		rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, keys...)
	}
	return out, nil
}

// UpsertContactPublicKey creates or updates a contact public key row.
func UpsertContactPublicKey(ctx context.Context, db *store.Store, key store.ContactPGPPublicKey) (store.ContactPGPPublicKey, error) {
	if key.UserID == 0 || key.ContactID == 0 {
		return store.ContactPGPPublicKey{}, store.ErrNotFound
	}
	if err := requireContactForUser(ctx, db, key.UserID, key.ContactID); err != nil {
		return store.ContactPGPPublicKey{}, err
	}
	key = normalizeContactPublicKey(key)
	if key.NormalizedEmail == "" || key.PublicKeyArmored == "" {
		return store.ContactPGPPublicKey{}, store.ErrNotFound
	}
	if err := requireContactEmailForUser(ctx, db, key.UserID, key.ContactID, key.NormalizedEmail); err != nil {
		return store.ContactPGPPublicKey{}, err
	}
	userDB, err := db.UserDB(ctx, key.UserID)
	if err != nil {
		return store.ContactPGPPublicKey{}, err
	}
	ts := nowUnix()
	tx, err := userDB.BeginTx(ctx, nil)
	if err != nil {
		return store.ContactPGPPublicKey{}, err
	}
	rollback := func() (store.ContactPGPPublicKey, error) {
		_ = tx.Rollback()
		return store.ContactPGPPublicKey{}, err
	}
	if key.IsPreferred {
		if _, err = tx.ExecContext(ctx, `UPDATE contact_pgp_public_keys SET is_preferred = 0, updated_at = ? WHERE user_id = ? AND normalized_email = ?`, ts, key.UserID, key.NormalizedEmail); err != nil {
			return rollback()
		}
	}
	if key.ID == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO contact_pgp_public_keys
			(user_id, contact_id, email, normalized_email, label, fingerprint, key_id, user_ids, public_key_armored, source_kind, source_detail, is_preferred, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key.UserID, key.ContactID, key.Email, key.NormalizedEmail, key.Label, key.Fingerprint, key.KeyID, key.UserIDs, key.PublicKeyArmored, key.SourceKind, key.SourceDetail, boolInt(key.IsPreferred), ts, ts)
		if err != nil {
			return rollback()
		}
		key.ID, err = res.LastInsertId()
		if err != nil {
			return rollback()
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE contact_pgp_public_keys SET email = ?, normalized_email = ?, label = ?, fingerprint = ?, key_id = ?, user_ids = ?, public_key_armored = ?, source_kind = ?, source_detail = ?, is_preferred = ?, updated_at = ?
			WHERE user_id = ? AND contact_id = ? AND id = ?`,
			key.Email, key.NormalizedEmail, key.Label, key.Fingerprint, key.KeyID, key.UserIDs, key.PublicKeyArmored, key.SourceKind, key.SourceDetail, boolInt(key.IsPreferred), ts, key.UserID, key.ContactID, key.ID)
		if err != nil {
			return rollback()
		}
		n, err := res.RowsAffected()
		if err != nil {
			return rollback()
		}
		if n == 0 {
			err = store.ErrNotFound
			return rollback()
		}
	}
	if err = tx.Commit(); err != nil {
		return store.ContactPGPPublicKey{}, err
	}
	return GetContactPublicKeyForUser(ctx, db, key.UserID, key.ID)
}

// GetContactPublicKeyForUser loads one public key row scoped to a user.
func GetContactPublicKeyForUser(ctx context.Context, db *store.Store, userID, id int64) (store.ContactPGPPublicKey, error) {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return store.ContactPGPPublicKey{}, err
	}
	return scanContactPublicKey(userDB.QueryRowContext(ctx, contactPublicKeySelectSQL()+` WHERE user_id = ? AND id = ?`, userID, id))
}

// DeleteContactPublicKeyForUser removes one contact public key row.
func DeleteContactPublicKeyForUser(ctx context.Context, db *store.Store, userID, id int64) error {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := userDB.ExecContext(ctx, `DELETE FROM contact_pgp_public_keys WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func identityPrivateKeySelectSQL() string {
	return `SELECT id, user_id, identity_id, label, fingerprint, key_id, user_ids, public_key_armored, encrypted_private_key, private_key_storage, revocation_certificate, is_active_signing, is_active_encryption, is_decrypt_only, created_at, updated_at FROM identity_pgp_private_keys`
}

func scanIdentityPrivateKeys(rows *sql.Rows) ([]store.IdentityPGPPrivateKey, error) {
	var out []store.IdentityPGPPrivateKey
	for rows.Next() {
		key, err := scanIdentityPrivateKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func scanIdentityPrivateKey(row rowScanner) (store.IdentityPGPPrivateKey, error) {
	var key store.IdentityPGPPrivateKey
	var activeSigning, activeEncryption, decryptOnly int
	var created, updated int64
	err := row.Scan(&key.ID, &key.UserID, &key.IdentityID, &key.Label, &key.Fingerprint, &key.KeyID, &key.UserIDs, &key.PublicKeyArmored, &key.EncryptedPrivateKey, &key.PrivateKeyStorage, &key.RevocationCertificate, &activeSigning, &activeEncryption, &decryptOnly, &created, &updated)
	key.IsActiveSigning = activeSigning != 0
	key.IsActiveEncryption = activeEncryption != 0
	key.IsDecryptOnly = decryptOnly != 0
	key.CreatedAt = unixTime(created)
	key.UpdatedAt = unixTime(updated)
	return key, err
}

func contactPublicKeySelectSQL() string {
	return `SELECT id, user_id, contact_id, email, normalized_email, label, fingerprint, key_id, user_ids, public_key_armored, source_kind, source_detail, is_preferred, created_at, updated_at FROM contact_pgp_public_keys`
}

func scanContactPublicKeys(rows *sql.Rows) ([]store.ContactPGPPublicKey, error) {
	var out []store.ContactPGPPublicKey
	for rows.Next() {
		key, err := scanContactPublicKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func scanContactPublicKey(row rowScanner) (store.ContactPGPPublicKey, error) {
	var key store.ContactPGPPublicKey
	var preferred int
	var created, updated int64
	err := row.Scan(&key.ID, &key.UserID, &key.ContactID, &key.Email, &key.NormalizedEmail, &key.Label, &key.Fingerprint, &key.KeyID, &key.UserIDs, &key.PublicKeyArmored, &key.SourceKind, &key.SourceDetail, &preferred, &created, &updated)
	key.IsPreferred = preferred != 0
	key.CreatedAt = unixTime(created)
	key.UpdatedAt = unixTime(updated)
	return key, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func normalizeIdentityPrivateKey(key store.IdentityPGPPrivateKey) store.IdentityPGPPrivateKey {
	key.Label = trimLimit(key.Label, 240)
	key.Fingerprint = normalizePGPFingerprint(key.Fingerprint)
	key.KeyID = trimLimit(key.KeyID, 80)
	key.UserIDs = trimLimit(key.UserIDs, 2000)
	key.PublicKeyArmored = strings.TrimSpace(key.PublicKeyArmored)
	key.EncryptedPrivateKey = strings.TrimSpace(key.EncryptedPrivateKey)
	switch strings.ToLower(strings.TrimSpace(key.PrivateKeyStorage)) {
	case "browser":
		key.PrivateKeyStorage = "browser"
	default:
		key.PrivateKeyStorage = "server"
	}
	key.RevocationCertificate = strings.TrimSpace(key.RevocationCertificate)
	if key.Label == "" {
		key.Label = firstNonEmpty(key.KeyID, key.Fingerprint, "PGP key")
	}
	if key.IsActiveSigning || key.IsActiveEncryption {
		key.IsDecryptOnly = false
	}
	return key
}

func normalizeContactPublicKey(key store.ContactPGPPublicKey) store.ContactPGPPublicKey {
	key.Email = strings.TrimSpace(key.Email)
	key.NormalizedEmail = store.NormalizeContactEmail(key.Email)
	key.Label = trimLimit(key.Label, 240)
	key.Fingerprint = normalizePGPFingerprint(key.Fingerprint)
	key.KeyID = trimLimit(key.KeyID, 80)
	key.UserIDs = trimLimit(key.UserIDs, 2000)
	key.PublicKeyArmored = strings.TrimSpace(key.PublicKeyArmored)
	key.SourceKind = trimLimit(strings.ToLower(strings.TrimSpace(key.SourceKind)), 80)
	key.SourceDetail = trimLimit(strings.TrimSpace(key.SourceDetail), 240)
	if key.SourceKind == "" {
		key.SourceKind = "manual"
	}
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

func requireContactForUser(ctx context.Context, db *store.Store, userID, contactID int64) error {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return err
	}
	var id int64
	err = userDB.QueryRowContext(ctx, `SELECT id FROM contacts WHERE user_id = ? AND id = ?`, userID, contactID).Scan(&id)
	if err == sql.ErrNoRows {
		return store.ErrNotFound
	}
	return err
}

func requireContactEmailForUser(ctx context.Context, db *store.Store, userID, contactID int64, normalizedEmail string) error {
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		return err
	}
	var id int64
	err = userDB.QueryRowContext(ctx, `
		SELECT id FROM contact_emails
		WHERE user_id = ? AND contact_id = ? AND normalized_email = ?`, userID, contactID, normalizedEmail).Scan(&id)
	if err == sql.ErrNoRows {
		return store.ErrNotFound
	}
	return err
}

func nowUnix() int64 {
	return time.Now().UTC().Unix()
}

func unixTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func trimLimit(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
