package web

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type StorageStats struct {
	DatabasePath  string
	DatabaseBytes int64
	IndexPath     string
	IndexBytes    int64
	BlobPath      string
	BlobBytes     int64
	TotalBytes    int64
	Error         string
}

const storageStatsCacheTTL = 5 * time.Minute

func (s *Server) cachedStorageStats() StorageStats {
	now := time.Now()
	s.storageMu.Lock()
	if !s.storageCachedAt.IsZero() && now.Sub(s.storageCachedAt) < storageStatsCacheTTL {
		stats := s.storageCached
		s.storageMu.Unlock()
		return stats
	}
	s.storageMu.Unlock()

	stats := s.storageStats()

	s.storageMu.Lock()
	s.storageCached = stats
	s.storageCachedAt = now
	s.storageMu.Unlock()
	return stats
}

func (s *Server) storageStats() StorageStats {
	blobRoot := ""
	if s.dataDir != "" {
		blobRoot = filepath.Join(s.dataDir, "blobs")
	}
	if blobRoot == "" && s.blobs != nil {
		blobRoot = filepath.Join(s.blobs.Root, "blobs")
	}
	stats := StorageStats{
		DatabasePath: s.databasePath,
		IndexPath:    s.indexPath,
		BlobPath:     blobRoot,
	}
	var errs []string
	var err error
	stats.DatabaseBytes, err = sqliteFileSetSize(s.databasePath)
	if err != nil {
		errs = append(errs, fmt.Sprintf("SQLite: %v", err))
	}
	stats.IndexBytes, err = pathSize(s.indexPath)
	if err != nil {
		errs = append(errs, fmt.Sprintf("Bleve: %v", err))
	}
	stats.BlobBytes, err = pathSize(blobRoot)
	if err != nil {
		errs = append(errs, fmt.Sprintf("blobs: %v", err))
	}
	stats.TotalBytes = stats.DatabaseBytes + stats.IndexBytes + stats.BlobBytes
	stats.Error = strings.Join(errs, "; ")
	return stats
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
