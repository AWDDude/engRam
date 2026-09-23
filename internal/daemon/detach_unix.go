//go:build !windows

package daemon

import "syscall"

// detachAttrs puts the spawned daemon in its own session, so a Ctrl-C or a
// hangup aimed at the terminal the spawning client happened to inherit does
// not also kill the daemon every other session is using.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
