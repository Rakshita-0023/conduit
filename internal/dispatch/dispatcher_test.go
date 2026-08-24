package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduit-mcp/conduit/internal/audit"
	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/health"
	"github.com/conduit-mcp/conduit/internal/policy"
	"github.com/conduit-mcp/conduit/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func dispatchConfig(url, auditPath string) config.Config {
	return config.Config{Audit: config.Audit{Path: auditPath}, Limits: config.Limits{MaxAggregateTools: 8, MaxAggregateResponseBytes: 1 << 20, MaxToolResponseBytes: 1 << 20, ToolCallTimeout: time.Second}, Policy: config.Policy{Allow: []string{"x.*"}}, Servers: []config.Downstream{{ID: "x", URL: url, Headers: map[string]string{"X-Configured": "yes"}}}}
}

func readyDispatcher(t *testing.T, c config.Config, tool *mcp.Tool) (*Dispatcher, *registry.Registry, *audit.Log) {
	t.Helper()
	p, err := policy.Compile(c.Policy)
	if err != nil {
		t.Fatal(err)
	}
	r := registry.New(c.Limits, p, mcp.Implementation{Name: "conduit", Version: "test"})
	snapshot, err := r.Publish(registry.Catalog{ServerID: "x", Tools: []*mcp.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := audit.Open(c.Audit.Path)
	if err != nil {
		t.Fatal(err)
	}
	h := health.New([]string{"x"})
	h.SetLive(true)
	h.SetServer("x", "healthy", 1, "")
	h.SetAggregate(snapshot.Generation, string(snapshot.State), snapshot.ToolCount)
	return New(c, r, a, h, "test"), r, a
}

func TestOneInvocationIsOneToolCallWithoutHandshake(t *testing.T) {
	var calls atomic.Int32
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Configured") != "yes" || r.Header.Get("Mcp-Method") != "tools/call" || r.Header.Get("Mcp-Name") != "original" || r.Header.Get("MCP-Protocol-Version") != protocolVersion {
			t.Errorf("headers=%v", r.Header)
		}
		if b, _ := os.ReadFile(p); !strings.Contains(string(b), "tool_call_authorized") {
			t.Error("transport started before durable authorization audit")
		}
		var req struct {
			ID     any                        `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "tools/call" {
			t.Errorf("method=%q", req.Method)
		}
		var name string
		_ = json.Unmarshal(req.Params["name"], &name)
		if name != "original" {
			t.Errorf("name=%q", name)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}, "structuredContent": map[string]any{"n": 9007199254740993}}})
	}))
	defer server.Close()
	c := dispatchConfig(server.URL, p)
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	res, err := d.Execute(context.Background(), Call{PublicName: "x.original", Arguments: json.RawMessage(`{"value":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !strings.Contains(string(res.Result), "9007199254740993") {
		t.Fatalf("calls=%d result=%s", calls.Load(), res.Result)
	}
}

func TestDeniedAndUnknownNeverDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	if _, err := d.Execute(context.Background(), Call{PublicName: "missing"}); err == nil {
		t.Fatal("want unavailable")
	}
	if calls.Load() != 0 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestGenerationChangeBeforeAuthorizationNeverDispatchesStaleRoute(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	d, r, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	var once atomic.Bool
	d.testBeforeCommit = func() {
		if once.CompareAndSwap(false, true) {
			_, err := r.Publish(registry.Catalog{ServerID: "x", Tools: []*mcp.Tool{{Name: "replacement", InputSchema: map[string]any{"type": "object"}}}})
			if err != nil {
				t.Errorf("publish: %v", err)
			}
		}
	}
	_, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
	if local, ok := err.(*Error); !ok || local.Code != CodeToolUnavailable || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestAuditFailureBeforeDispatchPreventsTransport(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
	local, ok := err.(*Error)
	if !ok || local.Code != CodeAuditUnavailable || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestTerminalAuditFailurePreservesValidResultAndBlocksNewAuthorization(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID any `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
	}))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	result := make(chan struct {
		res Response
		err error
	}, 1)
	go func() {
		res, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
		result <- struct {
			res Response
			err error
		}{res, err}
	}()
	<-entered
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	got := <-result
	if got.err != nil || !strings.Contains(string(got.res.Result), "ok") || d.health.Snapshot().AuditHealthy {
		t.Fatalf("result=%s err=%v health=%+v", got.res.Result, got.err, d.health.Snapshot())
	}
	_, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
	if local, ok := err.(*Error); !ok || local.Code != CodeAuditUnavailable {
		t.Fatalf("next err=%v", err)
	}
}

func TestInputRequiredCompatibility(t *testing.T) {
	for name, result := range map[string]string{
		"request state": `{"resultType":"input_required","requestState":"opaque","content":[]}`,
		"empty":         `{"resultType":"input_required","content":[]}`,
		"elicitation":   `{"resultType":"input_required","requestState":"opaque","inputRequests":{"x":{"method":"elicitation/create","params":{"message":"x"}}},"content":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID any `json:"id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + `"` + req.ID.(string) + `","result":` + result + `}`))
			}))
			defer server.Close()
			c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
			d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
			defer a.Close()
			_, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
			if name == "request state" && err != nil {
				t.Fatal(err)
			}
			if name != "request state" {
				local, ok := err.(*Error)
				if !ok || local.Code != CodeToolResponseUnsupported {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
}

func TestSSEAndOversizeAreUnknownAfterDispatch(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"sse": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {}\n\n"))
		},
		"oversize": func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID any `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"` + req.ID.(string) + `","result":{"content":[{"type":"text","text":"` + strings.Repeat("x", 1024) + `"}]}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
			if name == "oversize" {
				c.Limits.MaxToolResponseBytes = 64
			}
			d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
			defer a.Close()
			_, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
			local, ok := err.(*Error)
			if !ok || local.Code != CodeToolOutcomeUnknown {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestSSESessionIsCleanedUpWithoutRetry(t *testing.T) {
	for _, withSession := range []bool{true, false} {
		name := "stateless"
		if withSession {
			name = "stateful"
		}
		t.Run(name, func(t *testing.T) {
			var posts, deletes atomic.Int32
			sessions := map[string]bool{}
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					posts.Add(1)
					var req struct {
						ID     any    `json:"id"`
						Method string `json:"method"`
					}
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.Method != "tools/call" {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if withSession {
						mu.Lock()
						sessions["sse-session"] = true
						mu.Unlock()
						w.Header().Set("Mcp-Session-Id", "sse-session")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("event: message\ndata: ignored\n\n"))
				case http.MethodDelete:
					deletes.Add(1)
					mu.Lock()
					defer mu.Unlock()
					if !sessions[r.Header.Get("Mcp-Session-Id")] {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					delete(sessions, r.Header.Get("Mcp-Session-Id"))
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			defer server.Close()
			c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
			d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
			defer a.Close()
			_, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
			if local, ok := err.(*Error); !ok || local.Code != CodeToolOutcomeUnknown {
				t.Fatalf("err=%v", err)
			}
			wantDeletes := int32(0)
			if withSession {
				wantDeletes = 1
			}
			if posts.Load() != 1 || deletes.Load() != wantDeletes {
				t.Fatalf("posts=%d deletes=%d", posts.Load(), deletes.Load())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(sessions) != 0 {
				t.Fatalf("sessions=%v", sessions)
			}
		})
	}
}

func TestConnectionResetIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	_, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
	if local, ok := err.(*Error); !ok || local.Code != CodeToolOutcomeUnknown || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestToolResponseLimitExactAndOneOver(t *testing.T) {
	const internalID = "abcdefghijklmnopqrstuv"
	result := `{"jsonrpc":"2.0","id":"` + internalID + `","result":{"content":[{"type":"text","text":"ok"}]}}`
	for name, limit := range map[string]int64{"exact": int64(len(result)), "one over": int64(len(result) - 1)} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID string `json:"id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				body := strings.Replace(result, internalID, req.ID, 1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
			c.Limits.MaxToolResponseBytes = limit
			d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
			defer a.Close()
			response, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
			if name == "exact" && (err != nil || !strings.Contains(string(response.Result), "ok")) {
				t.Fatalf("result=%s err=%v", response.Result, err)
			}
			if name == "one over" {
				if local, ok := err.(*Error); !ok || local.Code != CodeToolOutcomeUnknown {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
}

func TestToolResponseLimitMaxInt64AllowsSmallResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID any `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
	}))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	c.Limits.MaxToolResponseBytes = math.MaxInt64
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	response, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
	if err != nil || !strings.Contains(string(response.Result), "ok") {
		t.Fatalf("response=%s err=%v", response.Result, err)
	}
}

func TestLargeToolResponseOverflowReadIsBounded(t *testing.T) {
	const limit = int64(64)
	source := &countingReadCloser{Reader: strings.NewReader(strings.Repeat("x", 1<<20))}
	body := &boundedBody{ReadCloser: source, max: limit}
	_, err := io.ReadAll(body)
	if err == nil {
		t.Fatal("want limit error")
	}
	if source.read > limit+1 {
		t.Fatalf("read %d bytes, want no more than %d", source.read, limit+1)
	}
}

type countingReadCloser struct {
	io.Reader
	read int64
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += int64(n)
	return n, err
}

func (*countingReadCloser) Close() error { return nil }

func TestNon2xxCorrelatedJSONRPCErrorIsPreserved(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     func(string) string
		wantWire bool
		wantCode int64
		wantData string
	}{
		{"400 correlated", http.StatusBadRequest, func(id string) string {
			return `{"jsonrpc":"2.0","id":"` + id + `","error":{"code":-32602,"message":"bad params","data":{"field":"x"}}}`
		}, true, -32602, `{"field":"x"}`},
		{"404 correlated", http.StatusNotFound, func(id string) string {
			return `{"jsonrpc":"2.0","id":"` + id + `","error":{"code":-32055,"message":"not found","data":[1,2]}}`
		}, true, -32055, `[1,2]`},
		{"wrong ID", http.StatusBadRequest, func(string) string { return `{"jsonrpc":"2.0","id":"wrong","error":{"code":-32602,"message":"bad"}}` }, false, 0, ""},
		{"malformed", http.StatusBadRequest, func(string) string { return `{not json` }, false, 0, ""},
		{"oversized", http.StatusBadRequest, func(id string) string {
			return `{"jsonrpc":"2.0","id":"` + id + `","error":{"code":-32602,"message":"` + strings.Repeat("x", 1024) + `"}}`
		}, false, 0, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req struct {
					ID string `json:"id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body(req.ID)))
			}))
			defer server.Close()
			c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
			if test.name == "oversized" {
				c.Limits.MaxToolResponseBytes = 64
			}
			d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
			defer a.Close()
			response, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
			if test.wantWire {
				if err != nil || response.Error == nil || response.Error.Code != test.wantCode || string(response.Error.Data) != test.wantData {
					t.Fatalf("response=%+v err=%v", response, err)
				}
				return
			}
			if local, ok := err.(*Error); !ok || local.Code != CodeToolOutcomeUnknown {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestHTTP200JSONRPCErrorPreservesFidelity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"` + req.ID + `","error":{"code":-32602,"message":"bad params","data":{"nested":[1,true]}}}`))
	}))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	response, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
	if err != nil || response.Error == nil || response.Error.Code != -32602 || response.Error.Message != "bad params" || string(response.Error.Data) != `{"nested":[1,true]}` {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestStatefulDownstreamSessionsAreCleanedUp(t *testing.T) {
	var mu sync.Mutex
	sessions := map[string]bool{}
	var posts, deletes, otherPosts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			var req struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Method != "tools/call" || r.Header.Get("Mcp-Method") != "tools/call" || r.Header.Get("Mcp-Name") != "original" || r.Header.Get("Mcp-Session-Id") != "" {
				otherPosts++
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			posts++
			sessionID := "session-" + strconv.Itoa(posts)
			sessions[sessionID] = true
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", sessionID)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
		case http.MethodDelete:
			sessionID := r.Header.Get("Mcp-Session-Id")
			if !sessions[sessionID] || r.URL.Path != "/" || r.Header.Get("Mcp-Method") != "" || r.Header.Get("Mcp-Name") != "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			deletes++
			delete(sessions, sessionID)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	for range 2 {
		response, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
		if err != nil || !strings.Contains(string(response.Result), "ok") {
			t.Fatalf("response=%s err=%v", response.Result, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 2 || deletes != 2 || otherPosts != 0 || len(sessions) != 0 {
		t.Fatalf("posts=%d deletes=%d otherPosts=%d sessions=%v", posts, deletes, otherPosts, sessions)
	}
}

func TestConfiguredSessionHeaderPreventsDispatch(t *testing.T) {
	for _, header := range []string{"Mcp-Session-Id", "mcp-session-id"} {
		t.Run(header, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer server.Close()
			c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
			c.Servers[0].Headers = map[string]string{header: "fixed-session"}
			d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
			defer a.Close()
			if _, err := d.Execute(context.Background(), Call{PublicName: "x.original"}); err == nil {
				t.Fatal("want dispatch failure")
			}
			if calls.Load() != 0 {
				t.Fatalf("downstream calls=%d", calls.Load())
			}
		})
	}
}

func TestShutdownCancelsStatefulCleanupWithoutReplacingResult(t *testing.T) {
	cleanupStarted := make(chan struct{})
	cleanupCancelled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	var posts, deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			var req struct {
				ID any `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "owned-session")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
		case http.MethodDelete:
			deletes.Add(1)
			if r.Header.Get("Mcp-Session-Id") != "owned-session" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			startOnce.Do(func() { close(cleanupStarted) })
			<-r.Context().Done()
			cancelOnce.Do(func() { close(cleanupCancelled) })
		}
	}))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	c.Limits.ToolCallTimeout = 5 * time.Second
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	result := make(chan struct {
		response Response
		err      error
	}, 1)
	go func() {
		response, err := d.Execute(context.Background(), Call{PublicName: "x.original"})
		result <- struct {
			response Response
			err      error
		}{response, err}
	}()
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup DELETE did not start")
	}
	d.CancelActive()
	select {
	case <-cleanupCancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel cleanup DELETE")
	}
	waited := make(chan struct{})
	go func() { d.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("Dispatcher.Wait outlived cancelled cleanup")
	}
	got := <-result
	if got.err != nil || !strings.Contains(string(got.response.Result), "ok") {
		t.Fatalf("response=%s err=%v", got.response.Result, got.err)
	}
	if posts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("posts=%d deletes=%d", posts.Load(), deletes.Load())
	}
}

func TestStatelessDownstreamDoesNotReceiveCleanupDelete(t *testing.T) {
	var posts, deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			var req struct {
				ID any `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	c := dispatchConfig(server.URL, filepath.Join(t.TempDir(), "audit.jsonl"))
	d, _, a := readyDispatcher(t, c, &mcp.Tool{Name: "original", InputSchema: map[string]any{"type": "object"}})
	defer a.Close()
	if _, err := d.Execute(context.Background(), Call{PublicName: "x.original"}); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 || deletes.Load() != 0 {
		t.Fatalf("posts=%d deletes=%d", posts.Load(), deletes.Load())
	}
}

func TestConduitErrorCodesUseImplementationDefinedRange(t *testing.T) {
	for _, code := range []int64{CodeToolUnavailable, CodeToolDispatchFailed, CodeToolOutcomeUnknown, CodeToolResponseUnsupported, CodeAuditUnavailable} {
		if code < -32019 || code > -32000 {
			t.Fatalf("code %d outside implementation-defined range", code)
		}
	}
}
