//go:build unix && !linux

package tunnel

import (
	"os"
	"syscall"
)

// sysProcAttr: own process group (no parent-death signal outside Linux).
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
