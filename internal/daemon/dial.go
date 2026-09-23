package daemon

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/gofrs/flock"

	"github.com/AWDDude/engRam/internal/config"
)

const (
	// spawnTimeout bounds the wait for a freshly spawned daemon to answer. It
	// is generous because a first run downloads the ~130MB embedding model
	// before it can serve anything, and that is exactly the case where giving
	// up early would look like a broken install.
	spawnTimeout = 3 * time.Minute

	// pollInterval is how often the client retries while a daemon starts.
	pollInterval = 100 * time.Millisecond
)

// Dial returns a connection to the daemon for cfg's database, starting one if
// none is running. The returned Conn carries the MCP stream.
func Dial(cfg config.Config, version string) (*Conn, error) {
	p := PathsFor(cfg)
	if err := os.MkdirAll(cfg.DB.Path, 0o700); err != nil {
		return nil, fmt.Errorf("creating db dir: %w", err)
	}

	// Two passes at most: the second exists only to reconnect after retiring a
	// daemon running a different build.
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := dialOrSpawn(p)
		if err != nil {
			return nil, err
		}
		if conn.Version == version {
			return conn, nil
		}
		conn.Close()
		fmt.Fprintf(os.Stderr, "engram: replacing daemon %s with %s\n", conn.Version, version)
		if err := stop(p, stopTimeout); err != nil {
			return nil, fmt.Errorf("retiring daemon %s: %w", conn.Version, err)
		}
	}
	return nil, fmt.Errorf("daemon keeps reporting a version other than %s", version)
}

// dialOrSpawn connects to a running daemon, or starts one and waits for it.
func dialOrSpawn(p Paths) (*Conn, error) {
	if conn, err := connect(p.Socket); err == nil {
		return conn, nil
	}
	if err := spawn(p); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(spawnTimeout)
	for {
		conn, err := connect(p.Socket)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, startupError(p, err)
		}
		time.Sleep(pollInterval)
	}
}

// spawn starts a daemon if this process wins the spawn lock.
//
// Losing the race is not an error: whoever holds the lock is starting one, and
// the caller polls for it either way. This lock only keeps a burst of sessions
// from starting a pile of daemons at once — the guarantee that one survives
// comes from the daemon's own lock, which is a separate file precisely because
// this one is still held while the daemon it started is booting.
func spawn(p Paths) error {
	lock := flock.New(p.SpawnLock)
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("taking spawn lock %s: %w", p.SpawnLock, err)
	}
	if !locked {
		return nil
	}
	defer func() { _ = lock.Unlock() }()

	// We just failed to dial this socket, so a file still sitting there was
	// left by a daemon that died without cleaning up. It refuses connections
	// but would still block bind.
	if err := os.Remove(p.Socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale socket %s: %w", p.Socket, err)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating the engram binary: %w", err)
	}
	logFile, err := os.OpenFile(p.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening daemon log %s: %w", p.Log, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// The environment carries ENGRAM_CONFIG_PATH and the XDG variables, so the
	// daemon must resolve the same config this client did.
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachAttrs()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}
	// Deliberately not waited on: the daemon outlives this client and is
	// reparented when we exit. Its exit status reaches the user through the
	// log file, which startupError surfaces on a failed dial.
	return nil
}

// startupError turns a dial timeout into something diagnosable. A daemon that
// fails to build its store (a changed embedding model, an unreadable config)
// dies before it ever accepts, so the only evidence is in its log.
func startupError(p Paths, cause error) error {
	base := fmt.Errorf("timed out waiting for the engram daemon on %s: %w", p.Socket, cause)
	if tail := logTail(p.Log, 20); tail != "" {
		return fmt.Errorf("%w\n--- last lines of %s ---\n%s", base, p.Log, tail)
	}
	return base
}

// Proxy connects to the daemon and pipes the MCP stream between it and the
// given stdio streams, returning when either end closes. This is what `engram`
// with no arguments does.
func Proxy(cfg config.Config, version string, stdin io.Reader, stdout io.Writer) error {
	conn, err := Dial(cfg, version)
	if err != nil {
		return err
	}
	return pipe(conn, stdin, stdout)
}
