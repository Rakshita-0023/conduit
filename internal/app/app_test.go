package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/health"
	"github.com/conduit-mcp/conduit/internal/ingress"
	"github.com/conduit-mcp/conduit/internal/policy"
	"github.com/conduit-mcp/conduit/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func downstream(t *testing.T, block <-chan struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
}
func cfg(path, url string) config.Config {
	return config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}, Audit: config.Audit{Path: path}, Limits: config.Limits{MaxPagesPerDownstream: 2, MaxToolsPerDownstream: 2, MaxDownstreamCatalogBytes: 1024, MaxAggregateTools: 2, MaxAggregateResponseBytes: 1024, CatalogRefreshInterval: time.Hour, RequestTimeout: 50 * time.Millisecond}, Servers: []config.Downstream{{ID: "x", URL: url}}}
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
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"resultType": "complete", "tools": []any{map[string]any{"name": "visible", "inputSchema": map[string]any{"type": "object"}}}}})
	}))
	defer good.Close()
	c := cfg(filepath.Join(t.TempDir(), "audit"), good.URL)
	c.Policy.Allow = []string{"x.*"}
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
	if st.Aggregate.State != "ready" || st.Aggregate.ToolCount != 1 || !strings.Contains(string(a.Registry.Snapshot().ResultJSON()), "x.visible") {
		t.Fatalf("aggregate=%+v result=%s", st.Aggregate, a.Registry.Snapshot().ResultJSON())
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
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "tools": []any{map[string]any{"name": "visible", "inputSchema": map[string]any{"type": "object"}}}}})
	}))
	defer downstream.Close()
	c := cfg(filepath.Join(t.TempDir(), "audit"), downstream.URL)
	c.Policy.Allow = []string{"x.*"}
	a, err := Start(context.Background(), c, "test")
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
	listBody := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	listRequest, err := http.NewRequest(http.MethodPost, "http://"+a.Listener.Addr().String()+"/mcp", strings.NewReader(string(listBody)))
	if err != nil {
		t.Fatal(err)
	}
	listRequest.Header.Set("Content-Type", "application/json")
	listRequest.Header.Set("Accept", "application/json, text/event-stream")
	listRequest.Header.Set("MCP-Protocol-Version", "2026-07-28")
	listRequest.Header.Set("Mcp-Method", "tools/list")
	listResponse, err := http.DefaultClient.Do(listRequest)
	if err != nil {
		t.Fatal(err)
	}
	listResponseBody, _ := io.ReadAll(listResponse.Body)
	listResponse.Body.Close()
	if listResponse.StatusCode != http.StatusOK || !strings.Contains(string(listResponseBody), "x.visible") {
		t.Fatalf("tools/list status=%d body=%s", listResponse.StatusCode, listResponseBody)
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

func TestPeriodicRefreshRemovesStaleCatalogAndRecovers(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "server/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
			return
		}
		if !healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "tools": []any{map[string]any{"name": "visible", "inputSchema": map[string]any{"type": "object"}}}}})
	}))
	defer downstream.Close()
	c := cfg(filepath.Join(t.TempDir(), "audit"), downstream.URL)
	c.Policy.Allow = []string{"x.*"}
	c.Limits.CatalogRefreshInterval = 15 * time.Millisecond
	c.Limits.RequestTimeout = 100 * time.Millisecond
	a, err := Start(context.Background(), c, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	waitFor(t, time.Second, func() bool { return a.Health.Snapshot().Ready && a.Health.Snapshot().Aggregate.ToolCount == 1 })
	healthy.Store(false)
	waitFor(t, time.Second, func() bool {
		status := a.Health.Snapshot()
		return !status.Ready && status.Aggregate.State == "unavailable" && status.Aggregate.ToolCount == 0 && status.Downstreams[0].State == "degraded"
	})
	healthy.Store(true)
	waitFor(t, time.Second, func() bool { return a.Health.Snapshot().Ready && a.Health.Snapshot().Aggregate.ToolCount == 1 })
}

func TestPeriodicRefreshDoesNotOverlapAndStops(t *testing.T) {
	var inFlight, maxInFlight, lists atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "server/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
			return
		}
		current := inFlight.Add(1)
		for {
			seen := maxInFlight.Load()
			if current <= seen || maxInFlight.CompareAndSwap(seen, current) {
				break
			}
		}
		lists.Add(1)
		time.Sleep(25 * time.Millisecond)
		inFlight.Add(-1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "tools": []any{}}})
	}))
	defer downstream.Close()
	c := cfg(filepath.Join(t.TempDir(), "audit"), downstream.URL)
	c.Limits.CatalogRefreshInterval = 5 * time.Millisecond
	c.Limits.RequestTimeout = 200 * time.Millisecond
	a, err := Start(context.Background(), c, "test")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return lists.Load() >= 2 })
	if maxInFlight.Load() != 1 {
		t.Fatalf("overlapping refreshes=%d", maxInFlight.Load())
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	afterClose := lists.Load()
	time.Sleep(40 * time.Millisecond)
	if lists.Load() != afterClose {
		t.Fatalf("refresh continued after close: %d -> %d", afterClose, lists.Load())
	}
}

func TestInitialRefreshRunsExactlyOnceBeforeTicker(t *testing.T) {
	var lists atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "server/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
			return
		}
		lists.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "tools": []any{}}})
	}))
	defer downstream.Close()
	c := cfg(filepath.Join(t.TempDir(), "audit"), downstream.URL)
	c.Limits.CatalogRefreshInterval = 250 * time.Millisecond
	a, err := Start(context.Background(), c, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	waitFor(t, time.Second, func() bool { return lists.Load() == 1 })
	time.Sleep(25 * time.Millisecond)
	if lists.Load() != 1 {
		t.Fatalf("initial tools/list attempts=%d", lists.Load())
	}
}

func TestStaleRefreshHealthCannotOverrideNewerCollision(t *testing.T) {
	compiled, err := policy.Compile(config.Policy{Allow: []string{"a.*"}})
	if err != nil {
		t.Fatal(err)
	}
	r := registry.New(config.Limits{MaxAggregateTools: 8, MaxAggregateResponseBytes: 1 << 20}, compiled, mcp.Implementation{Name: "conduit", Version: "test"})
	first, err := r.Publish(registry.Catalog{ServerID: "a.b", Tools: []*mcp.Tool{{Name: "c", InputSchema: map[string]any{"type": "object"}}}})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := r.Publish(registry.Catalog{ServerID: "a", Tools: []*mcp.Tool{{Name: "b.c", InputSchema: map[string]any{"type": "object"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if latest.State != registry.StateCollision || latest.Generation <= first.Generation {
		t.Fatalf("snapshots=%+v / %+v", first, latest)
	}

	// This is the previous out-of-order completion: refresh A has published a
	// ready registry snapshot but delays its health write until after refresh B
	// publishes the collision.
	h := health.New([]string{"a.b", "a"})
	h.SetLive(true)
	h.SetServer("a.b", "healthy", 1, "")
	h.SetServer("a", "healthy", 1, "")
	h.SetAggregate(latest.Generation, string(latest.State), latest.ToolCount)
	h.SetAggregate(first.Generation, string(first.State), first.ToolCount)
	status := h.Snapshot()
	if status.Aggregate.Generation != latest.Generation || status.Aggregate.State != string(latest.State) || status.Ready {
		t.Fatalf("status=%+v latest=%+v", status, latest)
	}

	server := ingress.New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, h, r, "test")
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "server/discover")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("discovery status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConcurrentRefreshPublicationEndsAtNewestCollision(t *testing.T) {
	newDownstream := func(toolName string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			w.Header().Set("Content-Type", "application/json")
			if request.Method == "server/discover" {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"resultType": "complete", "tools": []any{map[string]any{"name": toolName, "inputSchema": map[string]any{"type": "object"}}}}})
		}))
	}
	first := newDownstream("c")
	defer first.Close()
	second := newDownstream("b.c")
	defer second.Close()

	limits := config.Limits{MaxPagesPerDownstream: 2, MaxToolsPerDownstream: 2, MaxDownstreamCatalogBytes: 1024, MaxAggregateTools: 8, MaxAggregateResponseBytes: 1024, CatalogRefreshInterval: time.Hour, RequestTimeout: time.Second}
	compiled, err := policy.Compile(config.Policy{Allow: []string{"a.*"}})
	if err != nil {
		t.Fatal(err)
	}
	r := registry.New(limits, compiled, mcp.Implementation{Name: "conduit", Version: "test"})
	h := health.New([]string{"a.b", "a"})
	h.SetLive(true)
	app := &App{
		Config:   config.Config{Limits: limits},
		Health:   h,
		Registry: r,
	}

	firstLocked := make(chan struct{})
	secondBeforeLock := make(chan struct{})
	releaseFirst := make(chan struct{})
	var beforeCalls, afterCalls atomic.Int32
	app.testBeforePublicationLock = func() {
		switch beforeCalls.Add(1) {
		case 2:
			close(secondBeforeLock)
		}
	}
	app.testAfterPublicationLock = func() {
		if afterCalls.Add(1) == 1 {
			close(firstLocked)
			<-releaseFirst
		}
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		app.refreshOnce(context.Background(), config.Downstream{ID: "a.b", URL: first.URL})
	}()
	select {
	case <-firstLocked:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not acquire publication lock")
	}

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		app.refreshOnce(context.Background(), config.Downstream{ID: "a", URL: second.URL})
	}()
	select {
	case <-secondBeforeLock:
	case <-time.After(time.Second):
		t.Fatal("second refresh did not reach publication lock")
	}
	if got := afterCalls.Load(); got != 1 {
		t.Fatalf("second refresh bypassed publication mutex: hooks=%d", got)
	}

	close(releaseFirst)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not complete")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second refresh did not complete")
	}

	snapshot := r.Snapshot()
	status := h.Snapshot()
	if snapshot.State != registry.StateCollision || status.Aggregate.Generation != snapshot.Generation || status.Aggregate.State != string(snapshot.State) || status.Ready {
		t.Fatalf("snapshot=%+v status=%+v", snapshot, status)
	}

	server := ingress.New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, h, r, "test")
	healthResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(healthResponse, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if healthResponse.Code != http.StatusOK || !bytes.Contains(healthResponse.Body.Bytes(), []byte(`"ready":false`)) {
		t.Fatalf("health response=%d %s", healthResponse.Code, healthResponse.Body.String())
	}
	discoverBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	discover := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(discoverBody))
	discover.Header.Set("Content-Type", "application/json")
	discover.Header.Set("Accept", "application/json, text/event-stream")
	discover.Header.Set("MCP-Protocol-Version", "2026-07-28")
	discover.Header.Set("Mcp-Method", "server/discover")
	discoverResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(discoverResponse, discover)
	if discoverResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("discovery status=%d body=%s", discoverResponse.Code, discoverResponse.Body.String())
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met")
}
