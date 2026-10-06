package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func TestCardDAVModuleLifecycleAndTenantRoutes(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	root := t.TempDir()
	pluginDir := filepath.Join(root, plugins.CardDAVSync)
	backendDir := filepath.Join(pluginDir, "backend")
	if err := os.MkdirAll(backendDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(repoRoot, "plugins", plugins.CardDAVSync, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", filepath.Join(backendDir, "carddav_sync.so"), "./plugins/carddav_sync/backend")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOCACHE=/tmp/rolltop-go-build")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build module: %v\n%s", err, out)
	}
	manifests, err := plugins.LoadManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := plugins.NewBackendManager(root, manifests)
	backend, ok, err := manager.Plugin(plugins.CardDAVSync)
	if err != nil || !ok || backend == nil {
		t.Fatalf("load: %v %v", ok, err)
	}
	if backend.ID() != plugins.CardDAVSync {
		t.Fatalf("module ID: %q", backend.ID())
	}
	ctx := context.Background()
	st, err := store.OpenServerWithPluginManifests(filepath.Join(t.TempDir(), "system.db"), t.TempDir(), manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := st.CreateUser(ctx, "dav-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "dav-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: st, masterKey: []byte("0123456789abcdef0123456789abcdef"), pluginManifests: manifests, backendPlugins: manager, protectedAPIRoutes: newProtectedAPIRouteRegistry()}
	if server.PluginEnabled(ctx, plugins.CardDAVSync) {
		t.Fatal("CardDAV enabled by default")
	}
	if err := st.SetPluginEnabled(ctx, plugins.CardDAVSync, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := server.startBackendPlugin(ctx, plugins.CardDAVSync); err != nil || !ok {
		t.Fatalf("start: %v %v", ok, err)
	}
	defer server.stopBackendPlugin(plugins.CardDAVSync)
	path := "plugins/carddav_sync/routines"
	route, ok := server.protectedAPIRouteRegistry().match(path)
	if !ok {
		t.Fatal("routes not registered")
	}
	response := httptest.NewRecorder()
	body := `{"name":"Private address book","enabled":false,"server_url":"https://dav.example.test","username":"owner","password":"test-password","addressbook_url":"https://dav.example.test/book/","poll_interval_minutes":15}`
	route.handler(calendarPluginAPIHost{Server: server, userID: owner.ID}, path, response, httptest.NewRequest(http.MethodPost, "/api/"+path, strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	for _, test := range []struct {
		userID int64
		want   bool
	}{{owner.ID, true}, {other.ID, false}} {
		response := httptest.NewRecorder()
		route.handler(calendarPluginAPIHost{Server: server, userID: test.userID}, path, response, httptest.NewRequest(http.MethodGet, "/api/"+path+"?user_id=1", nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "Private address book") != test.want || strings.Contains(response.Body.String(), "test-password") {
			t.Fatalf("tenant list: %d %s", response.Code, response.Body.String())
		}
	}
	for _, test := range []struct{ method, suffix string }{{http.MethodPut, "/1"}, {http.MethodDelete, "/1"}, {http.MethodPost, "/1/test"}, {http.MethodPost, "/1/sync"}, {http.MethodGet, "/1/runs"}} {
		response := httptest.NewRecorder()
		action := path + test.suffix
		route.handler(calendarPluginAPIHost{Server: server, userID: other.ID}, action, response, httptest.NewRequest(test.method, "/api/"+action, strings.NewReader(body)))
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign %s %s: %d %s", test.method, action, response.Code, response.Body.String())
		}
	}
	if err := server.stopBackendPlugin(plugins.CardDAVSync); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.protectedAPIRouteRegistry().match(path); ok {
		t.Fatal("routes remained after module stop")
	}
}
