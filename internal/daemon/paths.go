// Package daemon lets many engram processes share one database.
//
// bbolt holds an exclusive lock on its file for the lifetime of the process,
// and boltStore serves reads from in-memory maps loaded once at open, so two
// MCP servers can neither share the file nor see each other's writes. Rather
// than invent a cross-process locking protocol, one process (the daemon) keeps
// owning the database exactly as it does today, and every other process
// becomes a byte pipe to it: the store's existing transaction and locking
// guarantees carry over untouched because the same code runs in one process.
//
// The daemon is spawned on demand by the first client that finds no one
// listening, so nothing has to be installed or supervised.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/AWDDude/engRam/internal/config"
)

// Paths locates the daemon's runtime files. They sit alongside the bolt file
// rather than in one global location, so a config pointing at a different
// db.path gets its own daemon instead of contending for a shared socket.
type Paths struct {
	Socket string
	// PrivateSocketDir is set only when Socket falls back to the temp
	// directory, and names the directory that has to exist, and be ours alone,
	// before anything binds there. It is empty in the ordinary case, where the
	// socket sits in the database directory that Dial and Serve already create.
	PrivateSocketDir string
	// Lock is the daemon's ownership lock, held for the daemon's whole life.
	Lock string
	// SpawnLock keeps a burst of clients from each starting a daemon. It is
	// deliberately a different file from Lock: a client holds it across the
	// spawn, and if that were the same lock the daemon it just started would
	// find its own parent holding the thing it needs and exit immediately.
	SpawnLock string
	PID       string
	Log       string
}

// maxSocketPath bounds the socket path length. A unix socket address is a
// fixed-size field in sockaddr_un — 104 bytes on macOS, 108 on Linux — and
// exceeding it fails at bind with a message that says nothing about length.
// 100 leaves room under the smaller of the two.
const maxSocketPath = 100

// PathsFor derives the daemon's file locations from the configured database
// directory. A db.path deep enough to overflow the socket address falls back
// to a digest-named socket in the temp directory; the digest keeps it unique
// per database, which is the property that matters.
func PathsFor(cfg config.Config) Paths {
	dir := cfg.DB.Path
	privateDir := ""
	socket := filepath.Join(dir, "engram.sock")
	if len(socket) > maxSocketPath {
		// The socket is the daemon's entire access control: anything that can
		// connect can read and write every memory. A name in the temp
		// directory, which is world-writable and sticky on Linux, derived only
		// from the database path is one another local user can predict and bind
		// first, answering with a forged preamble. So the fallback goes in a
		// per-user directory that ensureSocketDir refuses unless it is
		// owner-only.
		sum := sha256.Sum256([]byte(dir))
		privateDir = filepath.Join(os.TempDir(), "engram-"+strconv.Itoa(os.Getuid()))
		socket = filepath.Join(privateDir, hex.EncodeToString(sum[:])[:12]+".sock")
	}
	return Paths{
		Socket:           socket,
		PrivateSocketDir: privateDir,
		Lock:             filepath.Join(dir, "daemon.lock"),
		SpawnLock:        filepath.Join(dir, "spawn.lock"),
		PID:              filepath.Join(dir, "daemon.pid"),
		Log:              filepath.Join(dir, "daemon.log"),
	}
}

// ensureSocketDir creates the fallback socket directory, and does nothing when
// the socket lives beside the bolt file.
//
// The mode check is the point of it: MkdirAll succeeds on a directory that
// already exists whoever created it, and in shared temp space that could be
// another local user's. Owner-only is the condition that makes binding there
// safe — a directory we can write to and they can too is one where they can
// bind the socket first, and a directory they locked down instead is one we
// cannot traverse, so the bind fails loudly rather than quietly reaching them.
func ensureSocketDir(p Paths) error {
	if p.PrivateSocketDir == "" {
		return nil
	}
	if err := os.MkdirAll(p.PrivateSocketDir, 0o700); err != nil {
		return fmt.Errorf("creating socket dir %s: %w", p.PrivateSocketDir, err)
	}
	info, err := os.Stat(p.PrivateSocketDir)
	if err != nil {
		return fmt.Errorf("checking socket dir %s: %w", p.PrivateSocketDir, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("socket dir %s is mode %#o, want owner-only access", p.PrivateSocketDir, perm)
	}
	return nil
}
