//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package jobs

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive flock on path (created if needed): the lock of
// one job. The kernel drops it if the process dies. ErrLocked if another
// process holds it.
func lockFile(path string) (func() error, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
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
