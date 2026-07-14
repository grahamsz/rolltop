package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
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
		cmd.Env = append(os.Environ(), "GOCACHE=/tmp/rolltop-go-build")
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
	manifests, manager := testClientSidePGPBackendPlugins(t)
	db, err := store.OpenServerWithPluginManifests(filepath.Join(t.TempDir(), "rolltop.db"), t.TempDir(), manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
		t.Fatal(err)
	}
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

func TestEnabledBackendPluginsSkipsLoadFailuresAndReportsAdminError(t *testing.T) {
	ctx := context.Background()
	pluginID := "missing_backend"
	root := t.TempDir()
	pluginDir := filepath.Join(root, pluginID)
	if err := os.MkdirAll(filepath.Join(pluginDir, "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"id": "missing_backend",
		"name": "Missing Backend",
		"description": "Plugin with a missing backend binary",
		"backend": {"kind": "go-plugin", "binary": "backend/missing_backend.so"}
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	manifests, err := plugins.LoadManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenServerWithPluginManifests(filepath.Join(t.TempDir(), "rolltop.db"), t.TempDir(), manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetPluginEnabled(ctx, pluginID, true); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store:              db,
		pluginManifests:    manifests,
		backendPlugins:     plugins.NewBackendManager(root, manifests),
		protectedAPIRoutes: newProtectedAPIRouteRegistry(),
	}
	backendPlugins, err := server.enabledBackendPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(backendPlugins) != 0 {
		t.Fatalf("enabled backend plugins = %d, want 0", len(backendPlugins))
	}
	settings, err := db.ListPluginSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apiSettings := server.apiAdminPluginSettings(settings)
	var backendError string
	for _, setting := range apiSettings {
		if setting.ID == pluginID {
			backendError = setting.BackendError
			break
		}
	}
	if backendError == "" {
		t.Fatal("backend error was not reported")
	}
}

func TestQueueAccountMailboxSyncRequiresOwnedConfiguredDestination(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerAccount, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: owner.ID, Email: owner.Email, Host: "imap.owner.test", Port: 993,
		Username: owner.Email, EncryptedPassword: "encrypted", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerMailbox, err := db.GetOrCreateMailbox(ctx, owner.ID, ownerAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: other.ID, Email: other.Email, Host: "imap.other.test", Port: 993,
		Username: other.Email, EncryptedPassword: "encrypted", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherMailbox, err := db.GetOrCreateMailbox(ctx, other.ID, otherAccount.ID, "Private")
	if err != nil {
		t.Fatal(err)
	}

	runnerCtx, stopRunner := context.WithCancel(context.Background())
	stopRunner()
	runner := syncer.NewRunnerWithContext(runnerCtx, &syncer.Service{Store: db})
	server := &Server{store: db, syncRunner: runner}
	if err := server.QueueAccountMailboxSync(ctx, owner.ID, ownerAccount.ID, ownerMailbox.Name); err == nil {
		t.Fatal("stopped mailbox runner accepted new work")
	}
	if err := server.QueueAccountMailboxSync(ctx, owner.ID, otherAccount.ID, otherMailbox.Name); err == nil {
		t.Fatal("cross-user destination account was accepted")
	}
	if err := server.QueueAccountMailboxSync(ctx, owner.ID, ownerAccount.ID, otherMailbox.Name); err == nil {
		t.Fatal("mailbox outside the selected destination account was accepted")
	}
}

func TestServerCloseStopsBackendPluginsBeforeStoreClose(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.CreateUser(ctx, "shutdown@example.test", "Shutdown", "hash", false); err != nil {
		t.Fatal(err)
	}

	stopFailure := errors.New("stop failed")
	var stopOrder []string
	first := &backendLifecycleTestPlugin{id: "a-first", stopOrder: &stopOrder}
	second := &backendLifecycleTestPlugin{id: "z-second", stopOrder: &stopOrder, stopErr: stopFailure}
	server := &Server{
		store:                 db,
		protectedAPIRoutes:    newProtectedAPIRouteRegistry(),
		publicAPIRoutes:       newProtectedAPIRouteRegistry(),
		startedBackendPlugins: map[string]plugins.BackendPlugin{second.id: second, first.id: first},
	}
	noop := func(plugins.APIHost, string, http.ResponseWriter, *http.Request) {}
	if _, err := server.RegisterProtectedAPI(first.id, plugins.ProtectedAPIRoute{Path: "plugins/a-first", Handle: noop}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.RegisterPublicAPI(second.id, plugins.PublicAPIRoute{Path: "plugins/z-second", Handle: noop}); err != nil {
		t.Fatal(err)
	}

	if err := server.Close(); !errors.Is(err, stopFailure) {
		t.Fatalf("Close error = %v, want stop failure", err)
	}
	if first.stopCalls != 1 || second.stopCalls != 1 {
		t.Fatalf("stop calls = %d, %d; want one each", first.stopCalls, second.stopCalls)
	}
	if !first.storeAvailable || !second.storeAvailable {
		t.Fatal("plugin Stop ran after its host store was closed")
	}
	if len(stopOrder) != 2 || stopOrder[0] != first.id || stopOrder[1] != second.id {
		t.Fatalf("stop order = %#v", stopOrder)
	}
	if _, ok := server.protectedAPIRouteRegistry().match("plugins/a-first"); ok {
		t.Fatal("protected plugin route remained after Close")
	}
	if _, ok := server.publicAPIRouteRegistry().match("plugins/z-second"); ok {
		t.Fatal("public plugin route remained after Close")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close error = %v", err)
	}
	if first.stopCalls != 1 || second.stopCalls != 1 {
		t.Fatalf("second Close repeated Stop: %d, %d", first.stopCalls, second.stopCalls)
	}
	if _, _, err := server.startBackendPlugin(ctx, first.id); !errors.Is(err, errBackendPluginHostClosed) {
		t.Fatalf("start after Close error = %v", err)
	}
}

func TestServerCloseWaitsForConcurrentPluginStop(t *testing.T) {
	stopStarted := make(chan struct{})
	stopRelease := make(chan struct{})
	plugin := &backendLifecycleTestPlugin{id: "blocking", stopStarted: stopStarted, stopRelease: stopRelease}
	server := &Server{
		protectedAPIRoutes:    newProtectedAPIRouteRegistry(),
		publicAPIRoutes:       newProtectedAPIRouteRegistry(),
		startedBackendPlugins: map[string]plugins.BackendPlugin{plugin.id: plugin},
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- server.stopBackendPlugin(plugin.id) }()
	select {
	case <-stopStarted:
	case <-time.After(time.Second):
		t.Fatal("plugin Stop did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- server.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Server.Close returned before plugin Stop: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(stopRelease)
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if plugin.stopCalls != 1 {
		t.Fatalf("plugin Stop calls = %d", plugin.stopCalls)
	}
}

type backendLifecycleTestPlugin struct {
	id             string
	stopCalls      int
	stopOrder      *[]string
	stopErr        error
	storeAvailable bool
	stopStarted    chan struct{}
	stopRelease    <-chan struct{}
}

func (p *backendLifecycleTestPlugin) ID() string { return p.id }

func (p *backendLifecycleTestPlugin) Start(plugins.BackendStartHost) error { return nil }

func (p *backendLifecycleTestPlugin) Stop(host plugins.BackendStartHost) error {
	p.stopCalls++
	if p.stopStarted != nil {
		close(p.stopStarted)
	}
	if p.stopRelease != nil {
		<-p.stopRelease
	}
	if p.stopOrder != nil {
		*p.stopOrder = append(*p.stopOrder, p.id)
	}
	if st, ok := host.Store().(*store.Store); ok && st != nil {
		_, err := st.CountUsers(context.Background())
		p.storeAvailable = err == nil
	}
	return p.stopErr
}

type execBuildError struct {
	err error
	out string
}

func (e *execBuildError) Error() string {
	return e.err.Error() + ": " + e.out
}
