// File overview: PGP key API endpoints. Private-key passphrases stay browser-only;
// the backend only stores and unwraps the server-side encrypted armored key text.

package web

import (
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
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	emails := r.URL.Query()["email"]
	if joined := strings.TrimSpace(r.URL.Query().Get("emails")); joined != "" {
		emails = append(emails, strings.Split(joined, ",")...)
	}
	keys, err := s.store.ListContactPGPPublicKeysForEmails(r.Context(), cu.User.ID, emails)
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
