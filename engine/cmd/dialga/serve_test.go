package main

import (
	"bufio"
	"strings"
	"testing"
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
