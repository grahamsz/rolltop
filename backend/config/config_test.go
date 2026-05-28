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
