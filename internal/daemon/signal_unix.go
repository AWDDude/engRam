//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// terminate asks the daemon to shut down cleanly so it can unlink its socket
// and pid file on the way out.
func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
