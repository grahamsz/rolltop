// File overview: PGP key API endpoints. Private-key passphrases stay browser-only;
// the backend stores master-key-encrypted armored private keys and serves them
// back to the authenticated browser for unlock/export.

package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	mmcrypto "mailmirror/backend/crypto"
	"mailmirror/backend/plugins"
	"mailmirror/backend/store"
)

func (s *Server) apiPGPPrivateKeys(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.pluginEnabled(r.Context(), plugins.ClientSidePGP) {
		writeAPIError(w, http.StatusNotFound, "PGP plugin is not enabled.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := s.store.ListIdentityPGPPrivateKeysForUser(r.Context(), cu.User.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		out := make([]apiIdentityPGPPrivateKey, 0, len(keys))
		for _, key := range keys {
			if strings.TrimSpace(key.EncryptedPrivateKey) != "" {
				plain, err := mmcrypto.DecryptString(s.masterKey, key.EncryptedPrivateKey)
				if err != nil {
					s.serverError(w, err)
					return
				}
				key.PrivateKeyArmored = plain
			}
			out = append(out, apiIdentityPGPPrivateKeyFromStore(key, true))
		}
		writeJSON(w, map[string]any{"keys": out})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var in apiIdentityPGPPrivateKey
		if !decodeJSON(w, r, &in) {
			return
		}
		key := store.IdentityPGPPrivateKey{
			ID:                    in.ID,
			UserID:                cu.User.ID,
			IdentityID:            in.IdentityID,
			Label:                 in.Label,
			Fingerprint:           in.Fingerprint,
			KeyID:                 in.KeyID,
			UserIDs:               in.UserIDs,
			PublicKeyArmored:      in.PublicKeyArmored,
			RevocationCertificate: in.RevocationCertificate,
			IsActiveSigning:       in.IsActiveSigning,
			IsActiveEncryption:    in.IsActiveEncryption,
			IsDecryptOnly:         in.IsDecryptOnly,
		}
		if strings.TrimSpace(in.PrivateKeyArmored) != "" {
			encrypted, err := mmcrypto.EncryptString(s.masterKey, in.PrivateKeyArmored)
			if err != nil {
				s.serverError(w, err)
				return
			}
			key.EncryptedPrivateKey = encrypted
		} else if in.ID > 0 {
			existing, err := s.store.GetIdentityPGPPrivateKeyForUser(r.Context(), cu.User.ID, in.ID)
			if err != nil {
				if store.IsNotFound(err) {
					http.NotFound(w, r)
					return
				}
				s.serverError(w, err)
				return
			}
			key.EncryptedPrivateKey = existing.EncryptedPrivateKey
		}
		if strings.TrimSpace(key.EncryptedPrivateKey) == "" {
			writeAPIError(w, http.StatusBadRequest, "Private key is required.")
			return
		}
		if key.ID == 0 && (strings.TrimSpace(key.Fingerprint) != "" || strings.TrimSpace(key.KeyID) != "") {
			existingKeys, err := s.store.ListIdentityPGPPrivateKeysForUser(r.Context(), cu.User.ID)
			if err != nil {
				s.serverError(w, err)
				return
			}
			for _, existing := range existingKeys {
				sameFingerprint := key.Fingerprint != "" && strings.EqualFold(existing.Fingerprint, key.Fingerprint)
				sameKeyID := key.Fingerprint == "" && key.KeyID != "" && strings.EqualFold(existing.KeyID, key.KeyID)
				if sameFingerprint || sameKeyID {
					writeAPIError(w, http.StatusConflict, "This PGP private key is already saved.")
					return
				}
			}
		}
		saved, err := s.store.UpsertIdentityPGPPrivateKey(r.Context(), key)
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "Could not save PGP key.")
			return
		}
		if strings.TrimSpace(saved.EncryptedPrivateKey) != "" {
			plain, err := mmcrypto.DecryptString(s.masterKey, saved.EncryptedPrivateKey)
			if err != nil {
				s.serverError(w, err)
				return
			}
			saved.PrivateKeyArmored = plain
		}
		writeJSON(w, map[string]any{"ok": true, "key": apiIdentityPGPPrivateKeyFromStore(saved, true)})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) apiPGPPrivateKeyPath(w http.ResponseWriter, r *http.Request, rest string) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.pluginEnabled(r.Context(), plugins.ClientSidePGP) {
		writeAPIError(w, http.StatusNotFound, "PGP plugin is not enabled.")
		return
	}
	id, err := strconv.ParseInt(strings.Trim(rest, "/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if err := s.store.DeleteIdentityPGPPrivateKeyForUser(r.Context(), cu.User.ID, id); err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) apiPGPPublicKeys(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.pluginEnabled(r.Context(), plugins.ClientSidePGP) {
		writeAPIError(w, http.StatusNotFound, "PGP plugin is not enabled.")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var in apiContactPGPKey
		if !decodeJSON(w, r, &in) {
			return
		}
		saved, err := s.saveDiscoveredContactPGPKey(r.Context(), cu.User.ID, in)
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "Could not save PGP public key.")
			return
		}
		writeJSON(w, map[string]any{"ok": true, "key": apiContactPGPKeyFromStore(saved)})
		return
	default:
		methodNotAllowed(w)
		return
	}
	emails := r.URL.Query()["email"]
	if joined := strings.TrimSpace(r.URL.Query().Get("emails")); joined != "" {
		emails = append(emails, strings.Split(joined, ",")...)
	}
	var keys []store.ContactPGPPublicKey
	var err error
	if r.URL.Query().Get("all") == "1" {
		keys, err = s.store.ListAllContactPGPPublicKeysForEmails(r.Context(), cu.User.ID, emails)
	} else {
		keys, err = s.store.ListContactPGPPublicKeysForEmails(r.Context(), cu.User.ID, emails)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	out := make([]apiContactPGPKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, apiContactPGPKeyFromStore(key))
	}
	writeJSON(w, map[string]any{"keys": out})
}

func (s *Server) saveDiscoveredContactPGPKey(ctx context.Context, userID int64, in apiContactPGPKey) (store.ContactPGPPublicKey, error) {
	email := strings.TrimSpace(in.Email)
	if store.NormalizeContactEmail(email) == "" || strings.TrimSpace(in.PublicKeyArmored) == "" {
		return store.ContactPGPPublicKey{}, store.ErrNotFound
	}
	contactID := in.ContactID
	if contactID == 0 {
		contact, err := s.ensureContactForDiscoveredPGPKey(ctx, userID, email)
		if err != nil {
			return store.ContactPGPPublicKey{}, err
		}
		contactID = contact.ID
	}
	existingKeys, err := s.store.ListAllContactPGPPublicKeysForEmails(ctx, userID, []string{email})
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
		return s.store.UpsertContactPGPPublicKey(ctx, existing)
	}
	return s.store.UpsertContactPGPPublicKey(ctx, store.ContactPGPPublicKey{
		ID:               in.ID,
		UserID:           userID,
		ContactID:        contactID,
		Email:            email,
		Label:            firstNonEmpty(in.Label, email),
		Fingerprint:      in.Fingerprint,
		KeyID:            in.KeyID,
		UserIDs:          in.UserIDs,
		PublicKeyArmored: strings.TrimSpace(in.PublicKeyArmored),
		IsPreferred:      true,
	})
}

func (s *Server) ensureContactForDiscoveredPGPKey(ctx context.Context, userID int64, email string) (store.Contact, error) {
	if contact, err := s.store.GetContactByEmailForUser(ctx, userID, email); err == nil {
		return contact, nil
	} else if !store.IsNotFound(err) {
		return store.Contact{}, err
	}
	return s.store.CreateContact(ctx, userID, store.Contact{
		DisplayName: strings.TrimSpace(email),
		Emails: []store.ContactEmail{{
			Label:     "email",
			Email:     email,
			IsPrimary: true,
		}},
	})
}
