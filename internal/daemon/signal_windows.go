//go:build windows

package daemon

import "os"

// terminate kills the daemon: Windows offers no way to deliver SIGTERM to
// another process, so there is no clean-shutdown option here. The socket and
// pid file it leaves behind are handled as stale files by the next listen and
// dial, which is the same path a crash takes.
func terminate(p *os.Process) error {
	return p.Kill()
}
