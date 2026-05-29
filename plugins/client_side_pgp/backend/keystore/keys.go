// Package keystore contains OpenPGP key persistence helpers shared by the
// plugin API routes and Autocrypt discovery hooks.
package keystore

import (
	"context"
	"errors"
	"strings"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/store"
)

var ErrDuplicatePrivateKey = errors.New("duplicate PGP private key")

type ContactPublicKeyInput struct {
	ID               int64
	ContactID        int64
	Email            string
	Label            string
	Fingerprint      string
	KeyID            string
	UserIDs          string
	PublicKeyArmored string
	SourceKind       string
	SourceDetail     string
	IsPreferred      bool
}

func FillEncryptedPrivateKey(ctx context.Context, db *store.Store, masterKey []byte, userID int64, key *store.IdentityPGPPrivateKey, privateKeyArmored string, inputID int64) error {
	switch {
	case key.PrivateKeyStorage == "browser":
		key.EncryptedPrivateKey = ""
		return nil
	case strings.TrimSpace(privateKeyArmored) != "":
		encrypted, err := mmcrypto.EncryptString(masterKey, privateKeyArmored)
		if err != nil {
			return err
		}
		key.EncryptedPrivateKey = encrypted
		return nil
	case inputID > 0:
		existing, err := db.GetIdentityPGPPrivateKeyForUser(ctx, userID, inputID)
		if err != nil {
			return err
		}
		key.EncryptedPrivateKey = existing.EncryptedPrivateKey
		return nil
	default:
		return nil
	}
}

func RejectDuplicatePrivateKey(ctx context.Context, db *store.Store, userID int64, key store.IdentityPGPPrivateKey) error {
	if key.ID != 0 || (strings.TrimSpace(key.Fingerprint) == "" && strings.TrimSpace(key.KeyID) == "") {
		return nil
	}
	existingKeys, err := db.ListIdentityPGPPrivateKeysForUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, existing := range existingKeys {
		sameFingerprint := key.Fingerprint != "" && strings.EqualFold(existing.Fingerprint, key.Fingerprint)
		sameKeyID := key.Fingerprint == "" && key.KeyID != "" && strings.EqualFold(existing.KeyID, key.KeyID)
		if sameFingerprint || sameKeyID {
			return ErrDuplicatePrivateKey
		}
	}
	return nil
}

func ShouldEnableAutocryptForNewIdentityKey(ctx context.Context, db *store.Store, userID int64, key store.IdentityPGPPrivateKey) (bool, error) {
	if key.ID != 0 || key.IdentityID == 0 || !key.IsActiveEncryption || key.IsDecryptOnly || strings.TrimSpace(key.PublicKeyArmored) == "" {
		return false, nil
	}
	existing, err := db.ListIdentityPGPPrivateKeysForIdentity(ctx, userID, key.IdentityID)
	if err != nil {
		return false, err
	}
	return len(existing) == 0, nil
}

func EnableIdentityAutocrypt(ctx context.Context, db *store.Store, userID, identityID int64) error {
	identity, err := db.GetMailIdentityForUser(ctx, userID, identityID)
	if err != nil {
		return err
	}
	if identity.AutocryptEnabled {
		return nil
	}
	identity.AutocryptEnabled = true
	_, err = db.UpdateMailIdentityForUser(ctx, userID, identity)
	return err
}

func NormalizePrivateKeyStorage(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "browser") {
		return "browser"
	}
	return "server"
}

func SaveDiscoveredContactKey(ctx context.Context, db *store.Store, userID int64, in ContactPublicKeyInput) (store.ContactPGPPublicKey, error) {
	email := strings.TrimSpace(in.Email)
	if store.NormalizeContactEmail(email) == "" || strings.TrimSpace(in.PublicKeyArmored) == "" {
		return store.ContactPGPPublicKey{}, store.ErrNotFound
	}
	contactID := in.ContactID
	if contactID == 0 {
		contact, err := ensureContactForDiscoveredKey(ctx, db, userID, email)
		if err != nil {
			return store.ContactPGPPublicKey{}, err
		}
		contactID = contact.ID
	}
	existingKeys, err := db.ListAllContactPGPPublicKeysForEmails(ctx, userID, []string{email})
	if err != nil {
		return store.ContactPGPPublicKey{}, err
	}
	for _, existing := range existingKeys {
		if strings.TrimSpace(existing.PublicKeyArmored) != strings.TrimSpace(in.PublicKeyArmored) {
			continue
		}
		if existing.IsPreferred && (in.ID == 0 || in.ID == existing.ID) {
			return existing, nil
		}
		existing.IsPreferred = true
		return db.UpsertContactPGPPublicKey(ctx, existing)
	}
	return db.UpsertContactPGPPublicKey(ctx, store.ContactPGPPublicKey{
		ID:               in.ID,
		UserID:           userID,
		ContactID:        contactID,
		Email:            email,
		Label:            firstNonEmpty(in.Label, email),
		Fingerprint:      in.Fingerprint,
		KeyID:            in.KeyID,
		UserIDs:          in.UserIDs,
		PublicKeyArmored: strings.TrimSpace(in.PublicKeyArmored),
		SourceKind:       in.SourceKind,
		SourceDetail:     in.SourceDetail,
		IsPreferred:      true,
	})
}

func ensureContactForDiscoveredKey(ctx context.Context, db *store.Store, userID int64, email string) (store.Contact, error) {
	if contact, err := db.GetContactByEmailForUser(ctx, userID, email); err == nil {
		return contact, nil
	} else if !store.IsNotFound(err) {
		return store.Contact{}, err
	}
	return db.CreateContact(ctx, userID, store.Contact{
		DisplayName: strings.TrimSpace(email),
		Emails: []store.ContactEmail{{
			Label:     "email",
			Email:     email,
			IsPrimary: true,
		}},
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
