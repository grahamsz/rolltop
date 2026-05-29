package syncer

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

const autocryptTestRaw = "From: Alice <alice@example.test>\r\nAutocrypt: addr=alice@example.test; prefer-encrypt=mutual; keydata=AQIDBAUGBwg=\r\nSubject: hello\r\n\r\nbody"

func TestDiscoverAutocryptHeadersStoresSenderKey(t *testing.T) {
	ctx := context.Background()
	db := autocryptTestStore(t)
	defer db.Close()
	user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: db, PluginDir: testClientSidePGPPluginDir(t)}
	if err := svc.importIncomingMessageHooks(ctx, user.ID, []byte(autocryptTestRaw), "Alice <alice@example.test>"); err != nil {
		t.Fatal(err)
	}
	keys, err := db.ListAllContactPGPPublicKeysForEmails(ctx, user.ID, []string{"alice@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("stored key count = %d, want 1", len(keys))
	}
	if keys[0].Email != "alice@example.test" || !keys[0].IsPreferred || keys[0].PublicKeyArmored == "" {
		t.Fatalf("stored key = %+v", keys[0])
	}
}

func TestDiscoverAutocryptHeadersRequiresPluginAndMatchingFrom(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		enablePGP  bool
		parsedFrom string
	}{
		{name: "plugin disabled", enablePGP: false, parsedFrom: "Alice <alice@example.test>"},
		{name: "from mismatch", enablePGP: true, parsedFrom: "Mallory <mallory@example.test>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := autocryptTestStore(t)
			defer db.Close()
			user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.enablePGP {
				if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
					t.Fatal(err)
				}
			}
			svc := &Service{Store: db}
			if tc.enablePGP {
				svc.PluginDir = testClientSidePGPPluginDir(t)
			}
			if err := svc.importIncomingMessageHooks(ctx, user.ID, []byte(autocryptTestRaw), tc.parsedFrom); err != nil {
				t.Fatal(err)
			}
			keys, err := db.ListAllContactPGPPublicKeysForEmails(ctx, user.ID, []string{"alice@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			if len(keys) != 0 {
				t.Fatalf("stored key count = %d, want 0", len(keys))
			}
		})
	}
}

var (
	testPGPPluginOnce sync.Once
	testPGPPluginRoot string
	testPGPPluginErr  error
)

func testClientSidePGPPluginDir(t *testing.T) string {
	t.Helper()
	testPGPPluginOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		testPGPPluginRoot, testPGPPluginErr = os.MkdirTemp("", "rolltop-syncer-pgp-plugin-")
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

type execBuildError struct {
	err error
	out string
}

func (e *execBuildError) Error() string {
	return e.err.Error() + ": " + e.out
}

func autocryptTestStore(t *testing.T) *store.Store {
	t.Helper()
	root := testClientSidePGPPluginDir(t)
	manifests, err := plugins.LoadManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := plugins.NewBackendManager(root, manifests)
	if _, ok, err := manager.Plugin(plugins.ClientSidePGP); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("client_side_pgp backend plugin was not discovered")
	}
	db, err := store.OpenServerWithPluginManifests(filepath.Join(t.TempDir(), "rolltop.db"), t.TempDir(), manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
