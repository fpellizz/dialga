package main

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
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
