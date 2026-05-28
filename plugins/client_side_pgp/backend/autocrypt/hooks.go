package autocrypt

import (
	"bytes"
	"context"
	"net/mail"
	"net/textproto"
	"strings"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
	"rolltop/plugins/client_side_pgp/backend/keystore"
)

func OutboundMailHeaders(ctx context.Context, db *store.Store, userID int64, identity plugins.MailIdentityContext) ([]plugins.MailHeader, error) {
	if identity.Preferences["autocrypt_enabled"] != "true" || identity.ID == 0 {
		return nil, nil
	}
	key, err := db.ActiveIdentityPGPPublicKeyForUser(ctx, userID, identity.ID)
	if err != nil || strings.TrimSpace(key.PublicKeyArmored) == "" {
		return nil, nil
	}
	keyData, ok := KeyDataFromArmoredPublicKey(key.PublicKeyArmored)
	if !ok {
		return nil, nil
	}
	value := HeaderValue(identity.Email, keyData)
	if value == "" {
		return nil, nil
	}
	return []plugins.MailHeader{{Name: "Autocrypt", Value: value}}, nil
}

func ImportIncomingMessage(ctx context.Context, db *store.Store, userID int64, raw []byte, parsedFrom string) error {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	sender := store.NormalizeContactEmail(parsedFrom)
	if sender == "" {
		sender = store.NormalizeContactEmail(msg.Header.Get("From"))
	}
	if sender == "" {
		return nil
	}
	values := textproto.MIMEHeader(msg.Header).Values("Autocrypt")
	for _, header := range ParseHeaderValues(values) {
		if !strings.EqualFold(store.NormalizeContactEmail(header.Addr), sender) {
			continue
		}
		_, err := keystore.SaveDiscoveredContactKey(ctx, db, userID, keystore.ContactPublicKeyInput{
			Email:            header.Addr,
			Label:            header.Addr,
			PublicKeyArmored: header.PublicKey,
			IsPreferred:      true,
		})
		return err
	}
	return nil
}
