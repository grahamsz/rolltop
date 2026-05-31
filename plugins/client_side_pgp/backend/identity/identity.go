package identity

import (
	"context"
	"errors"
	"strings"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
	"rolltop/plugins/client_side_pgp/backend/keystore"
)

func Security(ctx context.Context, db *store.Store, userID int64, identity plugins.MailIdentityContext) (plugins.IdentitySecurityInfo, error) {
	if identity.ID == 0 {
		return plugins.IdentitySecurityInfo{}, store.ErrNotFound
	}
	key, err := keystore.ActiveIdentityPublicKeyForUser(ctx, db, userID, identity.ID)
	if err != nil {
		return plugins.IdentitySecurityInfo{}, err
	}
	return plugins.IdentitySecurityInfo{
		IdentityID:     identity.ID,
		PublicMaterial: key.PublicKeyArmored,
		HasSecret:      strings.TrimSpace(key.PublicKeyArmored) != "",
		Metadata:       map[string]string{"kind": "openpgp"},
	}, nil
}

func PublicKeyAttachment(ctx context.Context, db *store.Store, userID int64, identity plugins.MailIdentityContext, purpose string) (plugins.Attachment, error) {
	if strings.TrimSpace(purpose) != "public-key" {
		return plugins.Attachment{}, plugins.ErrUnsupported
	}
	key, err := activePublicKey(ctx, db, userID, identity.ID)
	if err != nil {
		return plugins.Attachment{}, err
	}
	return plugins.Attachment{
		Filename:    publicKeyFilename(identity.Email),
		ContentType: "application/pgp-keys",
		Inline:      false,
		Data:        []byte(strings.TrimSpace(key.PublicKeyArmored) + "\n"),
	}, nil
}

func activePublicKey(ctx context.Context, db *store.Store, userID, identityID int64) (store.IdentityPGPPrivateKey, error) {
	if identityID == 0 {
		return store.IdentityPGPPrivateKey{}, errors.New("this identity does not have a PGP public key")
	}
	key, err := keystore.ActiveIdentityPublicKeyForUser(ctx, db, userID, identityID)
	if err != nil {
		if store.IsNotFound(err) {
			return store.IdentityPGPPrivateKey{}, errors.New("this identity does not have a PGP public key")
		}
		return store.IdentityPGPPrivateKey{}, err
	}
	return key, nil
}

func publicKeyFilename(email string) string {
	filename := strings.NewReplacer("@", "-", ".", "-").Replace(store.NormalizeContactEmail(email))
	if strings.TrimSpace(filename) == "" {
		return "public-key.asc"
	}
	return filename + ".asc"
}
