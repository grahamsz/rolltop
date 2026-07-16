package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var errRolltopAlreadyRunning = errors.New("another rolltop process is using this data directory")

type instanceLock struct {
	file *os.File
}

// acquireInstanceLock prevents online maintenance from racing the HTTP server.
// flock is associated with the open file description, so it also works when
// the data directory is a Docker volume shared by separate containers.
func acquireInstanceLock(dataDir string) (*instanceLock, error) {
	dataDir = filepath.Clean(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory for instance lock: %w", err)
	}
	path := filepath.Join(dataDir, ".rolltop-instance.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w; stop the server before running offline maintenance", errRolltopAlreadyRunning)
		}
		return nil, fmt.Errorf("lock rolltop data directory %s: %w", dataDir, err)
	}
	if err := file.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
		_ = file.Sync()
	}
	return &instanceLock{file: file}, nil
}

func (l *instanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
