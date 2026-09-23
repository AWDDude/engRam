// Package mcptest is a minimal MCP client for tests.
//
// It exists because the daemon's whole design rests on MCP travelling
// unchanged over a unix socket and through a byte-pipe client, and the only
// way to prove that is to speak the protocol at both ends. mcp-go ships no
// client, and the tests need very little of one.
package mcptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// protocolVersion is the revision the tests negotiate. The server accepts what
// the client asks for here; nothing in these tests depends on the differences
// between revisions.
const protocolVersion = "2024-11-05"

// Client speaks newline-delimited JSON-RPC over any stream: a socket for the
// daemon's own tests, a subprocess's stdio for the end-to-end ones.
type Client struct {
	enc    *json.Encoder
	reader *bufio.Reader
	nextID int
}

// New starts a client and completes the MCP handshake.
func New(w io.Writer, r io.Reader) (*Client, error) {
	c := &Client{enc: json.NewEncoder(w), reader: bufio.NewReader(r), nextID: 1}
	if _, err := c.Call("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mcptest", "version": "1"},
	}); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		return nil, fmt.Errorf("initialized notification: %w", err)
	}
	return c, nil
}

type response struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Call sends a request and returns its result, skipping any notification the
// server sends in the meantime.
func (c *Client) Call(method string, params any) (json.RawMessage, error) {
	id := c.nextID
	c.nextID++
	if err := c.enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return nil, err
	}
	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("reading response to %s: %w", method, err)
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("parsing response to %s: %w (%s)", method, err, line)
		}
		if resp.ID != id {
			continue // a notification or a response to something else
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s failed: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// toolResult is the shape of a tools/call response, trimmed to the text
// content the engram tools return.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// CallTool invokes a tool and returns its text content. A tool that reports an
// error returns that error's text, so a caller can assert on it.
func (c *Client) CallTool(name string, args map[string]any) (string, error) {
	raw, err := c.Call("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("parsing %s result: %w", name, err)
	}
	var text string
	for _, part := range result.Content {
		text += part.Text
	}
	if result.IsError {
		return text, fmt.Errorf("tool %s reported an error: %s", name, text)
	}
	return text, nil
}

// ToolNames lists the tools the server advertises.
func (c *Client) ToolNames() ([]string, error) {
	raw, err := c.Call("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, fmt.Errorf("parsing tools/list result: %w", err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	return names, nil
}
