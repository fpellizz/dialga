package tunnel

import (
	"os"
	"syscall"
)

// sysProcAttr: own process group, and SIGTERM when the engine dies so no
// orphan plugin keeps the session open.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
