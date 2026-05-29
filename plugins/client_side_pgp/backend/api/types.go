package api

import (
	"strings"
	"time"

	"rolltop/backend/store"
	"rolltop/plugins/client_side_pgp/backend/keystore"
)

type ContactPGPKey struct {
	ID               int64  `json:"id,omitempty"`
	ContactID        int64  `json:"contact_id,omitempty"`
	Email            string `json:"email"`
	Label            string `json:"label"`
	Fingerprint      string `json:"fingerprint"`
	KeyID            string `json:"key_id"`
	UserIDs          string `json:"user_ids"`
	PublicKeyArmored string `json:"public_key_armored"`
	SourceKind       string `json:"source_kind,omitempty"`
	SourceDetail     string `json:"source_detail,omitempty"`
	IsPreferred      bool   `json:"is_preferred"`
}

type IdentityPGPPrivateKey struct {
	ID                    int64  `json:"id,omitempty"`
	IdentityID            int64  `json:"identity_id"`
	Label                 string `json:"label"`
	Fingerprint           string `json:"fingerprint"`
	KeyID                 string `json:"key_id"`
	UserIDs               string `json:"user_ids"`
	PublicKeyArmored      string `json:"public_key_armored"`
	PrivateKeyArmored     string `json:"private_key_armored,omitempty"`
	PrivateKeyStorage     string `json:"private_key_storage,omitempty"`
	RevocationCertificate string `json:"revocation_certificate,omitempty"`
	IsActiveSigning       bool   `json:"is_active_signing"`
	IsActiveEncryption    bool   `json:"is_active_encryption"`
	IsDecryptOnly         bool   `json:"is_decrypt_only"`
	CreatedAt             string `json:"created_at,omitempty"`
	UpdatedAt             string `json:"updated_at,omitempty"`
}

func ContactKeyFromStore(key store.ContactPGPPublicKey) ContactPGPKey {
	return ContactPGPKey{
		ID:               key.ID,
		ContactID:        key.ContactID,
		Email:            key.Email,
		Label:            key.Label,
		Fingerprint:      key.Fingerprint,
		KeyID:            key.KeyID,
		UserIDs:          key.UserIDs,
		PublicKeyArmored: key.PublicKeyArmored,
		SourceKind:       key.SourceKind,
		SourceDetail:     key.SourceDetail,
		IsPreferred:      key.IsPreferred,
	}
}

func ContactKeyInput(in ContactPGPKey) keystore.ContactPublicKeyInput {
	return keystore.ContactPublicKeyInput{
		ID:               in.ID,
		ContactID:        in.ContactID,
		Email:            in.Email,
		Label:            in.Label,
		Fingerprint:      in.Fingerprint,
		KeyID:            in.KeyID,
		UserIDs:          in.UserIDs,
		PublicKeyArmored: in.PublicKeyArmored,
		SourceKind:       in.SourceKind,
		SourceDetail:     in.SourceDetail,
		IsPreferred:      in.IsPreferred,
	}
}

func IdentityPrivateKeyFromStore(key store.IdentityPGPPrivateKey, includePrivate bool) IdentityPGPPrivateKey {
	out := IdentityPGPPrivateKey{
		ID:                    key.ID,
		IdentityID:            key.IdentityID,
		Label:                 key.Label,
		Fingerprint:           key.Fingerprint,
		KeyID:                 key.KeyID,
		UserIDs:               key.UserIDs,
		PublicKeyArmored:      key.PublicKeyArmored,
		PrivateKeyStorage:     firstNonEmpty(key.PrivateKeyStorage, "server"),
		RevocationCertificate: key.RevocationCertificate,
		IsActiveSigning:       key.IsActiveSigning,
		IsActiveEncryption:    key.IsActiveEncryption,
		IsDecryptOnly:         key.IsDecryptOnly,
		CreatedAt:             timeString(key.CreatedAt),
		UpdatedAt:             timeString(key.UpdatedAt),
	}
	if includePrivate {
		out.PrivateKeyArmored = key.PrivateKeyArmored
	}
	return out
}

func IdentityPrivateKeyToStore(userID int64, in IdentityPGPPrivateKey) store.IdentityPGPPrivateKey {
	return store.IdentityPGPPrivateKey{
		ID:                    in.ID,
		UserID:                userID,
		IdentityID:            in.IdentityID,
		Label:                 in.Label,
		Fingerprint:           in.Fingerprint,
		KeyID:                 in.KeyID,
		UserIDs:               in.UserIDs,
		PublicKeyArmored:      in.PublicKeyArmored,
		PrivateKeyStorage:     keystore.NormalizePrivateKeyStorage(in.PrivateKeyStorage),
		RevocationCertificate: in.RevocationCertificate,
		IsActiveSigning:       in.IsActiveSigning,
		IsActiveEncryption:    in.IsActiveEncryption,
		IsDecryptOnly:         in.IsDecryptOnly,
	}
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
