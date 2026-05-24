package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	switch {
	case path == "bootstrap":
		s.apiBootstrap(w, r)
	case path == "setup":
		s.apiSetup(w, r)
	case path == "login":
		s.apiLogin(w, r)
	case path == "logout":
		s.apiLogout(w, r)
	case path == "profile":
		s.apiProfile(w, r)
	case path == "mail":
		s.apiMail(w, r)
	case path == "search":
		s.apiSearch(w, r)
	case path == "compose":
		s.apiCompose(w, r)
	case path == "sync/status":
		s.apiSyncStatus(w, r)
	case path == "events":
		s.apiEvents(w, r)
	case path == "storage":
		s.apiStorage(w, r)
	case path == "brand-icons":
		s.apiBrandIcons(w, r)
	case path == "account":
		s.apiAccount(w, r)
	case path == "account/sync":
		s.apiAccountSync(w, r)
	case strings.HasPrefix(path, "account/folders/"):
		s.apiAccountFolder(w, r, strings.TrimPrefix(path, "account/folders/"))
	case path == "admin/users":
		s.apiAdminUsers(w, r)
	case path == "admin/remote-image-blocklist":
		s.apiAdminRemoteImageBlocklist(w, r)
	case path == "messages/bulk-move":
		s.apiBulkMoveMessages(w, r)
	case strings.HasPrefix(path, "messages/"):
		s.apiMessagePath(w, r, strings.TrimPrefix(path, "messages/"))
	case strings.HasPrefix(path, "sync-runs/"):
		s.apiSyncRun(w, r, strings.TrimPrefix(path, "sync-runs/"))
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) requireAPIAuth(w http.ResponseWriter, r *http.Request) (currentUser, bool) {
	cu, ok := current(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "login required")
		return currentUser{}, false
	}
	return cu, true
}

func (s *Server) requireAPIAdmin(w http.ResponseWriter, r *http.Request) (currentUser, bool) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return currentUser{}, false
	}
	if !cu.User.IsAdmin {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return currentUser{}, false
	}
	return cu, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}
