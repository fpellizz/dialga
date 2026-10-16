//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package jobs

// lockFile does not lock outside the Unix systems yet: v1 ships for Linux.
// Without it a process cannot tell a job that runs in another one from an
// orphan, so it treats every job that has not ended as its own.
// TODO: confirm with the v2.0 Windows build (LockFileEx).
func lockFile(string) (func() error, error) { return func() error { return nil }, nil }
