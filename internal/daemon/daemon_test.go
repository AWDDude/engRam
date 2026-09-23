package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/mcptest"
)

// These tests run the daemon inside the test process. That is deliberate — it
// exercises the real listener, accept loop and MCP wiring without a model on
// disk — but it means the pid file names the test binary, so nothing here may
// call Stop or StopIfRunning: they would signal the test run itself. Shutdown
// is driven through the context instead, and the signal path is covered
// end-to-end in cmd/engram's integration test.

// testConfig points at a short-pathed temp directory so the daemon's files
// land beside the database, the way they do in a real install. t.TempDir() is
// deep enough on macOS to push the socket onto its length fallback, which
// would quietly test a different layout than the one users get.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		DB:              config.DBConfig{Path: shortTempDir(t)},
		DefaultLimit:    20,
		MaxContentChars: 1024,
	}
}

// echoMCP builds an MCP server with one tool that reports which daemon
// answered, so a test can tell two daemons apart.
func echoMCP(name string) (NewMCPFunc, *int) {
	cleanups := 0
	return func() (*mcpserver.MCPServer, func(), error) {
		s := mcpserver.NewMCPServer("engram-test", "1.0.0", mcpserver.WithToolCapabilities(false))
		s.AddTool(
			mcp.NewTool("echo",
				mcp.WithDescription("echo the given text"),
				mcp.WithString("text", mcp.Required(), mcp.Description("text to echo")),
			),
			func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var args struct {
					Text string `json:"text"`
				}
				if err := req.BindArguments(&args); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(name + ":" + args.Text), nil
			},
		)
		return s, func() { cleanups++ }, nil
	}, &cleanups
}

// startDaemon runs Serve in the background and waits for it to accept
// connections. The returned stop func cancels it and reports what Serve
// returned.
func startDaemon(t *testing.T, cfg config.Config, version string, newMCP NewMCPFunc, idle time.Duration) (Paths, func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(ctx, cfg, version, newMCP, idle) }()

	p := PathsFor(cfg)
	waitFor(t, 10*time.Second, func() bool { return running(p.Socket) })

	stopped := false
	return p, func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-errCh:
			return err
		case <-time.After(10 * time.Second):
			return fmt.Errorf("daemon did not shut down")
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// dialClient connects to the daemon and completes the MCP handshake over the
// socket, the way the real client's byte pipe carries it.
func dialClient(t *testing.T, socket string) (*mcptest.Client, *Conn) {
	t.Helper()
	conn, err := connect(socket)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	client, err := mcptest.New(conn, conn)
	if err != nil {
		conn.Close()
		t.Fatalf("mcp handshake: %v", err)
	}
	return client, conn
}

func TestPathsFor_LivesBesideTheDatabase(t *testing.T) {
	// Not testConfig: t.TempDir() on macOS already sits ~95 bytes deep under
	// /var/folders, which is close enough to the socket address limit that
	// this would exercise the fallback instead of the normal layout.
	dir := shortTempDir(t)
	cfg := config.Config{DB: config.DBConfig{Path: dir}}
	p := PathsFor(cfg)

	// One daemon per database directory is what lets a second config with its
	// own db.path run its own daemon instead of contending for one socket.
	for name, got := range map[string]string{
		"socket": p.Socket,
		"lock":   p.Lock,
		"pid":    p.PID,
		"log":    p.Log,
	} {
		if filepath.Dir(got) != cfg.DB.Path {
			t.Errorf("%s path %q is not in the database directory %q", name, got, cfg.DB.Path)
		}
	}
}

// shortTempDir makes a temp directory with a short path, removed at the end of
// the test. The daemon's socket has a hard address-length limit, so tests that
// care which side of it they are on cannot use t.TempDir().
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "eg")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestPathsFor_FallsBackWhenTheSocketPathIsTooLong(t *testing.T) {
	// A sockaddr_un path is a fixed-size field (104 bytes on macOS), and
	// overflowing it fails at bind with nothing that mentions length.
	deep := filepath.Join(t.TempDir(), strings.Repeat("a-fairly-long-directory-name/", 10))
	cfg := config.Config{DB: config.DBConfig{Path: deep}}

	p := PathsFor(cfg)
	if len(p.Socket) > maxSocketPath {
		t.Errorf("fallback socket path is still %d bytes: %q", len(p.Socket), p.Socket)
	}
	if filepath.Dir(p.Socket) == deep {
		t.Errorf("expected the fallback to leave the database directory, got %q", p.Socket)
	}
	// The other files have no length limit and stay put, so a long path does
	// not scatter a daemon's state across two directories more than it must.
	if filepath.Dir(p.Lock) != deep {
		t.Errorf("lock should stay beside the database, got %q", p.Lock)
	}

	// Two different databases must not collide on one fallback socket.
	other := config.Config{DB: config.DBConfig{Path: deep + "other/"}}
	if PathsFor(other).Socket == p.Socket {
		t.Error("two database directories produced the same fallback socket")
	}
}

func TestServe_CarriesMCPToConcurrentClients(t *testing.T) {
	cfg := testConfig(t)
	newMCP, _ := echoMCP("daemon")
	p, stop := startDaemon(t, cfg, "1.0.0", newMCP, 0)
	defer func() {
		if err := stop(); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	// The point of the whole change: several sessions attached at once, each
	// with its own MCP session state, all served by one process.
	const clients = 4
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client, conn := dialClient(t, p.Socket)
			defer conn.Close()

			names, err := client.ToolNames()
			if err != nil {
				t.Errorf("client %d tools/list: %v", i, err)
				return
			}
			if len(names) != 1 || names[0] != "echo" {
				t.Errorf("client %d saw tools %v, want [echo]", i, names)
			}
			want := fmt.Sprintf("daemon:hello-%d", i)
			got, err := client.CallTool("echo", map[string]any{"text": fmt.Sprintf("hello-%d", i)})
			if err != nil {
				t.Errorf("client %d echo: %v", i, err)
				return
			}
			if got != want {
				t.Errorf("client %d got %q, want %q", i, got, want)
			}
		}(i)
	}
	wg.Wait()
}

func TestServe_ReportsItsVersionInThePreamble(t *testing.T) {
	cfg := testConfig(t)
	newMCP, _ := echoMCP("daemon")
	p, stop := startDaemon(t, cfg, "2.3.0", newMCP, 0)
	defer stop()

	conn, err := connect(p.Socket)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	// This is what tells a client upgraded underneath a running daemon that it
	// is talking to the old build.
	if conn.Version != "2.3.0" {
		t.Errorf("preamble version = %q, want %q", conn.Version, "2.3.0")
	}
}

func TestServe_SecondDaemonYieldsToTheOneThatOwnsTheDatabase(t *testing.T) {
	cfg := testConfig(t)
	first, _ := echoMCP("first")
	p, stop := startDaemon(t, cfg, "1.0.0", first, 0)
	defer stop()

	// Two clients racing to spawn both start a daemon; the lock is what makes
	// exactly one of them the owner. The loser must exit quietly rather than
	// fail, and must not have built a store or touched the socket.
	second, secondBuilt := echoMCP("second")
	if err := Serve(context.Background(), cfg, "1.0.0", second, 0); err != nil {
		t.Fatalf("second daemon should exit cleanly, got %v", err)
	}
	if *secondBuilt != 0 {
		t.Error("the losing daemon built a store instead of exiting first")
	}

	client, conn := dialClient(t, p.Socket)
	defer conn.Close()
	got, err := client.CallTool("echo", map[string]any{"text": "x"})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if got != "first:x" {
		t.Errorf("got %q, want the original daemon to still be serving", got)
	}
}

func TestServe_ReplacesASocketLeftByACrashedDaemon(t *testing.T) {
	cfg := testConfig(t)
	p := PathsFor(cfg)

	// A unix socket file outlives the process that bound it. It refuses
	// connections but still blocks bind, so a daemon that was killed would
	// otherwise leave the database unusable until someone deleted the file.
	if err := os.WriteFile(p.Socket, nil, 0o600); err != nil {
		t.Fatalf("planting a stale socket: %v", err)
	}

	newMCP, _ := echoMCP("daemon")
	_, stop := startDaemon(t, cfg, "1.0.0", newMCP, 0)
	defer stop()

	client, conn := dialClient(t, p.Socket)
	defer conn.Close()
	if _, err := client.CallTool("echo", map[string]any{"text": "x"}); err != nil {
		t.Errorf("echo after replacing a stale socket: %v", err)
	}
}

func TestListen_RefusesToStealALiveSocket(t *testing.T) {
	cfg := testConfig(t)
	p := PathsFor(cfg)

	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Unlinking a socket something is still serving would break every attached
	// session silently, so the stale-file path must check first.
	if _, err := listen(p.Socket); err == nil {
		t.Error("expected listen to refuse a socket that is being served")
	}
}

func TestServe_ExitsAfterSittingIdle(t *testing.T) {
	cfg := testConfig(t)
	newMCP, cleanups := echoMCP("daemon")

	// A daemon spawned by a client that then gave up must not sit on the model
	// forever.
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), cfg, "1.0.0", newMCP, 200*time.Millisecond) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("idle shutdown returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not shut down when idle")
	}
	if *cleanups != 1 {
		t.Errorf("store cleanup ran %d times, want 1", *cleanups)
	}

	p := PathsFor(cfg)
	for _, path := range []string{p.Socket, p.PID} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived shutdown", path)
		}
	}
}

func TestServe_StaysUpWhileAClientIsAttached(t *testing.T) {
	cfg := testConfig(t)
	newMCP, _ := echoMCP("daemon")
	idle := 200 * time.Millisecond
	p, stop := startDaemon(t, cfg, "1.0.0", newMCP, idle)
	defer stop()

	client, conn := dialClient(t, p.Socket)
	defer conn.Close()

	// An idle session still holds its connection, which is what must keep the
	// timer from firing: a long pause between tool calls is normal.
	time.Sleep(4 * idle)

	if _, err := client.CallTool("echo", map[string]any{"text": "still here"}); err != nil {
		t.Fatalf("echo after idling: %v", err)
	}
}

func TestQuery_ReportsTheRunningDaemon(t *testing.T) {
	cfg := testConfig(t)

	if st := Query(cfg); st.Running {
		t.Error("reported a daemon before one was started")
	}

	newMCP, _ := echoMCP("daemon")
	_, stop := startDaemon(t, cfg, "4.5.6", newMCP, 0)

	st := Query(cfg)
	if !st.Running {
		t.Fatal("did not report the running daemon")
	}
	if st.Version != "4.5.6" {
		t.Errorf("version = %q, want %q", st.Version, "4.5.6")
	}
	if st.PID != os.Getpid() {
		t.Errorf("pid = %d, want this process %d", st.PID, os.Getpid())
	}

	if err := stop(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if st := Query(cfg); st.Running {
		t.Error("still reporting a daemon after shutdown")
	}
}

func TestStopIfRunning_DoesNothingWithoutADaemon(t *testing.T) {
	// Every CLI command calls this unconditionally, so the no-daemon case has
	// to be a silent success rather than an error about a missing pid file.
	if err := StopIfRunning(testConfig(t)); err != nil {
		t.Errorf("StopIfRunning with no daemon: %v", err)
	}
}

func TestSpawnLock_OnlyOneCallerSpawns(t *testing.T) {
	cfg := testConfig(t)
	p := PathsFor(cfg)

	// Hold the spawn lock the way a client starting a daemon does, then
	// confirm a second client declines rather than starting another.
	lock, held, err := takeLock(p.SpawnLock)
	if err != nil || !held {
		t.Fatalf("takeLock: held=%v err=%v", held, err)
	}
	defer lock.Unlock()

	if err := spawn(p); err != nil {
		t.Errorf("spawn should yield quietly when the lock is held, got %v", err)
	}
	if _, err := os.Stat(p.Log); !os.IsNotExist(err) {
		t.Error("spawn started a daemon despite losing the lock")
	}
}

func TestSpawnLock_IsNotTheDaemonsOwnershipLock(t *testing.T) {
	cfg := testConfig(t)
	p := PathsFor(cfg)

	// The client holds the spawn lock while the daemon it started is booting.
	// If they were one file, that daemon would find the lock held by its own
	// parent and exit immediately, leaving the client polling a socket nobody
	// will ever bind.
	if p.SpawnLock == p.Lock {
		t.Fatal("the spawn lock and the daemon's ownership lock are the same file")
	}

	spawnLock, held, err := takeLock(p.SpawnLock)
	if err != nil || !held {
		t.Fatalf("taking the spawn lock: held=%v err=%v", held, err)
	}
	defer spawnLock.Unlock()

	daemonLock, held, err := takeLock(p.Lock)
	if err != nil {
		t.Fatalf("taking the daemon lock: %v", err)
	}
	if !held {
		t.Fatal("a daemon could not take its ownership lock while a client held the spawn lock")
	}
	if err := daemonLock.Unlock(); err != nil {
		t.Errorf("releasing the daemon lock: %v", err)
	}
}

func TestEnsureSocketDir_RefusesADirectoryOthersCanWrite(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("a-fairly-long-directory-name/", 10))
	p := PathsFor(config.Config{DB: config.DBConfig{Path: deep}})
	if p.PrivateSocketDir == "" {
		t.Fatal("the length fallback left the socket in shared temp space")
	}
	if err := ensureSocketDir(p); err != nil {
		t.Fatalf("ensureSocketDir: %v", err)
	}
	info, err := os.Stat(p.PrivateSocketDir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("created the fallback directory as %#o, want 0700", perm)
	}

	// The socket is the daemon's entire access control story: anything that can
	// connect reads and writes every memory. A directory in shared temp space
	// that another local user can write to is one where they can bind this
	// predictable path first and answer with a forged preamble.
	if err := os.Chmod(p.PrivateSocketDir, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(p.PrivateSocketDir, 0o700)
	if err := ensureSocketDir(p); err == nil {
		t.Error("accepted a world-writable fallback socket directory")
	}
}

func TestSpawn_LeavesASocketThatStillAnswersAlone(t *testing.T) {
	cfg := testConfig(t)
	p := PathsFor(cfg)

	// A daemon binds before it builds its store, so while the embedding model
	// downloads it accepts into the backlog and writes no preamble — which
	// connect reports as a timeout, exactly like a dead socket. Treating that as
	// stale would unlink a daemon that holds the database and the ownership
	// lock, leaving every replacement to exit on the spot and nothing to dial.
	listener, err := net.Listen("unix", p.Socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	if err := spawn(p); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !running(p.Socket) {
		t.Error("spawn unlinked a socket that was still answering")
	}
	if _, err := os.Stat(p.Log); !os.IsNotExist(err) {
		t.Error("spawn started a daemon despite one already being up")
	}
}

func TestServe_ShutsDownWhileTheStoreIsStillLoading(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())

	building := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	newMCP := func() (*mcpserver.MCPServer, func(), error) {
		close(building)
		<-release
		return nil, func() {}, nil
	}

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, "1.0.0", newMCP, 0) }()
	<-building

	// Building the store downloads the embedding model on a first run, which
	// takes minutes, and the daemon's signal handler has already suppressed the
	// default terminate disposition. If the build is not interruptible, a
	// `daemon stop` signals a process that neither dies nor closes its listener,
	// and fails once its own timeout expires — taking export, import and reembed
	// with it.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want a clean exit", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve stayed blocked building the store after cancellation")
	}

	p := PathsFor(cfg)
	for _, path := range []string{p.Socket, p.PID} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived the shutdown", path)
		}
	}
}

func TestStop_TreatsAMissingPIDFileAsAlreadyStopped(t *testing.T) {
	// After an upgrade both sessions can dial the same stale daemon, both see
	// the version mismatch, and both retire it. The winner's daemon unlinks the
	// pid file on its way out, so the loser must not fail on a file that is gone
	// precisely because the work is done — it would give up instead of
	// connecting to the replacement.
	if err := stop(PathsFor(testConfig(t)), stopTimeout); err != nil {
		t.Errorf("stop with no pid file and nothing listening: %v", err)
	}
}

func TestConnTracker_TurnsAwayAClientOnceIdleShutdownBegins(t *testing.T) {
	fired := make(chan struct{})
	tracker := newConnTracker(10*time.Millisecond, func() { close(fired) })
	tracker.arm()
	<-fired

	// Nothing has been written to a connection accepted this late, so the only
	// safe answer is to drop it: the client retries and spawns a replacement.
	// Serving it instead would kill the session, since it has already dialled
	// successfully and will not dial again.
	if tracker.add() {
		t.Error("accepted a connection after idle shutdown began")
	}
}

func TestConnTracker_DoesNotShutDownOnAClientThatBeatTheTimer(t *testing.T) {
	// time.Timer.Stop reports false once the callback has started, so add cannot
	// cancel a timer that is already going off. The callback has to re-check the
	// count itself, or a client that connects in that instant loses its session.
	tracker := newConnTracker(time.Hour, func() {
		t.Error("idle shutdown fired with a client attached")
	})
	if !tracker.add() {
		t.Fatal("add refused the first connection")
	}
	tracker.fire()
	if tracker.stopped {
		t.Error("the tracker stopped despite an attached client")
	}
}

func TestStop_WaitsForTheOwnershipLockToBeReleased(t *testing.T) {
	cfg := testConfig(t)
	newMCP, _ := echoMCP("daemon")
	p, shutdown := startDaemon(t, cfg, "1.0.0", newMCP, 0)

	// stop() is what a retirement or an export relies on, and its caller
	// starts a replacement immediately afterwards. Waiting only for the socket
	// to close would let that replacement race the old daemon's last defers
	// and exit on the spot, so stop must not return while the lock is held.
	if err := shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := waitForLockFree(p, stopTimeout); err != nil {
		t.Fatal(err)
	}

	lock, held, err := takeLock(p.Lock)
	if err != nil {
		t.Fatalf("takeLock: %v", err)
	}
	if !held {
		t.Fatal("the ownership lock is still held after the daemon stopped")
	}
	if err := lock.Unlock(); err != nil {
		t.Errorf("unlock: %v", err)
	}
}
