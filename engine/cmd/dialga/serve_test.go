package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
)

func TestReadToken(t *testing.T) {
	long := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"token and newline", long + "\n", long, false},
		{"token without newline", long, long, false},
		{"crlf", long + "\r\n", long, false},
		{"empty", "", "", true},
		{"too short", "abc\n", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readToken(bufio.NewReader(strings.NewReader(tc.input)))
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("readToken() = %q, %v; want %q, wantErr %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestServeKeepsTheJobJournal(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	code := make(chan int, 1)
	go func() {
		code <- serve(stdinR, stdoutW, io.Discard, filepath.Join(t.TempDir(), "config.yaml"))
		_ = stdoutW.Close()
	}()
	if _, err := io.WriteString(stdinW, strings.Repeat("t", 40)+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdoutR).ReadString('\n')
	if err != nil || !strings.Contains(line, `"event":"ready"`) {
		t.Fatalf("ready line %q, %v", line, err)
	}
	dir := filepath.Join(state, "dialga", "jobs")
	// While the engine runs, it owns the journal.
	if _, err := jobs.Open(dir, jobs.Options{}); !errors.Is(err, jobs.ErrLocked) {
		t.Fatalf("journal while serving: %v", err)
	}
	_ = stdinW.Close() // the shell is gone
	select {
	case c := <-code:
		if c != 0 {
			t.Fatalf("exit %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop when stdin closed")
	}
	m, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatalf("journal after serve: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWaitShell(t *testing.T) {
	r, w := io.Pipe()
	done := make(chan struct{})
	go func() {
		waitShell(r)
		close(done)
	}()
	if _, err := io.WriteString(w, "noise\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("stopped on a line that is not shutdown")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := io.WriteString(w, "shutdown\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop on the shutdown line")
	}
	_ = w.Close()

	waitShell(strings.NewReader("noise\n")) // EOF: returns
}
