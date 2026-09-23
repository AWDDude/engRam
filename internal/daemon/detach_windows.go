//go:build windows

package daemon

import "syscall"

// createNewProcessGroup is the Windows equivalent of setsid for our purpose:
// it detaches the child from the parent's console control events, so a Ctrl-C
// in the spawning client's console does not reach the daemon.
const createNewProcessGroup = 0x00000200

func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
