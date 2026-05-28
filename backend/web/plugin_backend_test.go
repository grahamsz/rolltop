package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

var (
	testPGPPluginOnce sync.Once
	testPGPPluginRoot string
	testPGPPluginErr  error
)

func testClientSidePGPBackendPlugins(t *testing.T) ([]plugins.Manifest, *plugins.BackendManager) {
	t.Helper()
	root := testClientSidePGPPluginDir(t)
	manifests, err := plugins.LoadManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	return manifests, plugins.NewBackendManager(root, manifests)
}

func testClientSidePGPPluginDir(t *testing.T) string {
	t.Helper()
	testPGPPluginOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		testPGPPluginRoot, testPGPPluginErr = os.MkdirTemp("", "rolltop-pgp-backend-plugin-")
		if testPGPPluginErr != nil {
			return
		}
		pluginDir := filepath.Join(testPGPPluginRoot, plugins.ClientSidePGP)
		backendDir := filepath.Join(pluginDir, "backend")
		if err := os.MkdirAll(backendDir, 0o700); err != nil {
			testPGPPluginErr = err
			return
		}
		manifest := `{
			"id": "client_side_pgp",
			"name": "Client-side PGP",
			"description": "Test PGP backend",
			"backend": {"kind": "go-plugin", "binary": "backend/client_side_pgp.so"}
		}`
		if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
			testPGPPluginErr = err
			return
		}
		cmd := exec.Command("go", "build", "-buildmode=plugin", "-o", filepath.Join(backendDir, "client_side_pgp.so"), "./plugins/client_side_pgp/backend")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "GOCACHE=/tmp/mailmirror-go-build")
		if out, err := cmd.CombinedOutput(); err != nil {
			testPGPPluginErr = &execBuildError{err: err, out: string(out)}
		}
	})
	if testPGPPluginErr != nil {
		t.Fatal(testPGPPluginErr)
	}
	return testPGPPluginRoot
}

func TestClientSidePGPBackendTestPluginLoads(t *testing.T) {
	_, manager := testClientSidePGPBackendPlugins(t)
	plugin, ok, err := manager.Plugin(plugins.ClientSidePGP)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || plugin == nil {
		t.Fatalf("plugin loaded = %v, %#v", ok, plugin)
	}
	if plugin.ID() != plugins.ClientSidePGP {
		t.Fatalf("plugin id = %q", plugin.ID())
	}
}

func TestBackendPluginRegistersAndUnregistersProtectedAPIRoutes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
		t.Fatal(err)
	}
	manifests, manager := testClientSidePGPBackendPlugins(t)
	server := &Server{
		store:              db,
		pluginManifests:    manifests,
		backendPlugins:     manager,
		protectedAPIRoutes: newProtectedAPIRouteRegistry(),
	}
	if _, ok := server.protectedAPIRouteRegistry().match("plugins/client_side_pgp/private-keys"); ok {
		t.Fatal("PGP route registered before plugin start")
	}
	if _, ok, err := server.startBackendPlugin(ctx, plugins.ClientSidePGP); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("PGP backend plugin did not start")
	}
	if _, ok := server.protectedAPIRouteRegistry().match("plugins/client_side_pgp/private-keys"); !ok {
		t.Fatal("PGP private-key route was not registered")
	}
	if _, ok := server.protectedAPIRouteRegistry().match("plugins/client_side_pgp/private-keys/42"); !ok {
		t.Fatal("PGP private-key child route was not registered")
	}
	if err := server.stopBackendPlugin(plugins.ClientSidePGP); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.protectedAPIRouteRegistry().match("plugins/client_side_pgp/private-keys"); ok {
		t.Fatal("PGP route remained registered after plugin stop")
	}
}

type execBuildError struct {
	err error
	out string
}

func (e *execBuildError) Error() string {
	return e.err.Error() + ": " + e.out
}
