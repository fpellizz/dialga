package tunnel

import (
	"os"
	"syscall"
)

// sysProcAttr: own process group, so Ctrl-C in the console reaches the
// engine only.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminate kills the plugin: Windows has no SIGTERM. TerminateSession then
// ends the session on the SSM side.
func terminate(p *os.Process) error { return p.Kill() }
