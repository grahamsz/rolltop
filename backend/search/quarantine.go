package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// IndexQuarantine is the reversible result of moving one tenant's Bleve index
// out of the live path. An empty QuarantinePath means no index existed.
type IndexQuarantine struct {
	IndexPath      string
	QuarantinePath string
}

// QuarantinePerUserIndex atomically moves one tenant's index to a timestamped
// sibling. The caller must ensure the Rolltop server is offline before calling.
func QuarantinePerUserIndex(root string, userID int64, now time.Time) (IndexQuarantine, error) {
	result, exists, err := inspectPerUserIndex(root, userID)
	if err != nil {
		return IndexQuarantine{}, err
	}
	if !exists {
		return result, nil
	}
	indexPath := result.IndexPath

	stamp := now.UTC().Format("20060102T150405.000000000Z")
	quarantinePath := indexPath + ".quarantine-" + stamp
	if _, err := os.Lstat(quarantinePath); err == nil {
		return IndexQuarantine{}, fmt.Errorf("search index quarantine already exists: %s", quarantinePath)
	} else if !errorsIsNotExist(err) {
		return IndexQuarantine{}, fmt.Errorf("inspect search index quarantine: %w", err)
	}
	if err := os.Rename(indexPath, quarantinePath); err != nil {
		return IndexQuarantine{}, fmt.Errorf("quarantine user %d search index: %w", userID, err)
	}
	result.QuarantinePath = quarantinePath
	return result, nil
}

// ValidatePerUserIndexPath performs the same tenant-boundary and symlink checks
// as quarantine without changing the filesystem. Offline commands call this
// before opening a tenant SQLite database so an invalid user directory cannot
// redirect migrations or pending-index writes outside the configured root.
func ValidatePerUserIndexPath(root string, userID int64) error {
	_, _, err := inspectPerUserIndex(root, userID)
	return err
}

func inspectPerUserIndex(root string, userID int64) (IndexQuarantine, bool, error) {
	if userID <= 0 {
		return IndexQuarantine{}, false, fmt.Errorf("user id must be positive")
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return IndexQuarantine{}, false, fmt.Errorf("resolve per-user index root: %w", err)
	}
	userDir := filepath.Join(root, strconv.FormatInt(userID, 10))
	indexPath := filepath.Join(userDir, "bleve")
	result := IndexQuarantine{IndexPath: indexPath}
	rootInfo, err := os.Lstat(root)
	if errorsIsNotExist(err) {
		return result, false, nil
	}
	if err != nil {
		return IndexQuarantine{}, false, fmt.Errorf("inspect per-user index root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return IndexQuarantine{}, false, fmt.Errorf("per-user index root is not a regular directory: %s", root)
	}
	rel, err := filepath.Rel(root, indexPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return IndexQuarantine{}, false, fmt.Errorf("user index path is outside the configured root")
	}
	userInfo, err := os.Lstat(userDir)
	if errorsIsNotExist(err) {
		return result, false, nil
	}
	if err != nil {
		return IndexQuarantine{}, false, fmt.Errorf("inspect user %d data directory: %w", userID, err)
	}
	if !userInfo.IsDir() || userInfo.Mode()&os.ModeSymlink != 0 {
		return IndexQuarantine{}, false, fmt.Errorf("user %d data directory is not a regular directory", userID)
	}
	info, err := os.Lstat(indexPath)
	if errorsIsNotExist(err) {
		return result, false, nil
	}
	if err != nil {
		return IndexQuarantine{}, false, fmt.Errorf("inspect user %d search index: %w", userID, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return IndexQuarantine{}, false, fmt.Errorf("user %d search index is not a regular directory", userID)
	}
	return result, true, nil
}

// RestoreQuarantinedIndex supports an operator-requested rollback before the
// server creates a replacement index. It never overwrites a live path.
func RestoreQuarantinedIndex(quarantine IndexQuarantine) error {
	if quarantine.QuarantinePath == "" {
		return nil
	}
	if _, err := os.Lstat(quarantine.IndexPath); err == nil {
		return fmt.Errorf("live search index already exists: %s", quarantine.IndexPath)
	} else if !errorsIsNotExist(err) {
		return fmt.Errorf("inspect live search index before restore: %w", err)
	}
	if err := os.Rename(quarantine.QuarantinePath, quarantine.IndexPath); err != nil {
		return fmt.Errorf("restore quarantined search index: %w", err)
	}
	return nil
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
