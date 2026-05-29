// File overview: File-backed plugin migration discovery. Runtime plugins keep
// schema changes in migrations/<scope>/*.sql so startup can inspect the plugin
// folder instead of relying on a central import list.

package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const ensureColumnDirective = "rolltop: ensure-column "

func loadManifestMigrations(manifest *Manifest) error {
	if manifest == nil || strings.TrimSpace(manifest.Dir) == "" {
		return nil
	}
	for _, scope := range []string{ScopeSystem, ScopeUser} {
		dir := filepath.Join(manifest.Dir, "migrations", scope)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			statements, columns, err := parseMigrationSQL(string(raw))
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			id := strings.TrimSuffix(entry.Name(), ".sql")
			if !validManifestID(id) {
				return fmt.Errorf("%s: invalid migration id %q", path, id)
			}
			manifest.Migrations = append(manifest.Migrations, Migration{
				Scope:         scope,
				PluginID:      manifest.ID,
				ID:            id,
				EnsureColumns: columns,
				Statements:    statements,
			})
		}
	}
	return nil
}

func parseMigrationSQL(raw string) ([]string, []MigrationColumn, error) {
	var statements []string
	var columns []MigrationColumn
	for _, chunk := range strings.Split(raw, ";") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		statement, column, hasColumn, err := parseMigrationStatement(chunk)
		if err != nil {
			return nil, nil, err
		}
		if hasColumn {
			columns = append(columns, column)
			continue
		}
		if statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements, columns, nil
}

func parseMigrationStatement(chunk string) (string, MigrationColumn, bool, error) {
	var directive string
	lines := strings.Split(chunk, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-- "+ensureColumnDirective) {
			if directive != "" {
				return "", MigrationColumn{}, false, fmt.Errorf("multiple ensure-column directives on one statement")
			}
			directive = strings.TrimSpace(strings.TrimPrefix(trimmed, "-- "+ensureColumnDirective))
			continue
		}
		cleaned = append(cleaned, line)
	}
	statement := strings.TrimSpace(strings.Join(cleaned, "\n"))
	if directive == "" {
		return statement, MigrationColumn{}, false, nil
	}
	parts := strings.Fields(directive)
	if len(parts) != 2 {
		return "", MigrationColumn{}, false, fmt.Errorf("invalid ensure-column directive %q", directive)
	}
	if statement == "" {
		return "", MigrationColumn{}, false, fmt.Errorf("ensure-column directive for %s.%s has no DDL", parts[0], parts[1])
	}
	return "", MigrationColumn{Table: parts[0], Column: parts[1], DDL: statement}, true, nil
}
