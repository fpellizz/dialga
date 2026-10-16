//go:build unix

package main

import (
	"context"
	"os"
	"sync"
	"syscall"
	"testing"

	"github.com/decisyon/dialga/engine/internal/jobs"
)

// Ctrl-C leaves the CLI job cancelled in the journal, as it was in memory.
func TestCtrlCLeavesTheJobCancelledInTheJournal(t *testing.T) {
	read := journalOf(t)
	a, _, s3 := jobApp(t, testConfig)
	started := make(chan struct{})
	var once sync.Once
	s3.OnUploadPart = func(ctx context.Context, _ int32, _ int) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}
	go func() {
		<-started
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	code, _, _ := run(a, "import", localFile(t, "sales.bak", "x"), "--apply", "--interval", "1ms")
	list := read()
	if code != exitFailure || len(list) != 1 || list[0].State != jobs.Cancelled || list[0].Origin != jobs.OriginCLI {
		t.Fatalf("code %d, journal %+v", code, list)
	}
}
