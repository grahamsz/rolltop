package config

import (
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadTrustedProxies(t *testing.T) {
	t.Setenv("ROLLTOP_MASTER_KEY", testMasterKey)
	for _, tc := range []struct {
		name  string
		value string
		want  []netip.Prefix
	}{
		{name: "default"},
		{name: "whitespace", value: "   "},
		{
			name:  "addresses and networks",
			value: " 127.0.0.1, ::1, 10.2.3.4/8, 2001:db8:1::5/48 ",
			want: []netip.Prefix{
				netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128"),
				netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8:1::/48"),
			},
		},
		{
			name: "mapped IPv4", value: "::ffff:127.0.0.1, ::ffff:10.2.3.4/104",
			want: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ROLLTOP_TRUSTED_PROXIES", tc.value)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cfg.TrustedProxies, tc.want) {
				t.Fatalf("trusted proxies = %v, want %v", cfg.TrustedProxies, tc.want)
			}
		})
	}
}

func TestLoadRejectsInvalidTrustedProxies(t *testing.T) {
	t.Setenv("ROLLTOP_MASTER_KEY", testMasterKey)
	for _, value := range []string{
		"localhost", "*", "127.0.0.1:8080", "10.0.0.0/33", "::1/129",
		"127.0.0.1,", ",127.0.0.1", "127.0.0.1, ,::1", "127.0.0.1,garbage",
		"fe80::1%eth0", "::ffff:10.0.0.1/80",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ROLLTOP_TRUSTED_PROXIES", value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ROLLTOP_TRUSTED_PROXIES") {
				t.Fatalf("Load with trusted proxies %q: err = %v", value, err)
			}
		})
	}
}

const testMasterKey = "12345678901234567890123456789012"

func TestLoadUsesRolltopDefaults(t *testing.T) {
	t.Setenv("ROLLTOP_MASTER_KEY", testMasterKey)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabasePath != filepath.Join("/data", "rolltop.db") {
		t.Fatalf("database path = %q", cfg.DatabasePath)
	}
	if cfg.DataDir != "/data" {
		t.Fatalf("data dir = %q", cfg.DataDir)
	}
	wantPluginDir, err := filepath.Abs("plugins")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PluginDir != wantPluginDir {
		t.Fatalf("plugin dir = %q", cfg.PluginDir)
	}
}

func TestLoadUsesRolltopDatabasePath(t *testing.T) {
	t.Setenv("ROLLTOP_MASTER_KEY", testMasterKey)
	t.Setenv("ROLLTOP_DB_PATH", "/rolltop-data/custom.db")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabasePath != "/rolltop-data/custom.db" {
		t.Fatalf("database path = %q", cfg.DatabasePath)
	}
}

func TestLoadUsesRolltopPluginDir(t *testing.T) {
	t.Setenv("ROLLTOP_MASTER_KEY", testMasterKey)
	t.Setenv("ROLLTOP_PLUGIN_DIR", "/rolltop-plugins")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PluginDir != "/rolltop-plugins" {
		t.Fatalf("plugin dir = %q", cfg.PluginDir)
	}
}

func TestParsePublicBaseURLAcceptsOriginOnly(t *testing.T) {
	t.Setenv("ROLLTOP_PUBLIC_BASE_URL", "https://mail.example.com/")
	got, err := parsePublicBaseURL("ROLLTOP_PUBLIC_BASE_URL")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://mail.example.com" {
		t.Fatalf("base URL = %q", got)
	}
}

func TestParsePublicBaseURLRejectsPathsAndSchemes(t *testing.T) {
	for _, value := range []string{
		"https://mail.example.com/app",
		"ftp://mail.example.com",
		"//mail.example.com",
		"https://user:pass@mail.example.com",
		"https://mail.example.com/?x=1",
	} {
		t.Setenv("ROLLTOP_PUBLIC_BASE_URL", value)
		if _, err := parsePublicBaseURL("ROLLTOP_PUBLIC_BASE_URL"); err == nil {
			t.Fatalf("parsePublicBaseURL(%q) unexpectedly succeeded", value)
		}
	}
}

func TestParseInt64FallbackAndErrors(t *testing.T) {
	got, err := parseInt64("ROLLTOP_TEST_INT64", 42)
	if err != nil || got != 42 {
		t.Fatalf("parseInt64 fallback = %d, %v", got, err)
	}
	t.Setenv("ROLLTOP_TEST_INT64", "1048576")
	got, err = parseInt64("ROLLTOP_TEST_INT64", 42)
	if err != nil || got != 1048576 {
		t.Fatalf("parseInt64 value = %d, %v", got, err)
	}
	t.Setenv("ROLLTOP_TEST_INT64", "huge")
	if _, err := parseInt64("ROLLTOP_TEST_INT64", 42); err == nil {
		t.Fatal("parseInt64 accepted a non-numeric value")
	}
}

func TestLoadRejectsTinyMaxMessageBytes(t *testing.T) {
	t.Setenv("ROLLTOP_MASTER_KEY", "12345678901234567890123456789012")
	t.Setenv("ROLLTOP_MAX_MESSAGE_BYTES", "100")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a max message size below the floor")
	}
	t.Setenv("ROLLTOP_MAX_MESSAGE_BYTES", "2097152")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxMessageBytes != 2097152 {
		t.Fatalf("max message bytes = %d", cfg.MaxMessageBytes)
	}
	if cfg.PublicBaseURL != "" {
		t.Fatalf("default public base URL = %q", cfg.PublicBaseURL)
	}
}

func TestLoadValidatesLogLevel(t *testing.T) {
	t.Setenv("ROLLTOP_MASTER_KEY", testMasterKey)
	t.Setenv("ROLLTOP_LOG_LEVEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("default log level = %q", cfg.LogLevel)
	}

	t.Setenv("ROLLTOP_LOG_LEVEL", "Debug")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("log level = %q", cfg.LogLevel)
	}

	t.Setenv("ROLLTOP_LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for unknown log level")
	}
}
