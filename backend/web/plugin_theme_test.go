package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func TestAvailableThemesIncludesEnabledPluginThemes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &Server{
		store: db,
		pluginManifests: []plugins.Manifest{{
			ID:               plugins.MatrixTheme,
			EnabledByDefault: true,
			Themes: []plugins.ThemeBundle{{
				ID:   "matrix",
				Name: "Matrix",
				CSS:  "themes/matrix/theme.css",
			}},
		}},
	}
	if err := db.SyncPluginDefinitions(ctx, plugins.DefinitionsFromManifests(server.pluginManifests)); err != nil {
		t.Fatal(err)
	}

	themes := server.availableThemes(ctx)
	if !slices.ContainsFunc(themes, func(theme apiThemeDefinition) bool {
		return theme.ID == "matrix" && theme.PluginID == plugins.MatrixTheme && theme.CSSURL == "/plugins/matrix_theme/assets/themes/matrix/theme.css"
	}) {
		t.Fatalf("missing matrix plugin theme: %#v", themes)
	}

	if err := db.SetPluginEnabled(ctx, plugins.MatrixTheme, false); err != nil {
		t.Fatal(err)
	}
	themes = server.availableThemes(ctx)
	if slices.ContainsFunc(themes, func(theme apiThemeDefinition) bool { return theme.ID == "matrix" }) {
		t.Fatalf("matrix theme remained available after disabling plugin: %#v", themes)
	}
}

func TestPluginAssetRouteServesEnabledThemeAsset(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pluginRoot := t.TempDir()
	writeMatrixThemePlugin(t, pluginRoot)
	server, err := New(Options{Store: db, PluginDir: pluginRoot})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/matrix_theme/assets/themes/matrix/theme.css", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != ":root[data-theme=\"matrix\"]{--accent:#00f5a0}" {
		t.Fatalf("asset body = %q", body)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/plugins/matrix_theme/assets/manifest.json", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("manifest status = %d", rec.Code)
	}

	if err := db.SetPluginEnabled(ctx, plugins.MatrixTheme, false); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/plugins/matrix_theme/assets/themes/matrix/theme.css", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d", rec.Code)
	}
}

func TestPluginAssetRouteServesEnabledFrontendPluginAssets(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pluginRoot := t.TempDir()
	writeFrontendPlugin(t, pluginRoot)
	server, err := New(Options{Store: db, PluginDir: pluginRoot})
	if err != nil {
		t.Fatal(err)
	}

	plugins := server.frontendPlugins(ctx)
	if len(plugins) != 1 || plugins[0].ID != "frontend_test_plugin" ||
		!strings.HasPrefix(plugins[0].ModuleURL, "/plugins/frontend_test_plugin/assets/frontend/index.js?v=") ||
		!strings.HasPrefix(plugins[0].CSSURL, "/plugins/frontend_test_plugin/assets/styles/plugin.css?v=") {
		t.Fatalf("frontend plugins = %#v", plugins)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/plugins/frontend_test_plugin/assets/frontend/index.js", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "export default {};" {
		t.Fatalf("frontend asset body = %q", body)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/plugins/frontend_test_plugin/assets/frontend/chunks/openpgp.js", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk status = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/plugins/frontend_test_plugin/assets/styles/plugin.css", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("css status = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/plugins/frontend_test_plugin/assets/manifest.json", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("manifest status = %d", rec.Code)
	}

	if err := db.SetPluginEnabled(ctx, "frontend_test_plugin", false); err != nil {
		t.Fatal(err)
	}
	if plugins := server.frontendPlugins(ctx); len(plugins) != 0 {
		t.Fatalf("disabled frontend plugins = %#v", plugins)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/plugins/frontend_test_plugin/assets/frontend/index.js", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d", rec.Code)
	}
}

func writeMatrixThemePlugin(t *testing.T, root string) {
	t.Helper()
	pluginDir := filepath.Join(root, "matrix_theme")
	themeDir := filepath.Join(pluginDir, "themes", "matrix")
	if err := os.MkdirAll(themeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "theme.css"), []byte(`:root[data-theme="matrix"]{--accent:#00f5a0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"id": "matrix_theme",
		"name": "Matrix theme",
		"description": "Adds Matrix.",
		"enabled_by_default": true,
		"themes": [{"id": "matrix", "name": "Matrix", "css": "themes/matrix/theme.css"}]
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFrontendPlugin(t *testing.T, root string) {
	t.Helper()
	pluginDir := filepath.Join(root, "frontend_test_plugin")
	frontendDir := filepath.Join(pluginDir, "frontend")
	chunkDir := filepath.Join(frontendDir, "chunks")
	styleDir := filepath.Join(pluginDir, "styles")
	if err := os.MkdirAll(chunkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(styleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontendDir, "index.js"), []byte(`export default {};`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chunkDir, "openpgp.js"), []byte(`export const ok = true;`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(styleDir, "plugin.css"), []byte(`.plugin{display:block}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"id": "frontend_test_plugin",
		"name": "Frontend test plugin",
		"description": "Adds browser module assets.",
		"enabled_by_default": true,
		"frontend": {"module": "frontend/index.js", "css": "styles/plugin.css"}
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}
