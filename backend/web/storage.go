// File overview: Per-user storage usage measurement and caching.

package web

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StorageStats is the per-user disk usage summary shown on the settings page.
type StorageStats struct {
	DatabasePath   string
	DatabaseBytes  int64
	IndexPath      string
	IndexBytes     int64
	IndexBreakdown StorageIndexBreakdown
	BlobPath       string
	BlobBytes      int64
	TotalBytes     int64
	Error          string
}

// StorageIndexBreakdown describes the per-user Bleve directory without exposing
// message content or data from another tenant's storage tree.
type StorageIndexBreakdown struct {
	FileCount       int
	ZapCount        int
	ZapBytes        int64
	LargestZapPath  string
	LargestZapBytes int64
	RootBytes       int64
	OtherBytes      int64
}

const storageStatsCacheTTL = 5 * time.Minute

type storageStatsCacheEntry struct {
	Stats    StorageStats
	CachedAt time.Time
}

func (s *Server) cachedStorageStats(userID int64) StorageStats {
	now := time.Now()
	s.storageMu.Lock()
	if entry, ok := s.storageCached[userID]; ok && now.Sub(entry.CachedAt) < storageStatsCacheTTL {
		stats := entry.Stats
		s.storageMu.Unlock()
		return stats
	}
	s.storageMu.Unlock()

	stats := s.storageStatsForUser(userID)

	s.storageMu.Lock()
	if s.storageCached == nil {
		s.storageCached = make(map[int64]storageStatsCacheEntry)
	}
	s.storageCached[userID] = storageStatsCacheEntry{Stats: stats, CachedAt: now}
	s.storageMu.Unlock()
	return stats
}

func (s *Server) storageStatsForUser(userID int64) StorageStats {
	databasePath, indexPath, blobPath := s.userStoragePaths(userID)
	stats := StorageStats{
		DatabasePath: joinedStoragePaths(databasePath),
		IndexPath:    joinedStoragePaths(indexPath),
		BlobPath:     joinedStoragePaths(blobPath),
	}
	var errs []string
	var err error
	stats.DatabaseBytes, err = sqliteFileSetSize(databasePath)
	if err != nil {
		errs = append(errs, fmt.Sprintf("user SQLite: %v", err))
	}
	stats.IndexBytes, stats.IndexBreakdown, err = bleveIndexBreakdown(indexPath)
	if err != nil {
		errs = append(errs, fmt.Sprintf("user Bleve: %v", err))
	}
	stats.BlobBytes, err = pathSize(blobPath)
	if err != nil {
		errs = append(errs, fmt.Sprintf("user blobs: %v", err))
	}
	stats.TotalBytes = stats.DatabaseBytes + stats.IndexBytes + stats.BlobBytes
	stats.Error = strings.Join(errs, "; ")
	return stats
}

func (s *Server) userStoragePaths(userID int64) (databasePath, indexPath, blobPath string) {
	if userID <= 0 {
		return "", "", ""
	}
	id := strconv.FormatInt(userID, 10)
	if strings.TrimSpace(s.dataDir) != "" {
		userDir := filepath.Join(s.dataDir, "users", id)
		return filepath.Join(userDir, "mailmirror.db"),
			filepath.Join(userDir, "bleve"),
			filepath.Join(userDir, "blobs")
	}
	if s.blobs != nil && strings.TrimSpace(s.blobs.Root) != "" {
		blobPath = filepath.Join(s.blobs.Root, "users", id, "blobs")
	}
	return s.databasePath, s.indexPath, blobPath
}

func joinedStoragePaths(paths ...string) string {
	var clean []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" && !strings.Contains(path, string(filepath.Separator)+"*"+string(filepath.Separator)) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				continue
			}
		}
		if path != "" {
			clean = append(clean, path)
		}
	}
	return strings.Join(clean, " + ")
}

func bleveIndexBreakdown(path string) (int64, StorageIndexBreakdown, error) {
	var breakdown StorageIndexBreakdown
	if strings.TrimSpace(path) == "" {
		return 0, breakdown, nil
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, breakdown, nil
	}
	if err != nil {
		return 0, breakdown, err
	}
	if !info.IsDir() {
		breakdown.FileCount = 1
		breakdown.OtherBytes = info.Size()
		return info.Size(), breakdown, nil
	}

	var total int64
	err = filepath.WalkDir(path, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size := info.Size()
		total += size
		breakdown.FileCount++
		switch {
		case filepath.Ext(entry.Name()) == ".zap":
			breakdown.ZapCount++
			breakdown.ZapBytes += size
			if size > breakdown.LargestZapBytes {
				breakdown.LargestZapBytes = size
				breakdown.LargestZapPath = relativeStoragePath(path, filePath)
			}
		case entry.Name() == "root.bolt":
			breakdown.RootBytes += size
		default:
			breakdown.OtherBytes += size
		}
		return nil
	})
	return total, breakdown, err
}

func relativeStoragePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

func sqliteFileSetSize(path string) (int64, error) {
	if strings.TrimSpace(path) == "" {
		return 0, nil
	}
	var total int64
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		n, err := pathSize(p)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func pathSize(path string) (int64, error) {
	if strings.TrimSpace(path) == "" {
		return 0, nil
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
