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
	"os"
	"path/filepath"

	"github.com/AWDDude/engRam/internal/config"
)

// Paths locates the daemon's runtime files. They sit alongside the bolt file
// rather than in one global location, so a config pointing at a different
// db.path gets its own daemon instead of contending for a shared socket.
type Paths struct {
	Socket string
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
	socket := filepath.Join(dir, "engram.sock")
	if len(socket) > maxSocketPath {
		sum := sha256.Sum256([]byte(dir))
		socket = filepath.Join(os.TempDir(), "engram-"+hex.EncodeToString(sum[:])[:12]+".sock")
	}
	return Paths{
		Socket:    socket,
		Lock:      filepath.Join(dir, "daemon.lock"),
		SpawnLock: filepath.Join(dir, "spawn.lock"),
		PID:       filepath.Join(dir, "daemon.pid"),
		Log:       filepath.Join(dir, "daemon.log"),
	}
}
