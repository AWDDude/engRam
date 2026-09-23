package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AWDDude/engRam/internal/config"
	"github.com/AWDDude/engRam/internal/daemon"
	"github.com/AWDDude/engRam/internal/mcptest"
)

// This is the regression test for the bug the daemon exists to fix: a second
// engram process used to die on the bolt lock with "another engram process
// still has it open", and even sharing the file would not have helped, because
// each process served reads from its own in-memory copy.
//
// It runs the real binary, so it needs the embedding model already downloaded
// and skips otherwise rather than pulling ~130MB in a test.

// modelName is the default embedding model's directory name under model.path.
const modelName = "jinaai_jina-embeddings-v2-small-en"

// realModelDir finds an already-downloaded model to borrow, so the test needs
// no network. Only the model is shared; the database is always a temp copy.
func realModelDir(t *testing.T) string {
	t.Helper()
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory to find a model in: %v", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "engram", "models")
	if _, err := os.Stat(filepath.Join(dir, modelName)); err != nil {
		t.Skipf("embedding model not downloaded at %s; skipping end-to-end test", dir)
	}
	return dir
}

// buildEngram builds the binary under test once per test.
func buildEngram(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "engram")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building engram: %v\n%s", err, out)
	}
	return bin
}

// testEnv writes a config pointing at a scratch database and returns the
// environment that selects it, so nothing here can touch real memories.
func testEnv(t *testing.T, modelDir string) (env []string, dbPath string) {
	t.Helper()
	// Not t.TempDir(): on macOS its path is long enough to push the daemon
	// socket onto its length fallback, and this test is more useful exercising
	// the layout a real install gets.
	dir, err := os.MkdirTemp("", "engram-e2e")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	dbPath = filepath.Join(dir, "db")
	cfg := config.Default()
	cfg.Model.Path = modelDir
	cfg.DB.Path = dbPath
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshalling config: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	// The daemon deliberately outlives the processes that talk to it, so
	// without this every test run would leave one behind holding a deleted
	// database until its idle timeout expired.
	t.Cleanup(func() {
		if err := daemon.StopIfRunning(cfg); err != nil {
			t.Logf("stopping the test daemon: %v", err)
		}
	})

	return append(os.Environ(), "ENGRAM_CONFIG_PATH="+cfgPath), dbPath
}

// session is one `engram` process standing in for a Claude Code session.
type session struct {
	cmd    *exec.Cmd
	client *mcptest.Client
	stderr *safeBuffer
}

// safeBuffer collects a subprocess's stderr. exec writes it from its own
// goroutine while the test reads it for failure messages, so it needs a lock.
type safeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startSession(t *testing.T, bin string, env []string) *session {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr := &safeBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting engram: %v", err)
	}

	s := &session{cmd: cmd, stderr: stderr}
	t.Cleanup(func() { s.close() })

	// The first session pays for the daemon's startup, including loading the
	// model, so the handshake can take a while on a cold start.
	type result struct {
		client *mcptest.Client
		err    error
	}
	done := make(chan result, 1)
	go func() {
		client, err := mcptest.New(stdin, stdout)
		done <- result{client, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("mcp handshake: %v\nstderr: %s", r.err, stderr.String())
		}
		s.client = r.client
	case <-time.After(3 * time.Minute):
		t.Fatalf("timed out on the mcp handshake\nstderr: %s", stderr.String())
	}
	return s
}

func (s *session) close() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

// storeMemory stores a memory and returns its id.
func (s *session) storeMemory(t *testing.T, title, content string) string {
	t.Helper()
	out, err := s.client.CallTool("store", map[string]any{"title": title, "content": content})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	var stored struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &stored); err != nil {
		t.Fatalf("parsing store result %q: %v", out, err)
	}
	return stored.ID
}

// searchTitles returns the titles a search matched.
func (s *session) searchTitles(t *testing.T, query string) []string {
	t.Helper()
	out, err := s.client.CallTool("search", map[string]any{"query": query})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var results struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("parsing search result %q: %v", out, err)
	}
	titles := make([]string, 0, len(results.Results))
	for _, r := range results.Results {
		titles = append(titles, r.Title)
	}
	return titles
}

func run(t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("engram %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestConcurrentSessionsShareOneStore(t *testing.T) {
	modelDir := realModelDir(t)
	bin := buildEngram(t)
	env, dbPath := testEnv(t, modelDir)

	first := startSession(t, bin, env)
	second := startSession(t, bin, env)

	// Before this change the second process died here on the bolt lock.
	status := run(t, bin, env, "daemon", "status")
	if !strings.Contains(status, "running on") {
		t.Fatalf("expected a running daemon, got %q", status)
	}

	// One daemon for both sessions: the socket, lock and pid all live beside
	// the database, so exactly one pid file means exactly one owner.
	if _, err := os.Stat(filepath.Join(dbPath, "daemon.pid")); err != nil {
		t.Errorf("no pid file beside the database: %v", err)
	}

	// The cache-coherence half of the bug: a write in one session has to be
	// visible in the other without restarting anything.
	first.storeMemory(t, "Kestrel deployment runbook", "restart the kestrel service before draining")
	titles := second.searchTitles(t, "kestrel runbook")
	if len(titles) == 0 {
		t.Fatal("the second session could not see a memory the first stored")
	}
	found := false
	for _, title := range titles {
		if title == "Kestrel deployment runbook" {
			found = true
		}
	}
	if !found {
		t.Errorf("second session saw %v, missing the memory the first stored", titles)
	}

	// And the other direction, on the same daemon.
	second.storeMemory(t, "Osprey rollback notes", "roll the osprey release back with the previous tag")
	if titles := first.searchTitles(t, "osprey rollback"); len(titles) == 0 {
		t.Error("the first session could not see a memory the second stored")
	}
}

func TestCLICommandsTakeTheDatabaseFromTheDaemon(t *testing.T) {
	modelDir := realModelDir(t)
	bin := buildEngram(t)
	env, _ := testEnv(t, modelDir)

	session := startSession(t, bin, env)
	session.storeMemory(t, "Exported memory", "this should survive a round trip through CSV")

	// export needs the bolt file the daemon is holding. Stopping it is safe
	// precisely because the next session spawns a new one.
	csv := filepath.Join(t.TempDir(), "memories.csv")
	out := run(t, bin, env, "export", "-f", csv)
	if !strings.Contains(out, "Exported 1 memories") {
		t.Errorf("export said %q", out)
	}
	if status := run(t, bin, env, "daemon", "status"); !strings.Contains(status, "No engram daemon") {
		t.Errorf("expected the daemon to be stopped, got %q", status)
	}

	data, err := os.ReadFile(csv)
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}
	if !strings.Contains(string(data), "Exported memory") {
		t.Errorf("exported CSV is missing the memory:\n%s", data)
	}

	// The killed session's pipe is gone, so a new one has to bring the daemon
	// back by itself.
	revived := startSession(t, bin, env)
	if titles := revived.searchTitles(t, "exported memory"); len(titles) == 0 {
		t.Error("a new session could not reach a respawned daemon")
	}
	if status := run(t, bin, env, "daemon", "status"); !strings.Contains(status, "running on") {
		t.Errorf("expected a respawned daemon, got %q", status)
	}
}

func TestDaemonStopEndsTheSessionsAttachedToIt(t *testing.T) {
	modelDir := realModelDir(t)
	bin := buildEngram(t)
	env, _ := testEnv(t, modelDir)

	session := startSession(t, bin, env)
	session.storeMemory(t, "Doomed memory", "written before the daemon was stopped")

	if out := run(t, bin, env, "daemon", "stop"); !strings.Contains(out, "Stopped") {
		t.Errorf("daemon stop said %q", out)
	}

	// A client whose daemon goes away must exit rather than hang: the MCP
	// session state cannot be moved to a new daemon, so ending the process and
	// letting the MCP client restart it is the honest outcome.
	done := make(chan error, 1)
	go func() { done <- session.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the client kept running after its daemon stopped")
	}
	session.cmd.Process = nil // already reaped

	if out := run(t, bin, env, "daemon", "stop"); !strings.Contains(out, "No engram daemon") {
		t.Errorf("stopping an already-stopped daemon said %q", out)
	}
}

func TestClientRetiresADaemonRunningADifferentBuild(t *testing.T) {
	modelDir := realModelDir(t)
	env, _ := testEnv(t, modelDir)

	// Two builds of the same source that differ only in reported version, so
	// this tests the skew handling rather than any behavior change. Without
	// it, a `brew upgrade` would leave the old daemon serving every session
	// until the machine was rebooted.
	oldBin := buildEngramVersion(t, "1.0.0-old")
	newBin := buildEngramVersion(t, "2.0.0-new")

	startSession(t, oldBin, env)
	if status := run(t, oldBin, env, "daemon", "status"); !strings.Contains(status, "1.0.0-old") {
		t.Fatalf("expected the old daemon, got %q", status)
	}

	upgraded := startSession(t, newBin, env)
	status := run(t, newBin, env, "daemon", "status")
	if !strings.Contains(status, "2.0.0-new") {
		t.Errorf("expected the new daemon to have taken over, got %q", status)
	}

	// The upgraded session must work against the daemon it brought up.
	upgraded.storeMemory(t, "Post-upgrade memory", "stored after the daemon was replaced")
	if titles := upgraded.searchTitles(t, "post-upgrade"); len(titles) == 0 {
		t.Error("the upgraded session could not use its new daemon")
	}
}

func buildEngramVersion(t *testing.T, version string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "engram-"+version)
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building engram %s: %v\n%s", version, err, out)
	}
	return bin
}
