package parity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rakshita-0023/conduit/internal/app"
	"github.com/Rakshita-0023/conduit/internal/config"
)

// These scenarios intentionally use blocking handlers or response sizes derived
// from the downstream request. They remain code-driven rather than extending
// the JSON fixture format into a lifecycle DSL.
func TestGoOracleCodeDrivenScenarios(t *testing.T) {
	t.Run("no automatic tools/call retry", func(t *testing.T) {
		var calls atomic.Int32
		downstream := newLoopbackServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method := downstreamMethod(r)
			switch method {
			case "server/discover":
				discoverReply(w, downstreamID(r))
			case "tools/list":
				listReply(w, downstreamID(r), "search")
			case "tools/call":
				calls.Add(1)
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = connection.Close()
				}
			}
		}))
		defer downstream.Close()
		instance, baseURL, auditPath, stop := startOracleApp(t, downstream.URL, time.Hour, 1<<20)
		defer stop()
		call := publicMCP(t, baseURL, 101, "tools/call", map[string]any{"name": "x.search", "arguments": map[string]any{}})
		assertPublicError(t, call, -32012)
		if calls.Load() != 1 {
			t.Fatalf("tools/call POSTs=%d want 1", calls.Load())
		}
		assertAuditEvents(t, auditPath, "audit_ready", "tool_call_authorized", "tool_call_unknown_after_dispatch")
		_ = instance
	})

	t.Run("response byte exact boundary and one byte over", func(t *testing.T) {
		const internalID = "abcdefghijklmnopqrstuv"
		template := `{"jsonrpc":"2.0","id":"` + internalID + `","result":{"content":[{"type":"text","text":"ok"}]}}`
		for _, test := range []struct {
			name  string
			limit int64
			code  int
		}{
			{name: "exact", limit: int64(len(template)), code: 0},
			{name: "one_over", limit: int64(len(template) - 1), code: -32012},
		} {
			t.Run(test.name, func(t *testing.T) {
				downstream := newLoopbackServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch downstreamMethod(r) {
					case "server/discover":
						discoverReply(w, downstreamID(r))
					case "tools/list":
						listReply(w, downstreamID(r), "search")
					case "tools/call":
						id := downstreamID(r)
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(strings.Replace(template, internalID, fmt.Sprint(id), 1)))
					}
				}))
				defer downstream.Close()
				_, baseURL, _, stop := startOracleApp(t, downstream.URL, time.Hour, test.limit)
				defer stop()
				call := publicMCP(t, baseURL, 101, "tools/call", map[string]any{"name": "x.search", "arguments": map[string]any{}})
				if test.code == 0 {
					if call.Status != http.StatusOK || toolText(call.JSON) != "ok" {
						t.Fatalf("call=%+v", call)
					}
					return
				}
				assertPublicError(t, call, test.code)
			})
		}
	})

	t.Run("audit unavailable prevents downstream side effect", func(t *testing.T) {
		var calls atomic.Int32
		downstream := newLoopbackServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch downstreamMethod(r) {
			case "server/discover":
				discoverReply(w, downstreamID(r))
			case "tools/list":
				listReply(w, downstreamID(r), "search")
			case "tools/call":
				calls.Add(1)
				resultReply(w, downstreamID(r), "unexpected")
			}
		}))
		defer downstream.Close()
		instance, baseURL, _, stop := startOracleApp(t, downstream.URL, time.Hour, 1<<20)
		defer stop()
		if err := instance.Audit.Close(); err != nil {
			t.Fatal(err)
		}
		call := publicMCP(t, baseURL, 101, "tools/call", map[string]any{"name": "x.search", "arguments": map[string]any{}})
		assertPublicError(t, call, -32014)
		if calls.Load() != 0 {
			t.Fatalf("tools/call POSTs=%d want 0", calls.Load())
		}
	})

	t.Run("authorization audit precedes downstream call", func(t *testing.T) {
		var auditPath string
		downstream := newLoopbackServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch downstreamMethod(r) {
			case "server/discover":
				discoverReply(w, downstreamID(r))
			case "tools/list":
				listReply(w, downstreamID(r), "search")
			case "tools/call":
				contents, err := os.ReadFile(auditPath)
				if err != nil || !strings.Contains(string(contents), "tool_call_authorized") {
					t.Errorf("authorization audit not durable before transport: %q err=%v", contents, err)
				}
				resultReply(w, downstreamID(r), "ok")
			}
		}))
		defer downstream.Close()
		_, baseURL, path, stop := startOracleApp(t, downstream.URL, time.Hour, 1<<20)
		defer stop()
		auditPath = path
		call := publicMCP(t, baseURL, 101, "tools/call", map[string]any{"name": "x.search", "arguments": map[string]any{}})
		if toolText(call.JSON) != "ok" {
			t.Fatalf("call=%+v", call)
		}
		assertAuditEvents(t, auditPath, "audit_ready", "tool_call_authorized", "tool_call_completed")
	})

	t.Run("refresh failure removes and recovery restores catalog", func(t *testing.T) {
		var failing atomic.Bool
		downstream := newLoopbackServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if failing.Load() {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			switch downstreamMethod(r) {
			case "server/discover":
				discoverReply(w, downstreamID(r))
			case "tools/list":
				listReply(w, downstreamID(r), "search")
			}
		}))
		defer downstream.Close()
		_, baseURL, _, stop := startOracleApp(t, downstream.URL, 20*time.Millisecond, 1<<20)
		defer stop()
		failing.Store(true)
		waitForStatus(t, baseURL, func(status map[string]any) bool {
			return status["ready"] == false && toolCount(status) == 0
		})
		failing.Store(false)
		waitForStatus(t, baseURL, func(status map[string]any) bool {
			return status["ready"] == true && toolCount(status) == 1
		})
	})

	t.Run("shutdown cancels active dispatch before audit close", func(t *testing.T) {
		started := make(chan struct{})
		cancelled := make(chan struct{})
		downstream := newLoopbackServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch downstreamMethod(r) {
			case "server/discover":
				discoverReply(w, downstreamID(r))
			case "tools/list":
				listReply(w, downstreamID(r), "search")
			case "tools/call":
				close(started)
				<-r.Context().Done()
				close(cancelled)
			}
		}))
		defer downstream.Close()
		instance, baseURL, auditPath, stop := startOracleApp(t, downstream.URL, time.Hour, 1<<20)
		defer stop()
		callDone := make(chan error, 1)
		go func() {
			_, err := sendPublicMCP(baseURL, 101, "tools/call", map[string]any{"name": "x.search", "arguments": map[string]any{}}, "x.search", nil)
			callDone <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("downstream tools/call did not start")
		}
		closeDone := make(chan error, 1)
		go func() { closeDone <- instance.Close(context.Background()) }()
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("shutdown did not cancel downstream")
		}
		select {
		case err := <-closeDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown did not complete")
		}
		if !strings.Contains(string(mustRead(t, auditPath)), "tool_call_unknown_after_dispatch") {
			t.Fatal("missing terminal unknown audit")
		}
		if err := <-callDone; err != nil {
			t.Fatal(err)
		}
	})
}

func startOracleApp(t *testing.T, downstreamURL string, refreshInterval time.Duration, toolResponseLimit int64) (*app.App, string, string, func()) {
	t.Helper()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	cfg := config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Audit:    config.Audit{Path: auditPath},
		Policy:   config.Policy{Allow: []string{"x.*"}},
		Limits: config.Limits{
			MaxPagesPerDownstream: 4, MaxToolsPerDownstream: 8,
			MaxDownstreamCatalogBytes: 1 << 20, MaxAggregateTools: 8,
			MaxAggregateResponseBytes: 1 << 20, MaxToolResponseBytes: toolResponseLimit,
			CatalogRefreshInterval: refreshInterval, RequestTimeout: time.Second, ToolCallTimeout: time.Second,
		},
		Servers: []config.Downstream{{ID: "x", URL: downstreamURL}},
	}
	instance, err := app.Start(context.Background(), cfg, "parity-go")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- instance.Server.Serve(instance.Listener) }()
	baseURL := "http://" + instance.Listener.Addr().String()
	waitForStatus(t, baseURL, func(status map[string]any) bool { return status["ready"] == true })
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = instance.Close(context.Background())
		<-serveErr
	}
	return instance, baseURL, auditPath, stop
}

func downstreamMethod(r *http.Request) string {
	var request struct {
		Method string `json:"method"`
	}
	decodeDownstreamRequest(r, &request)
	return request.Method
}

func downstreamID(r *http.Request) any {
	var request struct {
		ID any `json:"id"`
	}
	decodeDownstreamRequest(r, &request)
	return request.ID
}

func decodeDownstreamRequest(r *http.Request, destination any) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	_ = json.Unmarshal(body, destination)
}

func discoverReply(w http.ResponseWriter, id any) {
	w.Header().Set("Content-Type", "application/json")
	writeRPC(w, http.StatusOK, id, map[string]any{"resultType": "complete", "supportedVersions": []string{protocolVersion}, "capabilities": map[string]any{"tools": map[string]any{}}})
}

func listReply(w http.ResponseWriter, id any, name string) {
	w.Header().Set("Content-Type", "application/json")
	writeRPC(w, http.StatusOK, id, map[string]any{"resultType": "complete", "tools": []any{map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}}}})
}

func resultReply(w http.ResponseWriter, id any, text string) {
	w.Header().Set("Content-Type", "application/json")
	writeRPC(w, http.StatusOK, id, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
}

func waitForStatus(t *testing.T, baseURL string, condition func(map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/status")
		if err == nil {
			var status map[string]any
			_ = json.NewDecoder(response.Body).Decode(&status)
			response.Body.Close()
			if condition(status) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("status condition did not become true")
}

func toolCount(status map[string]any) int {
	aggregate, _ := status["aggregate"].(map[string]any)
	count, _ := aggregate["tool_count"].(float64)
	return int(count)
}

func toolText(response map[string]any) string {
	result, _ := response["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

func assertPublicError(t *testing.T, response publicObservation, code int) {
	t.Helper()
	errorValue, _ := response.JSON["error"].(map[string]any)
	if response.Status != http.StatusOK || errorValue == nil || int(errorValue["code"].(float64)) != code {
		t.Fatalf("response=%+v want code=%d", response, code)
	}
}

func assertAuditEvents(t *testing.T, path string, want ...string) {
	t.Helper()
	got := readAudit(t, path)
	events := make([]string, 0, len(got))
	for _, event := range got {
		events = append(events, event.Event)
	}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("audit events=%v want=%v", events, want)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
