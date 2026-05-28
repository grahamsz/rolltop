package config

import (
	"path/filepath"
	"testing"
)

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
}

func TestLoadAcceptsLegacyMailmirrorEnv(t *testing.T) {
	t.Setenv("MAILMIRROR_MASTER_KEY", testMasterKey)
	t.Setenv("MAILMIRROR_DATA_DIR", "/legacy-data")
	t.Setenv("MAILMIRROR_DB_PATH", "/legacy-data/mailmirror.db")
	t.Setenv("MAILMIRROR_COOKIE_SECURE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/legacy-data" {
		t.Fatalf("data dir = %q", cfg.DataDir)
	}
	if cfg.DatabasePath != filepath.Join("/legacy-data", "rolltop.db") {
		t.Fatalf("database path = %q", cfg.DatabasePath)
	}
	if !cfg.CookieSecure {
		t.Fatalf("cookie secure = false")
	}
}

func TestLoadPrefersRolltopEnvOverLegacy(t *testing.T) {
	t.Setenv("ROLLTOP_MASTER_KEY", testMasterKey)
	t.Setenv("ROLLTOP_DATA_DIR", "/rolltop-data")
	t.Setenv("ROLLTOP_DB_PATH", "/rolltop-data/mailmirror.db")
	t.Setenv("MAILMIRROR_DATA_DIR", "/legacy-data")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/rolltop-data" {
		t.Fatalf("data dir = %q", cfg.DataDir)
	}
	if cfg.DatabasePath != filepath.Join("/rolltop-data", "rolltop.db") {
		t.Fatalf("database path = %q", cfg.DatabasePath)
	}
}
