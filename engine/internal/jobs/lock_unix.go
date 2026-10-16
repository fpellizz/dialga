//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDir takes an exclusive flock on dir/.lock: one engine per journal, or
// a second app would mark the running jobs of the first as interrupted. The
// kernel drops the lock if the engine dies.
func lockDir(dir string) (func() error, error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("job journal: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("job journal: %w", err)
	}
	return f.Close, nil
}
