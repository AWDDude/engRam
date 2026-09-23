package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// notificationBuffer is how many server notifications may queue for one client
// before they are dropped. engram's tools send none — the buffer exists
// because the session interface requires a channel, and dropping beats
// blocking the server on a client that has stopped reading.
const notificationBuffer = 16

// socketSession is one connected client's MCP session.
//
// mcp-go's stdio transport cannot be reused here: its ClientSession is a
// package-level singleton with the fixed id "stdio" (see server/stdio.go), so
// a second concurrent Listen fails to register and the two connections fight
// over one writer. That is exactly the shape the daemon needs, so the daemon
// drives MCPServer.HandleMessage itself — the same entry point mcp-go's own
// HTTP transports use — and gives every connection its own session.
//
// The wire format is unchanged: newline-delimited JSON-RPC, identical to what
// an MCP client expects on stdio. That is what keeps the client a plain byte
// pipe with no protocol knowledge of its own.
type socketSession struct {
	id            string
	notifications chan mcp.JSONRPCNotification
	initialized   atomic.Bool
}

func newSocketSession() *socketSession {
	return &socketSession{
		id:            uuid.NewString(),
		notifications: make(chan mcp.JSONRPCNotification, notificationBuffer),
	}
}

func (s *socketSession) SessionID() string { return s.id }

func (s *socketSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	return s.notifications
}

func (s *socketSession) Initialize()       { s.initialized.Store(true) }
func (s *socketSession) Initialized() bool { return s.initialized.Load() }

// Compile-time proof that the session satisfies what mcp-go requires of one.
var _ mcpserver.ClientSession = (*socketSession)(nil)

// serveSession runs the JSON-RPC loop for one client until it disconnects or
// the daemon shuts down.
//
// Messages from a single client are handled in order rather than in parallel:
// a session's own calls are sequential in practice, and serving them in order
// avoids interleaving two writes to the same connection. Separate clients run
// in separate goroutines, which is where the concurrency that matters lives —
// the store's read lock lets their searches overlap.
func serveSession(ctx context.Context, conn net.Conn, mcpSrv *mcpserver.MCPServer, version string) error {
	if _, err := fmt.Fprintf(conn, "%s%s\n", preamblePrefix, version); err != nil {
		return fmt.Errorf("writing preamble: %w", err)
	}

	session := newSocketSession()
	if err := mcpSrv.RegisterSession(ctx, session); err != nil {
		return fmt.Errorf("registering session: %w", err)
	}
	defer mcpSrv.UnregisterSession(ctx, session.SessionID())
	ctx = mcpSrv.WithContext(ctx, session)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var writeMu sync.Mutex
	write := func(msg any) error {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshalling response: %w", err)
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err = conn.Write(append(data, '\n'))
		return err
	}

	// Notifications are written from their own goroutine, so the write mutex
	// above is what keeps them from interleaving with a response mid-line.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case notification := <-session.notifications:
				if err := write(notification); err != nil {
					return
				}
			}
		}
	}()

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF || isClosed(err) {
				return nil // the client went away, which is how sessions end
			}
			return fmt.Errorf("reading from client: %w", err)
		}
		if len(trimSpace(line)) == 0 {
			continue
		}
		// HandleMessage returns nil for a notification, which by definition
		// takes no reply.
		resp := mcpSrv.HandleMessage(ctx, line)
		if resp == nil {
			continue
		}
		if err := write(resp); err != nil {
			if isClosed(err) {
				return nil
			}
			return fmt.Errorf("writing to client: %w", err)
		}
	}
}

// trimSpace drops leading and trailing ASCII whitespace, which is all that can
// separate JSON-RPC frames on this transport.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// logSessionEnd reports an abnormal session end, staying quiet about the
// ordinary ones: a client disconnecting and the daemon shutting down.
func logSessionEnd(ctx context.Context, err error) {
	if err == nil || ctx.Err() != nil || isClosed(err) {
		return
	}
	log.Printf("client session ended: %v", err)
}
