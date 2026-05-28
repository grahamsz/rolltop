package api

import (
	"net/http"
	"strings"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
	"rolltop/plugins/client_side_pgp/backend/keystore"
)

func PublicKeys(host plugins.APIHost, _ string, w http.ResponseWriter, r *http.Request) {
	cu, ok := host.RequireAPIAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		savePublicKey(host, cu.UserID, w, r)
		return
	default:
		methodNotAllowed(w)
		return
	}

	keys, err := contactPublicKeysForRequest(r, storeFromHost(host), cu.UserID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	out := make([]ContactPGPKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, ContactKeyFromStore(key))
	}
	host.WriteJSON(w, map[string]any{"keys": out})
}

func savePublicKey(host plugins.APIHost, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	var in ContactPGPKey
	if !host.DecodeJSON(w, r, &in) {
		return
	}
	saved, err := keystore.SaveDiscoveredContactKey(r.Context(), storeFromHost(host), userID, ContactKeyInput(in))
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		host.WriteAPIError(w, http.StatusBadRequest, "Could not save PGP public key.")
		return
	}
	host.WriteJSON(w, map[string]any{"ok": true, "key": ContactKeyFromStore(saved)})
}

func contactPublicKeysForRequest(r *http.Request, db *store.Store, userID int64) ([]store.ContactPGPPublicKey, error) {
	emails := r.URL.Query()["email"]
	if joined := strings.TrimSpace(r.URL.Query().Get("emails")); joined != "" {
		emails = append(emails, strings.Split(joined, ",")...)
	}
	if r.URL.Query().Get("all") == "1" {
		return db.ListAllContactPGPPublicKeysForEmails(r.Context(), userID, emails)
	}
	return db.ListContactPGPPublicKeysForEmails(r.Context(), userID, emails)
}
