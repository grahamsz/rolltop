package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"rolltop/backend/config"
	"rolltop/backend/plugins"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

const resetSearchUsage = `Usage:
  rolltop reset-search --user-id ID --confirm-offline

The Rolltop server must be stopped. This command quarantines only the selected
user's Bleve index and marks that user's search-visible messages for reindexing.
It changes only derived search completion state; it does not delete or alter
message content, IMAP state, attachments, or blobs.
`

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("a command is required\n\n%s", resetSearchUsage)
	}
	switch args[0] {
	case "reset-search":
		return runResetSearch(ctx, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		_, _ = io.WriteString(stdout, resetSearchUsage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], resetSearchUsage)
	}
}

func runResetSearch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("reset-search", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = io.WriteString(stderr, resetSearchUsage) }
	userID := flags.Int64("user-id", 0, "numeric local user ID")
	confirmOffline := flags.Bool("confirm-offline", false, "confirm that every Rolltop server using this data volume is stopped")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("reset-search does not accept positional arguments")
	}
	if *userID <= 0 {
		return fmt.Errorf("--user-id must be a positive numeric local user ID")
	}
	if !*confirmOffline {
		return fmt.Errorf("refusing online search reset: stop every Rolltop server using this data volume, then pass --confirm-offline")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	lock, err := acquireInstanceLock(cfg.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	manifests, err := plugins.LoadManifests(cfg.PluginDir)
	if err != nil {
		return err
	}
	db, err := store.OpenServerWithPluginManifests(cfg.DatabasePath, cfg.DataDir, manifests, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.GetUserByID(ctx, *userID); err != nil {
		if store.IsNotFound(err) {
			return fmt.Errorf("local user %d does not exist", *userID)
		}
		return fmt.Errorf("load local user %d: %w", *userID, err)
	}
	indexRoot := filepath.Join(cfg.DataDir, "users")
	if err := search.ValidatePerUserIndexPath(indexRoot, *userID); err != nil {
		return fmt.Errorf("validate local user %d data path: %w", *userID, err)
	}
	// Open and migrate the selected tenant database before touching its index.
	if _, err := db.UserStore(ctx, *userID); err != nil {
		return fmt.Errorf("open local user %d store: %w", *userID, err)
	}

	marked, err := db.MarkSearchVisibleMessagesPendingIndex(ctx, *userID)
	if err != nil {
		return fmt.Errorf("mark user %d messages pending search reindex: %w", *userID, err)
	}
	// Pending flags are deliberately written first. If the process stops before
	// the rename, rebuilding documents in the old index is harmless. The inverse
	// ordering could leave an empty index with every SQLite row marked complete.
	quarantine, err := search.QuarantinePerUserIndex(indexRoot, *userID, time.Now())
	if err != nil {
		return fmt.Errorf("messages were safely marked pending, but the index could not be quarantined: %w", err)
	}

	if quarantine.QuarantinePath == "" {
		fmt.Fprintf(stdout, "User %d had no existing Bleve index at %s.\n", *userID, quarantine.IndexPath)
	} else {
		fmt.Fprintf(stdout, "Quarantined user %d Bleve index:\n  %s\n  -> %s\n", *userID, quarantine.IndexPath, quarantine.QuarantinePath)
	}
	fmt.Fprintf(stdout, "Marked %d search-visible messages pending reindex. Message content, blobs, and IMAP were not changed.\n", marked)
	fmt.Fprintln(stdout, "Start Rolltop normally; startup will queue this user's local search rebuild.")
	if quarantine.QuarantinePath != "" {
		fmt.Fprintf(stdout, "To restore before starting Rolltop, rename %s back to %s.\n",
			quotePath(quarantine.QuarantinePath), quotePath(quarantine.IndexPath))
	}
	return nil
}

func quotePath(path string) string {
	if strings.IndexFunc(path, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\'' || r == '"' || r == '\\'
	}) < 0 {
		return path
	}
	return fmt.Sprintf("%q", path)
}
