package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestMain keeps the tests off the real system keyring and off the real job
// journal: the runs that apply something record their job there.
func TestMain(m *testing.M) {
	keyring.MockInit()
	state, err := os.MkdirTemp("", "dialga-state-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_STATE_HOME", state)
	code := m.Run()
	_ = os.RemoveAll(state)
	os.Exit(code)
}
