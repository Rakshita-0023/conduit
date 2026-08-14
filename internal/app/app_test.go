package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conduit-mcp/conduit/internal/config"
)

func downstream(t *testing.T, block <-chan struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
}
func cfg(path, url string) config.Config {
	return config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}, Audit: config.Audit{Path: path}, Limits: config.Limits{MaxPagesPerDownstream: 2, MaxToolsPerDownstream: 2, MaxDownstreamCatalogBytes: 1024, RequestTimeout: 50 * time.Millisecond}, Servers: []config.Downstream{{ID: "x", URL: url}}}
}
func TestStartIsLiveBeforeInitialRefreshCompletes(t *testing.T) {
	block := make(chan struct{})
	s := downstream(t, block)
	defer s.Close()
	a, e := Start(context.Background(), cfg(filepath.Join(t.TempDir(), "audit"), s.URL), "test")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close(context.Background())
	if a.Health.Snapshot().Ready {
		t.Fatal("unexpected ready")
	}
	close(block)
}
func TestAuditUnavailable(t *testing.T) {
	c := cfg(t.TempDir(), "http://127.0.0.1:1")
	if _, e := Start(context.Background(), c, "test"); e == nil {
		t.Fatal("want audit error")
	}
}

func TestOneHealthyOneFailedAndSanitizedStatus(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "server/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"resultType": "complete", "tools": []any{}}})
	}))
	defer good.Close()
	c := cfg(filepath.Join(t.TempDir(), "audit"), good.URL)
	c.Servers = append(c.Servers, config.Downstream{ID: "bad", URL: "http://127.0.0.1:1", Headers: map[string]string{"Authorization": "Bearer secret-value"}})
	a, err := Start(context.Background(), c, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for !a.Health.Snapshot().Ready && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st := a.Health.Snapshot()
	if !st.Ready {
		t.Fatalf("status=%+v", st)
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "secret-value") {
		t.Fatalf("secret leaked: %s", b)
	}
}

func TestAllDownstreamsFailAfterStart(t *testing.T) {
	a, err := Start(context.Background(), cfg(filepath.Join(t.TempDir(), "audit"), "http://127.0.0.1:1"), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	deadline := time.Now().Add(time.Second)
	for a.Health.Snapshot().Downstreams[0].State == "starting" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := a.Health.Snapshot()
	if status.Ready || status.Downstreams[0].State != "degraded" {
		t.Fatalf("status=%+v", status)
	}
}

func TestStartRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Start(ctx, cfg(filepath.Join(t.TempDir(), "audit"), "http://127.0.0.1:1"), "test"); err == nil {
		t.Fatal("want canceled startup error")
	}
}

func TestCloseCancelsRefreshAndReadiness(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer s.Close()
	a, err := Start(context.Background(), cfg(filepath.Join(t.TempDir(), "audit"), s.URL), "test")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := a.Health.Snapshot()
	if status.Live || status.Ready {
		t.Fatalf("status=%+v", status)
	}
	close(release)
}

func TestStartFailsWhenListenerCannotBind(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := cfg(filepath.Join(t.TempDir(), "audit"), "http://127.0.0.1:1")
	c.Listener.Address = listener.Addr().String()
	if _, err := Start(context.Background(), c, "test"); err == nil {
		t.Fatal("want bind error")
	}
}

func TestToolCallNeverReachesDownstream(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		mu.Lock()
		methods = append(methods, request.Method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "server/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "tools": []any{}}})
	}))
	defer downstream.Close()
	a, err := Start(context.Background(), cfg(filepath.Join(t.TempDir(), "audit"), downstream.URL), "test")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Server.Serve(a.Listener) }()
	defer func() {
		if err := a.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for !a.Health.Snapshot().Ready && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !a.Health.Snapshot().Ready {
		t.Fatal("not ready")
	}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"public.tool","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+a.Listener.Addr().String()+"/mcp", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "public.tool")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("unexpected tools/call status %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(methods, ",") != "server/discover,tools/list" {
		t.Fatalf("downstream methods=%v", methods)
	}
}
