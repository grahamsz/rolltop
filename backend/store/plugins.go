// File overview: Plugin registry persistence. It stores plugin enablement, records plugin migration checksums, and applies system-level plugin migrations.

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"mailmirror/backend/plugins"
	remoteimageblocklist "mailmirror/backend/plugins/remote_image_blocklist"
)

type PluginSetting struct {
	ID               string
	Name             string
	Description      string
	Enabled          bool
	EnabledByDefault bool
	Heavy            bool
	CreatedAt        int64
	UpdatedAt        int64
}

func (s *Store) initPluginTables(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS plugin_settings (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL,
			enabled_by_default INTEGER NOT NULL,
			heavy INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS plugin_migrations (
			plugin_id TEXT NOT NULL,
			migration_id TEXT NOT NULL,
			applied_at INTEGER NOT NULL,
			app_version TEXT NOT NULL DEFAULT '',
			checksum TEXT NOT NULL,
			PRIMARY KEY(plugin_id, migration_id)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return s.seedPluginSettings(ctx)
}

func (s *Store) seedPluginSettings(ctx context.Context) error {
	ts := nowUnix()
	for _, def := range plugins.All() {
		_, err := s.db.ExecContext(ctx, `INSERT INTO plugin_settings
				(id, name, description, enabled, enabled_by_default, heavy, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				description = excluded.description,
				enabled_by_default = excluded.enabled_by_default,
				heavy = excluded.heavy,
				updated_at = excluded.updated_at`,
			def.ID, def.Name, def.Description, boolInt(def.EnabledByDefault), boolInt(def.EnabledByDefault), boolInt(def.Heavy), ts, ts)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListPluginSettings(ctx context.Context) ([]PluginSetting, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, enabled, enabled_by_default, heavy, created_at, updated_at
		FROM plugin_settings ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PluginSetting
	for rows.Next() {
		var setting PluginSetting
		var enabled, enabledByDefault, heavy int
		if err := rows.Scan(&setting.ID, &setting.Name, &setting.Description, &enabled, &enabledByDefault, &heavy, &setting.CreatedAt, &setting.UpdatedAt); err != nil {
			return nil, err
		}
		setting.Enabled = enabled != 0
		setting.EnabledByDefault = enabledByDefault != 0
		setting.Heavy = heavy != 0
		out = append(out, setting)
	}
	return out, rows.Err()
}

func (s *Store) PluginEnabled(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM plugin_settings WHERE id = ?`, id).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		def, ok := plugins.Lookup(id)
		if !ok {
			return false, nil
		}
		return def.EnabledByDefault, nil
	}
	return enabled != 0, err
}

func (s *Store) SetPluginEnabled(ctx context.Context, id string, enabled bool) error {
	def, ok := plugins.Lookup(strings.TrimSpace(id))
	if !ok {
		return ErrNotFound
	}
	if enabled {
		if err := s.ApplyPluginMigrations(ctx, def.ID); err != nil {
			return err
		}
	}
	ts := nowUnix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO plugin_settings
			(id, name, description, enabled, enabled_by_default, heavy, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			enabled = excluded.enabled,
			enabled_by_default = excluded.enabled_by_default,
			heavy = excluded.heavy,
			updated_at = excluded.updated_at`,
		def.ID, def.Name, def.Description, boolInt(enabled), boolInt(def.EnabledByDefault), boolInt(def.Heavy), ts, ts)
	return err
}

func (s *Store) ApplyEnabledPluginMigrations(ctx context.Context) error {
	settings, err := s.ListPluginSettings(ctx)
	if err != nil {
		return err
	}
	for _, setting := range settings {
		if !setting.Enabled {
			continue
		}
		if err := s.ApplyPluginMigrations(ctx, setting.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ApplyPluginMigrations(ctx context.Context, pluginID string) error {
	pluginID = strings.TrimSpace(pluginID)
	if _, ok := plugins.Lookup(pluginID); !ok {
		return ErrNotFound
	}
	for _, migration := range pluginMigrations() {
		if migration.PluginID != pluginID {
			continue
		}
		if err := s.applyPluginMigration(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyPluginMigration(ctx context.Context, migration plugins.Migration) error {
	checksum := pluginMigrationChecksum(migration)
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT checksum FROM plugin_migrations WHERE plugin_id = ? AND migration_id = ?`,
		migration.PluginID, migration.ID).Scan(&existing)
	if err == nil {
		if existing != checksum {
			return fmt.Errorf("plugin migration checksum mismatch for %s/%s", migration.PluginID, migration.ID)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range migration.Statements {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply plugin migration %s/%s: %w", migration.PluginID, migration.ID, err)
		}
	}
	if migration.Apply != nil {
		if err := migration.Apply(ctx, tx); err != nil {
			return fmt.Errorf("apply plugin migration %s/%s: %w", migration.PluginID, migration.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_migrations (plugin_id, migration_id, applied_at, checksum)
		VALUES (?, ?, ?, ?)`, migration.PluginID, migration.ID, nowUnix(), checksum); err != nil {
		return err
	}
	return tx.Commit()
}

func pluginMigrationChecksum(m plugins.Migration) string {
	h := sha256.New()
	_, _ = h.Write([]byte(m.PluginID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(m.ID))
	for _, stmt := range m.Statements {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strings.TrimSpace(stmt)))
	}
	if m.Apply != nil {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte("custom"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func pluginMigrations() []plugins.Migration {
	// User-owned plugin tables are part of the user schema migration.
	// Runtime plugin migrations only manage system-level plugin tables.
	return remoteimageblocklist.Migrations()
}
