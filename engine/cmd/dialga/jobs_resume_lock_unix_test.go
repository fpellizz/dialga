//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A job another process runs or resumes is not resumed here: its lock is held.
func TestJobsResumeRefusesAJobAnotherProcessHolds(t *testing.T) {
	dir, id := interruptedImport(t)
	f, err := os.OpenFile(filepath.Join(dir, "."+id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	a, _, _ := testApp(t, testConfig)
	resumable(a, nil)
	code, _, errOut := run(a, "jobs", "resume", id, "--dir", dir)
	if code != exitFailure || !strings.Contains(errOut, "in use by another Dialga process") || !strings.Contains(errOut, "Activity page") {
		t.Fatalf("locked: %d %q", code, errOut)
	}
}
