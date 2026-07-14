package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExperimentalMailPluginsAreDisabledByDefault(t *testing.T) {
	for _, pluginID := range []string{"remote_imap_sync", "experimental_spam_filter"} {
		t.Run(pluginID, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "plugins", pluginID, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				Experimental     bool `json:"experimental"`
				EnabledByDefault bool `json:"enabled_by_default"`
			}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			if !manifest.Experimental {
				t.Fatal("mail plugin must remain marked experimental")
			}
			if manifest.EnabledByDefault {
				t.Fatal("experimental mail plugin must be disabled by default")
			}
		})
	}
}

func TestLoadManifestsReadsThemeBundles(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "matrix_theme")
	themeDir := filepath.Join(pluginDir, "themes", "matrix")
	distDir := filepath.Join(pluginDir, "frontend_dist", "themes", "matrix")
	if err := os.MkdirAll(themeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(distDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "theme.css"), []byte(":root{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"id": "matrix_theme",
		"name": "Matrix theme",
		"description": "Adds Matrix.",
		"themes": [{"id": "matrix", "name": "Matrix", "css": "frontend_dist/themes/matrix/theme.css"}]
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	manifests, err := LoadManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 {
		t.Fatalf("manifest count = %d", len(manifests))
	}
	if manifests[0].ID != MatrixTheme || len(manifests[0].Themes) != 1 || manifests[0].Themes[0].ID != "matrix" {
		t.Fatalf("unexpected manifest: %#v", manifests[0])
	}
}

func TestLoadManifestsRejectsUnsafeThemeCSSPath(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "bad_theme")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"id": "bad_theme",
		"name": "Bad theme",
		"themes": [{"id": "bad", "name": "Bad", "css": "../theme.css"}]
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadManifests(root); err == nil {
		t.Fatal("expected unsafe path error")
	}
}
