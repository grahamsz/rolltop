package store

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"rolltop/backend/plugins"
)

func TestBundledPluginMigrationsRespectDatabaseScope(t *testing.T) {
	ctx := context.Background()
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	pluginRoot := filepath.Join(t.TempDir(), "plugins")
	remoteRoot := filepath.Join(pluginRoot, plugins.RemoteImageBlocklist)
	backendDir := filepath.Join(remoteRoot, "backend")
	if err := os.MkdirAll(backendDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"id": "remote_image_blocklist",
		"name": "Remote image blocklist",
		"description": "Test remote image blocklist",
		"backend": {"kind": "go-plugin", "binary": "backend/remote_image_blocklist.so"}
	}`
	if err := os.WriteFile(filepath.Join(remoteRoot, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-a", "-buildmode=plugin", "-o", filepath.Join(backendDir, "remote_image_blocklist.so"), "./plugins/remote_image_blocklist/backend")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOCACHE=/tmp/rolltop-go-build")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	manifests, err := plugins.LoadManifests(pluginRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager := plugins.NewBackendManager(pluginRoot, manifests)
	if _, ok, err := manager.Plugin(plugins.RemoteImageBlocklist); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("remote image blocklist backend plugin was not discovered")
	}
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	db, err := OpenServer(filepath.Join(dataDir, "rolltop.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := db.CreateUser(ctx, "plugins@example.test", "Plugins", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	assertTableExists(t, ctx, db.DB(), "plugin_remote_image_blocklist_rules", true)
	assertTableExists(t, ctx, db.DB(), "identity_pgp_private_keys", false)
	assertTableExists(t, ctx, userDB, "identity_pgp_private_keys", true)
	assertPluginMigrationCount(t, ctx, db.DB(), plugins.RemoteImageBlocklist, 1)
	assertPluginMigrationCount(t, ctx, db.DB(), plugins.ClientSidePGP, 0)
	assertPluginMigrationCount(t, ctx, userDB, plugins.ClientSidePGP, 5)
}

func assertTableExists(t *testing.T, ctx context.Context, db *sql.DB, table string, want bool) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	got := count != 0
	if got != want {
		t.Fatalf("table %s exists = %v, want %v", table, got, want)
	}
}

func assertPluginMigrationCount(t *testing.T, ctx context.Context, db *sql.DB, pluginID string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_migrations WHERE plugin_id = ?`, pluginID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("plugin_migrations count for %s = %d, want %d", pluginID, count, want)
	}
}
