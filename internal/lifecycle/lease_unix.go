//go:build unix

package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// WriterLease holds the deployment-wide credential writer lock.
type WriterLease struct {
	mu   sync.Mutex
	file *os.File
}

func AcquireWriterLease(path string) (*WriterLease, error) {
	if path == "" {
		return nil, fmt.Errorf("writer lease path is empty")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return nil, fmt.Errorf("create writer lease directory: %w", errMkdir)
	}
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, fmt.Errorf("open writer lease: %w", errOpen)
	}
	if errLock := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); errLock != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire writer lease: %w", errLock)
	}
	return &WriterLease{file: file}, nil
}

func (l *WriterLease) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	errUnlock := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	errClose := l.file.Close()
	l.file = nil
	if errUnlock != nil {
		return fmt.Errorf("release writer lease: %w", errUnlock)
	}
	return errClose
}
