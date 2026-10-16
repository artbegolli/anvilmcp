package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// mcpClient is a minimal MCP Streamable HTTP client: JSON-RPC over POST, with
// the session id echoed back on every subsequent call. Responses arrive either
// as plain JSON or as an SSE stream (which is also how server-initiated
// notifications, e.g. progress, reach us mid-request).
type mcpClient struct {
	url       string
	sessionID string
	nextID    int
	hc        *http.Client
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Params  json.RawMessage `json:"params"`
	Error   *rpcErr         `json:"error"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcErr) String() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// newClient connects and completes the MCP initialize handshake.
func newClient(t *testing.T, s *testServer) *mcpClient {
	t.Helper()
	c := &mcpClient{url: s.mcpURL, hc: &http.Client{Timeout: 60 * time.Second}}

	res, _ := c.rpc(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "anvilmcp-e2e", "version": "0"},
	})
	var init struct {
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(res, &init); err != nil {
		t.Fatalf("decode initialize result: %v (raw: %s)", err, res)
	}
	if init.ServerInfo.Name == "" {
		t.Fatalf("initialize returned no serverInfo.name: %s", res)
	}
	c.notify(t, "notifications/initialized", nil)
	return c
}

// rpc sends a request and returns its result plus any notifications that
// arrived on the same response stream.
func (c *mcpClient) rpc(t *testing.T, method string, params any) (json.RawMessage, []rpcMsg) {
	t.Helper()
	c.nextID++
	body := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		body["params"] = params
	}
	msgs := c.post(t, body)

	var result json.RawMessage
	var notes []rpcMsg
	for _, m := range msgs {
		switch {
		case m.Error != nil:
			t.Fatalf("%s returned %s", method, m.Error)
		case m.Method != "":
			notes = append(notes, m)
		case m.Result != nil:
			result = m.Result
		}
	}
	if result == nil {
		t.Fatalf("%s returned no result (messages: %d)", method, len(msgs))
	}
	return result, notes
}

func (c *mcpClient) notify(t *testing.T, method string, params any) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		body["params"] = params
	}
	c.post(t, body)
}

func (c *mcpClient) post(t *testing.T, body map[string]any) []rpcMsg {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatalf("POST %v: %v", body["method"], err)
	}
	defer func() { _ = resp.Body.Close() }()

	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		c.sessionID = id
	}
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %v: HTTP %d: %s", body["method"], resp.StatusCode, payload)
	}
	return parseMessages(payload)
}

// parseMessages pulls JSON-RPC messages out of either an SSE stream or a bare
// JSON body.
func parseMessages(payload []byte) []rpcMsg {
	var out []rpcMsg
	text := string(payload)
	if strings.Contains(text, "data:") {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var m rpcMsg
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &m); err == nil {
				out = append(out, m)
			}
		}
		return out
	}
	if strings.TrimSpace(text) == "" {
		return out
	}
	var m rpcMsg
	if err := json.Unmarshal(payload, &m); err == nil {
		out = append(out, m)
	}
	return out
}

// ---------------------------------------------------------------------------
// tool helpers

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError       bool `json:"isError"`
	notifications []rpcMsg
}

func (r toolResult) text() string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// decode unmarshals the tool's JSON payload into v.
func (r toolResult) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.text()), v); err != nil {
		t.Fatalf("decode tool payload: %v (raw: %s)", err, r.text())
	}
}

// callTool invokes a tool. meta is merged into params._meta (used for
// progressToken) when non-nil.
func (c *mcpClient) callTool(t *testing.T, name string, args, meta map[string]any) toolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	params := map[string]any{"name": name, "arguments": args}
	if meta != nil {
		params["_meta"] = meta
	}
	res, notes := c.rpc(t, "tools/call", params)

	var tr toolResult
	if err := json.Unmarshal(res, &tr); err != nil {
		t.Fatalf("decode tools/call result: %v (raw: %s)", err, res)
	}
	tr.notifications = notes
	return tr
}

// mustCallTool fails if the tool reported an error.
func (c *mcpClient) mustCallTool(t *testing.T, name string, args map[string]any) toolResult {
	t.Helper()
	tr := c.callTool(t, name, args, nil)
	if tr.IsError {
		t.Fatalf("%s failed: %s", name, tr.text())
	}
	return tr
}

// toolNames returns every registered tool name.
func (c *mcpClient) toolNames(t *testing.T) []string {
	t.Helper()
	res, _ := c.rpc(t, "tools/list", map[string]any{})
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
