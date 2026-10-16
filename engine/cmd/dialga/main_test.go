package main

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestMain keeps the tests off the real system keyring.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}
