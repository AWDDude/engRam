package daemon

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// preamblePrefix opens the single line the daemon writes before handing a
// connection to the MCP server. It exists so a client can tell which build is
// answering: after an upgrade the old daemon is still running and would
// otherwise keep serving old code to a new binary until the machine rebooted.
const preamblePrefix = "engram-daemon "

// connectTimeout bounds a single dial. Connecting to a unix socket is a local
// syscall, so anything slower than this means the listener is wedged rather
// than busy.
const connectTimeout = 2 * time.Second

// Conn is a live connection to the daemon with the version preamble already
// consumed.
//
// Read is overridden on purpose: reading the preamble goes through a bufio
// reader, which may have pulled the first bytes of the MCP stream into its
// buffer along with the line. Reading the embedded net.Conn directly would
// skip them and desynchronize the protocol.
type Conn struct {
	net.Conn
	reader *bufio.Reader

	// Version is the daemon's build, taken from the preamble.
	Version string
}

func (c *Conn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// connect dials the socket and reads the daemon's version preamble.
func connect(socket string) (*Conn, error) {
	c, err := net.DialTimeout("unix", socket, connectTimeout)
	if err != nil {
		return nil, err
	}
	// The preamble is written immediately on accept, so a daemon that is up
	// answers at once. A deadline here keeps a half-open socket from hanging
	// the client forever.
	if err := c.SetReadDeadline(time.Now().Add(connectTimeout)); err != nil {
		c.Close()
		return nil, err
	}
	reader := bufio.NewReader(c)
	line, err := reader.ReadString('\n')
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("reading daemon preamble: %w", err)
	}
	if !strings.HasPrefix(line, preamblePrefix) {
		c.Close()
		return nil, fmt.Errorf("unexpected greeting on %s: %q", socket, strings.TrimSpace(line))
	}
	// Clear the deadline: from here the connection carries the MCP stream,
	// which is idle for as long as the session is idle.
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		c.Close()
		return nil, err
	}
	return &Conn{
		Conn:    c,
		reader:  reader,
		Version: strings.TrimSpace(strings.TrimPrefix(line, preamblePrefix)),
	}, nil
}

// running reports whether a daemon is currently answering on the socket.
func running(socket string) bool {
	c, err := net.DialTimeout("unix", socket, connectTimeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func writePID(path string) error {
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing pid file %s: %w", path, err)
	}
	return nil
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("reading pid file %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parsing pid file %s: %w", path, err)
	}
	return pid, nil
}

// logTail returns roughly the last n lines of the daemon log, for surfacing a
// startup failure that the client only ever sees as a dial timeout.
func logTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// pipe copies the MCP stream both ways between the client's stdio and the
// daemon, returning when either side closes. The client parses nothing: the
// bytes are already MCP in both directions, so there is no second protocol to
// keep in sync with the tool definitions.
func pipe(conn *Conn, stdin io.Reader, stdout io.Writer) error {
	go func() {
		_, err := io.Copy(conn, stdin)
		if err != nil && !isClosed(err) {
			// Nothing can be done about a failed send here, and stderr is the
			// MCP client's log, so say it there and let the read side below
			// end the session.
			fmt.Fprintf(os.Stderr, "engram: sending to the daemon: %v\n", err)
		}
		// Half-close rather than close: the daemon's reader sees EOF and winds
		// the session down, while this side stays open to receive whatever it
		// is still writing.
		_ = conn.CloseWrite()
	}()

	// The daemon closing its end is what ends the session, whether that came
	// from our own EOF above or from the daemon shutting down. Returning on
	// the send side instead would cut off a reply still in flight.
	_, err := io.Copy(stdout, conn)
	conn.Close()
	if err != nil && !isClosed(err) {
		return err
	}
	return nil
}

// CloseWrite half-closes the connection, which is what lets the client signal
// it has nothing more to send while still reading the daemon's last reply.
// Unix sockets support it; the assertion keeps that assumption explicit rather
// than relying on the concrete type.
func (c *Conn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func isClosed(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "file already closed")
}
