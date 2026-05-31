package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
	"rolltop/plugins/client_side_pgp/backend/keystore"
)

const Path = "plugins/client_side_pgp"

func PrivateKeyRoute(host plugins.APIHost, path string, w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(strings.Trim(path, "/"), Path+"/private-keys/")
	privateKeyPath(host, w, r, rest)
}

func PrivateKeys(host plugins.APIHost, _ string, w http.ResponseWriter, r *http.Request) {
	cu, ok := host.RequireAPIAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := keystore.ListIdentityPrivateKeysForUser(r.Context(), storeFromHost(host), cu.UserID)
		if err != nil {
			host.ServerError(w, err)
			return
		}
		out := make([]IdentityPGPPrivateKey, 0, len(keys))
		for _, key := range keys {
			if key.PrivateKeyStorage != "browser" && strings.TrimSpace(key.EncryptedPrivateKey) != "" {
				plain, err := mmcrypto.DecryptString(host.MasterKey(), key.EncryptedPrivateKey)
				if err != nil {
					host.ServerError(w, err)
					return
				}
				key.PrivateKeyArmored = plain
			}
			out = append(out, IdentityPrivateKeyFromStore(key, true))
		}
		host.WriteJSON(w, map[string]any{"keys": out})
	case http.MethodPost:
		savePrivateKey(host, cu.UserID, w, r)
	default:
		methodNotAllowed(w)
	}
}

func savePrivateKey(host plugins.APIHost, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in IdentityPGPPrivateKey
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	key := IdentityPrivateKeyToStore(userID, in)
	if err := keystore.FillEncryptedPrivateKey(r.Context(), storeFromHost(host), host.MasterKey(), userID, &key, in.PrivateKeyArmored, in.ID); err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		host.ServerError(w, err)
		return
	}
	if key.PrivateKeyStorage != "browser" && strings.TrimSpace(key.EncryptedPrivateKey) == "" {
		host.WriteAPIError(w, http.StatusBadRequest, "Private key is required.")
		return
	}
	if err := keystore.RejectDuplicatePrivateKey(r.Context(), storeFromHost(host), userID, key); err != nil {
		if errors.Is(err, keystore.ErrDuplicatePrivateKey) {
			host.WriteAPIError(w, http.StatusConflict, "This PGP private key is already saved.")
			return
		}
		host.ServerError(w, err)
		return
	}
	enableAutocrypt, err := keystore.ShouldEnableAutocryptForNewIdentityKey(r.Context(), storeFromHost(host), userID, key)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	saved, err := keystore.UpsertIdentityPrivateKey(r.Context(), storeFromHost(host), key)
	if store.IsNotFound(err) {
		host.WriteAPIError(w, http.StatusNotFound, fmt.Sprintf("PGP identity key target was not found: user_id=%d identity_id=%d", key.UserID, key.IdentityID))
		return
	}
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, "Could not save PGP key.")
		return
	}
	if enableAutocrypt {
		if err := keystore.EnableIdentityAutocrypt(r.Context(), storeFromHost(host), userID, saved.IdentityID); err != nil {
			host.ServerError(w, err)
			return
		}
	}
	if saved.PrivateKeyStorage != "browser" && strings.TrimSpace(saved.EncryptedPrivateKey) != "" {
		plain, err := mmcrypto.DecryptString(host.MasterKey(), saved.EncryptedPrivateKey)
		if err != nil {
			host.ServerError(w, err)
			return
		}
		saved.PrivateKeyArmored = plain
	}
	host.WriteJSON(w, map[string]any{"ok": true, "key": IdentityPrivateKeyFromStore(saved, true)})
}

func privateKeyPath(host plugins.APIHost, w http.ResponseWriter, r *http.Request, rest string) {
	cu, ok := host.RequireAPIAuth(w, r)
	if !ok {
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
	if !host.VerifyCSRF(w, r) {
		return
	}
	if err := keystore.DeleteIdentityPrivateKeyForUser(r.Context(), storeFromHost(host), cu.UserID, id); err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		host.ServerError(w, err)
		return
	}
	host.WriteJSON(w, map[string]any{"ok": true})
}

func storeFromHost(host plugins.BackendHost) *store.Store {
	db, _ := host.Store().(*store.Store)
	return db
}

func methodNotAllowed(w http.ResponseWriter) {
	w.WriteHeader(http.StatusMethodNotAllowed)
}
