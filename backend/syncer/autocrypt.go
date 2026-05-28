// File overview: Autocrypt key discovery during message sync.

package syncer

import (
	"bytes"
	"context"
	"net/mail"
	"net/textproto"
	"strings"

	"mailmirror/backend/autocrypt"
	"mailmirror/backend/plugins"
	"mailmirror/backend/store"
)

func (s *Service) discoverAutocryptHeaders(ctx context.Context, userID int64, raw []byte, parsedFrom string) error {
	if !s.pluginEnabled(ctx, plugins.ClientSidePGP) {
		return nil
	}
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
	for _, header := range autocrypt.ParseHeaderValues(values) {
		if !strings.EqualFold(store.NormalizeContactEmail(header.Addr), sender) {
			continue
		}
		if err := s.saveDiscoveredAutocryptKey(ctx, userID, header.Addr, header.PublicKey); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func (s *Service) saveDiscoveredAutocryptKey(ctx context.Context, userID int64, email, publicKey string) error {
	email = strings.TrimSpace(email)
	publicKey = strings.TrimSpace(publicKey)
	if store.NormalizeContactEmail(email) == "" || publicKey == "" {
		return nil
	}
	existing, err := s.Store.ListAllContactPGPPublicKeysForEmails(ctx, userID, []string{email})
	if err != nil {
		return err
	}
	for _, key := range existing {
		if strings.TrimSpace(key.PublicKeyArmored) == publicKey {
			if key.IsPreferred {
				return nil
			}
			key.IsPreferred = true
			_, err := s.Store.UpsertContactPGPPublicKey(ctx, key)
			return err
		}
	}
	contact, err := s.autocryptContact(ctx, userID, email)
	if err != nil {
		return err
	}
	_, err = s.Store.UpsertContactPGPPublicKey(ctx, store.ContactPGPPublicKey{
		UserID:           userID,
		ContactID:        contact.ID,
		Email:            email,
		Label:            email,
		PublicKeyArmored: publicKey,
		IsPreferred:      true,
	})
	return err
}

func (s *Service) autocryptContact(ctx context.Context, userID int64, email string) (store.Contact, error) {
	if contact, err := s.Store.GetContactByEmailForUser(ctx, userID, email); err == nil {
		return contact, nil
	} else if !store.IsNotFound(err) {
		return store.Contact{}, err
	}
	return s.Store.CreateContact(ctx, userID, store.Contact{
		DisplayName: email,
		Emails: []store.ContactEmail{{
			Label:     "email",
			Email:     email,
			IsPrimary: true,
		}},
	})
}
