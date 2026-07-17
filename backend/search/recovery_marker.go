package search

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const searchIndexRecoveryMarker = "bleve.recovery-required"

// MarkSearchIndexRecoveryRequired durably records that a tenant index must be
// quarantined on the next process start. The marker is a sibling of the live
// Bleve directory, so moving that directory cannot accidentally consume it.
func (s *Service) MarkSearchIndexRecoveryRequired(userID int64) error {
	markerPath, userDir, err := s.searchIndexRecoveryMarkerPath(userID, true)
	if err != nil {
		return err
	}
	return writeSearchIndexRecoveryMarker(markerPath, userDir, syncDirectory)
}

func writeSearchIndexRecoveryMarker(markerPath, userDir string, syncDir func(string) error) error {
	if info, err := os.Lstat(markerPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("search recovery marker is not a regular file")
		}
		// A prior publish may have succeeded while its directory sync failed.
		// Re-sync an existing marker before treating it as durable.
		if err := syncDir(userDir); err != nil {
			return fmt.Errorf("sync existing search recovery marker directory: %w", err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect search recovery marker: %w", err)
	}

	temporary, err := os.CreateTemp(userDir, ".bleve.recovery-required-*")
	if err != nil {
		return fmt.Errorf("create search recovery marker: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure search recovery marker: %w", err)
	}
	if _, err := temporary.WriteString("rolltop-search-recovery-v1\n"); err != nil {
		return fmt.Errorf("write search recovery marker: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync search recovery marker: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close search recovery marker: %w", err)
	}
	if err := os.Rename(temporaryPath, markerPath); err != nil {
		return fmt.Errorf("publish search recovery marker: %w", err)
	}
	keepTemporary = false
	if err := syncDir(userDir); err != nil {
		return fmt.Errorf("sync search recovery directory: %w", err)
	}
	return nil
}

// SearchIndexRecoveryRequired reports whether startup must quarantine and
// rebuild this tenant's index before any Bleve handle is opened.
func (s *Service) SearchIndexRecoveryRequired(userID int64) (bool, error) {
	markerPath, _, err := s.searchIndexRecoveryMarkerPath(userID, false)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(markerPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect search recovery marker: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("search recovery marker is not a regular file")
	}
	return true, nil
}

// ClearSearchIndexRecoveryRequired acknowledges successful offline quarantine.
// Callers must first reset SQLite index-completion state and durably move the old
// index. A failed marker-removal sync restores the marker for a later retry when
// possible and always returns an error.
func (s *Service) ClearSearchIndexRecoveryRequired(userID int64) error {
	return s.clearSearchIndexRecoveryRequiredWithSync(userID, syncDirectory)
}

func (s *Service) clearSearchIndexRecoveryRequiredWithSync(userID int64, syncDir func(string) error) error {
	markerPath, userDir, err := s.searchIndexRecoveryMarkerPath(userID, false)
	if err != nil {
		return err
	}
	info, err := os.Lstat(markerPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect search recovery marker: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("search recovery marker is not a regular file")
	}
	if err := os.Remove(markerPath); err != nil {
		return fmt.Errorf("clear search recovery marker: %w", err)
	}
	if err := syncDir(userDir); err != nil {
		clearErr := fmt.Errorf("sync cleared search recovery marker: %w", err)
		// The caller will fail startup, so restore a durable marker for the next
		// attempt whenever possible. The index rename was synced before this
		// method was called, making either crash outcome safe even if restoration
		// also fails.
		if restoreErr := writeSearchIndexRecoveryMarker(markerPath, userDir, syncDir); restoreErr != nil {
			return errors.Join(clearErr, fmt.Errorf("restore search recovery marker after clear failure: %w", restoreErr))
		}
		return fmt.Errorf("%w; marker restored for retry", clearErr)
	}
	return nil
}

func (s *Service) searchIndexRecoveryMarkerPath(userID int64, createUserDir bool) (string, string, error) {
	if s == nil || !s.perUser || s.root == "" {
		return "", "", fmt.Errorf("search recovery markers require a per-user index service")
	}
	if userID <= 0 {
		return "", "", fmt.Errorf("user id must be positive")
	}
	root, err := filepath.Abs(filepath.Clean(s.root))
	if err != nil {
		return "", "", fmt.Errorf("resolve per-user index root: %w", err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", "", fmt.Errorf("inspect per-user index root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("per-user index root is not a regular directory: %s", root)
	}
	userDir := filepath.Join(root, strconv.FormatInt(userID, 10))
	relative, err := filepath.Rel(root, userDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("user index path is outside the configured root")
	}
	if createUserDir {
		if err := os.Mkdir(userDir, 0o700); err != nil && !os.IsExist(err) {
			return "", "", fmt.Errorf("create user search directory: %w", err)
		}
	}
	userInfo, err := os.Lstat(userDir)
	if os.IsNotExist(err) && !createUserDir {
		return filepath.Join(userDir, searchIndexRecoveryMarker), userDir, nil
	}
	if err != nil {
		return "", "", fmt.Errorf("inspect user %d data directory: %w", userID, err)
	}
	if !userInfo.IsDir() || userInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("user %d data directory is not a regular directory", userID)
	}
	return filepath.Join(userDir, searchIndexRecoveryMarker), userDir, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
