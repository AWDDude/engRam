package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/AWDDude/engRam/internal/config"
)

// DefaultIdleTimeout is how long the daemon stays up with no clients attached
// before exiting. A client holds its connection open for as long as its MCP
// session lives, so in practice this fires only once every session is gone,
// and it is what keeps the embedding model from sitting in RAM forever.
const DefaultIdleTimeout = 10 * time.Minute

// NewMCPFunc builds the MCP server the daemon serves, plus the cleanup to run
// at shutdown. It is injected rather than built here so tests can run a real
// daemon over a mock store without a model on disk.
type NewMCPFunc func() (*mcpserver.MCPServer, func(), error)

// Serve runs the daemon for cfg's database until ctx is cancelled, the process
// is signalled, or idle expires. Returning nil covers the ordinary reasons to
// stop, including losing the startup race to another daemon.
func Serve(ctx context.Context, cfg config.Config, version string, newMCP NewMCPFunc, idle time.Duration) error {
	p := PathsFor(cfg)
	if err := os.MkdirAll(cfg.DB.Path, 0o700); err != nil {
		return fmt.Errorf("creating db dir: %w", err)
	}

	lock, held, err := takeLock(p.Lock)
	if err != nil {
		return err
	}
	if !held {
		// Another daemon already owns this database. When two sessions start
		// at once both may spawn, and exactly one gets here: that is the
		// mechanism that keeps a single owner, so it is a normal exit.
		log.Printf("another engram daemon already owns %s, exiting", cfg.DB.Path)
		return nil
	}
	defer func() { _ = lock.Unlock() }()

	// Bind before building the store. Building it loads the embedding model,
	// and downloads it on a first run, which can take minutes. A client that
	// raced the spawn then waits in the listen backlog instead of failing to
	// dial, so no separate readiness signal is needed.
	listener, err := listen(p.Socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer func() {
		if err := os.Remove(p.Socket); err != nil && !os.IsNotExist(err) {
			log.Printf("removing socket %s: %v", p.Socket, err)
		}
	}()

	if err := writePID(p.PID); err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(p.PID); err != nil && !os.IsNotExist(err) {
			log.Printf("removing pid file %s: %v", p.PID, err)
		}
	}()

	mcpSrv, cleanup, err := newMCP()
	if err != nil {
		return err
	}
	defer cleanup()

	log.Printf("engram daemon %s serving %s (pid %d)", version, p.Socket, os.Getpid())
	return accept(ctx, listener, mcpSrv, version, idle)
}

// listen binds the unix socket, clearing a socket file left behind by a daemon
// that died without unlinking it.
func listen(socket string) (net.Listener, error) {
	listener, err := net.Listen("unix", socket)
	if err != nil {
		// A leftover socket file blocks bind while refusing connections. Only
		// remove it once nothing answers on it: we hold the daemon lock, so a
		// live listener here should be impossible, and unlinking a working one
		// would break every attached session without any error to show for it.
		if running(socket) {
			return nil, fmt.Errorf("another process is already listening on %s", socket)
		}
		if _, statErr := os.Stat(socket); statErr != nil {
			return nil, fmt.Errorf("listening on %s: %w", socket, err)
		}
		if rmErr := os.Remove(socket); rmErr != nil {
			return nil, fmt.Errorf("removing stale socket %s: %w", socket, rmErr)
		}
		listener, err = net.Listen("unix", socket)
		if err != nil {
			return nil, fmt.Errorf("listening on %s: %w", socket, err)
		}
	}
	// The socket is the entire access control story for the daemon: anything
	// that can connect can read and write every memory, so it is owner-only.
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("securing socket %s: %w", socket, err)
	}
	return listener, nil
}

// accept serves every client that connects until shutdown.
func accept(ctx context.Context, listener net.Listener, mcpSrv *mcpserver.MCPServer, version string, idle time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	shutdown := &shutdownState{listener: listener}
	tracker := newConnTracker(idle, func() {
		log.Printf("idle for %s with no clients, shutting down", idle)
		shutdown.begin()
	})
	// Arm the timer now: a daemon that is spawned but never connected to (the
	// client gave up, or died mid-startup) must not linger with the model
	// loaded.
	tracker.arm()

	go func() {
		<-ctx.Done()
		shutdown.begin()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := listener.Accept()
		if err != nil {
			if shutdown.started() {
				break
			}
			wg.Wait()
			return fmt.Errorf("accepting on %s: %w", listener.Addr(), err)
		}
		tracker.add()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer tracker.done()
			defer conn.Close()

			// A session blocks reading from its client, which closing the
			// listener does nothing about. Without this the daemon would hang
			// in the Wait below until every attached session happened to
			// disconnect on its own, and a `daemon stop` would never return.
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-ctx.Done():
					conn.Close()
				case <-done:
				}
			}()

			logSessionEnd(ctx, serveSession(ctx, conn, mcpSrv, version))
		}()
	}
	// Cancel before waiting, so the watchers above close their connections.
	// This covers the idle path too, where nothing cancelled the context.
	cancel()
	wg.Wait()
	return nil
}

// shutdownState makes a deliberate listener close distinguishable from a real
// accept failure, which otherwise look identical.
type shutdownState struct {
	mu       sync.Mutex
	began    bool
	listener net.Listener
}

func (s *shutdownState) begin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.began {
		return
	}
	s.began = true
	s.listener.Close()
}

func (s *shutdownState) started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.began
}

// connTracker counts attached clients and fires onIdle once the last one
// leaves and stays gone for the timeout.
type connTracker struct {
	mu     sync.Mutex
	n      int
	timer  *time.Timer
	idle   time.Duration
	onIdle func()
}

func newConnTracker(idle time.Duration, onIdle func()) *connTracker {
	return &connTracker{idle: idle, onIdle: onIdle}
}

// arm starts the idle countdown if nothing is attached. An idle of zero or
// less disables idle shutdown entirely, which is what tests want.
func (t *connTracker) arm() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.armLocked()
}

func (t *connTracker) armLocked() {
	if t.idle <= 0 || t.n > 0 || t.timer != nil {
		return
	}
	t.timer = time.AfterFunc(t.idle, t.onIdle)
}

func (t *connTracker) add() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n++
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}

func (t *connTracker) done() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n--
	t.armLocked()
}
