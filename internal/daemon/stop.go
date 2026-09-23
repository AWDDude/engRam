package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/AWDDude/engRam/internal/config"
)

// stopTimeout bounds the wait for a signalled daemon to let go of its socket.
// Shutdown only has to close a listener and a bolt file, so this is generous.
const stopTimeout = 30 * time.Second

// Status describes whether a daemon is serving cfg's database.
type Status struct {
	Running bool
	PID     int
	Version string
	Socket  string
}

// Query reports on the daemon for cfg's database. A daemon that is up answers
// with its version, which is also how a stale socket file is told apart from a
// live one.
func Query(cfg config.Config) Status {
	p := PathsFor(cfg)
	st := Status{Socket: p.Socket}
	conn, err := connect(p.Socket)
	if err != nil {
		return st
	}
	defer conn.Close()
	st.Running = true
	st.Version = conn.Version
	if pid, err := readPID(p.PID); err == nil {
		st.PID = pid
	}
	return st
}

// StopIfRunning shuts down the daemon owning cfg's database, so a CLI command
// can open the file directly. Doing nothing when none is running makes this
// safe to call unconditionally.
//
// Taking the database outright is only reasonable because starting a daemon is
// automatic: the next tool call brings a fresh one up. That is what lets
// export, import and reembed stay simple direct-to-bolt commands instead of
// needing a quiesce protocol or a set of MCP tools that would also expose
// reembed to the model.
func StopIfRunning(cfg config.Config) error {
	p := PathsFor(cfg)
	if !running(p.Socket) {
		return nil
	}
	return stop(p, stopTimeout)
}

// Stop shuts down the daemon owning cfg's database, reporting whether one was
// running. Backs `engram daemon stop`.
func Stop(cfg config.Config) (bool, error) {
	p := PathsFor(cfg)
	if !running(p.Socket) {
		return false, nil
	}
	return true, stop(p, stopTimeout)
}

// stop signals the daemon and waits until nothing answers on the socket.
//
// It waits on the socket refusing connections rather than on the pid file or
// the process table: that is the condition every caller actually needs, and it
// is also true when the daemon dies without unlinking anything.
func stop(p Paths, timeout time.Duration) error {
	pid, err := readPID(p.PID)
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding daemon process %d: %w", pid, err)
	}
	if err := terminate(proc); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signalling daemon %d: %w", pid, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("unix", p.Socket, connectTimeout)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon %d did not shut down within %s", pid, timeout)
		}
		time.Sleep(pollInterval)
	}

	// Nothing is listening. Clear the socket file in case the daemon was
	// killed before it could, so the next bind is clean.
	if err := os.Remove(p.Socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing socket %s: %w", p.Socket, err)
	}

	// A closed listener is not proof the process is gone: it still has to
	// release the ownership lock on its way out. Returning before that lets
	// the caller start a replacement that finds the lock still held and exits
	// on the spot, leaving nothing to connect to.
	return waitForLockFree(p, time.Until(deadline))
}

// waitForLockFree blocks until the daemon's ownership lock can be taken, which
// is the point at which the previous daemon has fully exited and a replacement
// can start.
func waitForLockFree(p Paths, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		lock, held, err := takeLock(p.Lock)
		if err != nil {
			return err
		}
		if held {
			return lock.Unlock()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the daemon still holds %s after %s", p.Lock, timeout)
		}
		time.Sleep(pollInterval)
	}
}
