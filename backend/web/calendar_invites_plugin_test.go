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

type calendarPluginAPIHost struct {
	*Server
	userID int64
}

func (h calendarPluginAPIHost) RequireAPIAuth(http.ResponseWriter, *http.Request) (plugins.CurrentUser, bool) {
	return plugins.CurrentUser{UserID: h.userID}, true
}
func (h calendarPluginAPIHost) VerifyCSRF(http.ResponseWriter, *http.Request) bool { return true }

// Build the real module and load it through the production ABI, then exercise
// its import hook, migrations and protected routes with two separate users.
func TestCalendarInvitesModuleLifecycleAndTenantRoutes(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	root := t.TempDir()
	pluginDir := filepath.Join(root, plugins.CalendarInvites)
	backendDir := filepath.Join(pluginDir, "backend")
	if err := os.MkdirAll(backendDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(repoRoot, "plugins", plugins.CalendarInvites, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", filepath.Join(backendDir, "calendar_invites.so"), "./plugins/calendar_invites/backend")
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
	backend, ok, err := manager.Plugin(plugins.CalendarInvites)
	if err != nil || !ok || backend == nil {
		t.Fatalf("load module: found=%v error=%v", ok, err)
	}
	if backend.ID() != plugins.CalendarInvites {
		t.Fatalf("module ID = %q", backend.ID())
	}
	hook, ok := backend.(plugins.IncomingMessageHook)
	if !ok {
		t.Fatal("module does not expose its invitation import hook")
	}
	ctx := context.Background()
	st, err := store.OpenServerWithPluginManifests(filepath.Join(t.TempDir(), "rolltop.db"), t.TempDir(), manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := st.CreateUser(ctx, "calendar-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "calendar-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: st, pluginManifests: manifests, backendPlugins: manager, protectedAPIRoutes: newProtectedAPIRouteRegistry()}
	if server.PluginEnabled(ctx, plugins.CalendarInvites) {
		t.Fatal("calendar plugin enabled by default")
	}
	if err := st.SetPluginEnabled(ctx, plugins.CalendarInvites, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := server.startBackendPlugin(ctx, plugins.CalendarInvites); err != nil || !ok {
		t.Fatalf("start module: %v, %v", ok, err)
	}
	defer server.stopBackendPlugin(plugins.CalendarInvites)
	raw := "From: organizer@example.test\r\nContent-Type: text/calendar; method=REQUEST\r\n\r\nBEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:module-test\r\nSEQUENCE:1\r\nSUMMARY:Private meeting\r\nDTSTART:20261008T140000Z\r\nORGANIZER:mailto:organizer@example.test\r\nATTENDEE:mailto:calendar-owner@example.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := hook.ImportIncomingMessage(ctx, server, owner.ID, []byte(raw), ""); err != nil {
		t.Fatal(err)
	}
	path := "plugins/calendar_invites/invites"
	route, ok := server.protectedAPIRouteRegistry().match(path)
	if !ok {
		t.Fatal("invites route not registered")
	}
	for _, test := range []struct {
		userID int64
		want   bool
	}{{owner.ID, true}, {other.ID, false}} {
		response := httptest.NewRecorder()
		route.handler(calendarPluginAPIHost{Server: server, userID: test.userID}, path, response, httptest.NewRequest(http.MethodGet, "/api/"+path+"?user_id=1", nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "Private meeting") != test.want {
			t.Fatalf("user %d response: %d %s", test.userID, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	rsvpPath := path + "/1/rsvp"
	route.handler(calendarPluginAPIHost{Server: server, userID: other.ID}, rsvpPath, response, httptest.NewRequest(http.MethodPost, "/api/"+rsvpPath, strings.NewReader(`{"partstat":"ACCEPTED","sequence":1}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-user RSVP: %d %s", response.Code, response.Body.String())
	}
	if err := server.stopBackendPlugin(plugins.CalendarInvites); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.protectedAPIRouteRegistry().match(path); ok {
		t.Fatal("invites route remained after module stop")
	}
}
