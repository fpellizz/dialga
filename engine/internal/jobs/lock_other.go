//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package jobs

// lockDir does not lock outside the Unix systems yet: v1 ships for Linux.
// TODO: confirm with the v2.0 Windows build (LockFileEx).
func lockDir(string) (func() error, error) { return func() error { return nil }, nil }
