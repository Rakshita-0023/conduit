// Package parity_test exercises the current Go service through its public HTTP
// boundary. Its normalized observations are migration fixtures, not a second
// production implementation.
package parity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rakshita-0023/conduit/internal/app"
	"github.com/Rakshita-0023/conduit/internal/config"
)

const protocolVersion = "2026-07-28"

type scenario struct {
	Name        string               `json:"name"`
	Downstreams []scenarioDownstream `json:"downstreams"`
	Policy      struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"policy"`
	Limits struct {
		MaxAggregateTools     int   `json:"max_aggregate_tools"`
		MaxToolResponseBytes  int64 `json:"max_tool_response_bytes"`
		MaxAggregateRespBytes int64 `json:"max_aggregate_response_bytes"`
	} `json:"limits"`
	Call   *scenarioCall       `json:"call"`
	Expect scenarioExpectation `json:"expect"`
}

type scenarioDownstream struct {
	ID      string            `json:"id"`
	Headers map[string]string `json:"headers"`
	Tools   []map[string]any  `json:"tools"`
	Call    scenarioReply     `json:"call_response"`
}

type scenarioCall struct {
	Name          string            `json:"name"`
	Arguments     map[string]any    `json:"arguments"`
	CallerHeaders map[string]string `json:"caller_headers"`
}

type scenarioReply struct {
	Kind      string          `json:"kind"`
	Status    int             `json:"status"`
	Result    json.RawMessage `json:"result"`
	Error     *wireError      `json:"error"`
	SessionID string          `json:"session_id"`
}

type wireError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type scenarioExpectation struct {
	Ready       bool                 `json:"ready"`
	Tools       []string             `json:"tools"`
	Call        *expectedCall        `json:"call"`
	Downstream  []expectedDownstream `json:"downstream"`
	AuditEvents []string             `json:"audit_events"`
}

type expectedCall struct {
	Status       int             `json:"status"`
	ErrorCode    *int            `json:"error_code"`
	ErrorMessage string          `json:"error_message"`
	Result       json.RawMessage `json:"result"`
	ErrorData    json.RawMessage `json:"error_data"`
}

type expectedDownstream struct {
	ID        string            `json:"id"`
	Method    string            `json:"method"`
	MCPMethod string            `json:"mcp_method"`
	MCPName   string            `json:"mcp_name"`
	SessionID string            `json:"session_id"`
	Headers   map[string]string `json:"headers"`
	Count     int               `json:"count"`
	ToolName  string            `json:"tool_name"`
}

// observation is the stable result format that a future Python implementation
// will be compared with after applying the same normalization.
type observation struct {
	Public     []publicObservation            `json:"public"`
	Downstream map[string][]downstreamRequest `json:"downstream"`
	Audit      []auditObservation             `json:"audit"`
	Status     map[string]any                 `json:"status"`
}

type publicObservation struct {
	Operation string         `json:"operation"`
	Status    int            `json:"status"`
	JSON      map[string]any `json:"json,omitempty"`
}

type downstreamRequest struct {
	Method    string            `json:"method"`
	MCPMethod string            `json:"mcp_method,omitempty"`
	MCPName   string            `json:"mcp_name,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      map[string]any    `json:"body,omitempty"`
}

type auditObservation struct {
	Event    string `json:"event"`
	Outcome  string `json:"outcome,omitempty"`
	CallID   string `json:"call_id,omitempty"`
	Tool     string `json:"public_tool,omitempty"`
	ServerID string `json:"server_id,omitempty"`
	ToolName string `json:"downstream_tool_name,omitempty"`
}

type mockDownstream struct {
	definition scenarioDownstream
	server     *httptest.Server
	mu         sync.Mutex
	records    []downstreamRequest
}

func newMockDownstream(def scenarioDownstream) *mockDownstream {
	m := &mockDownstream{definition: def}
	m.server = newLoopbackServer(http.HandlerFunc(m.serveHTTP))
	return m
}

func newLoopbackServer(handler http.Handler) *httptest.Server {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	// NewUnstartedServer creates its own listener before callers can replace it.
	// Build the test server directly so every oracle server is explicitly IPv4
	// loopback, including on hosts where httptest chooses an unavailable IPv6
	// listener.
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	return server
}

func (m *mockDownstream) Close()      { m.server.Close() }
func (m *mockDownstream) URL() string { return m.server.URL }

func (m *mockDownstream) serveHTTP(w http.ResponseWriter, r *http.Request) {
	record := downstreamRequest{
		Method: r.Method, MCPMethod: r.Header.Get("Mcp-Method"),
		MCPName: r.Header.Get("Mcp-Name"), SessionID: r.Header.Get("Mcp-Session-Id"),
		Headers: selectedHeaders(r.Header),
	}
	if r.Method == http.MethodDelete {
		m.record(record)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var request map[string]any
	_ = json.NewDecoder(r.Body).Decode(&request)
	record.Body = normalizeDownstreamBody(request)
	m.record(record)
	id := request["id"]
	method, _ := request["method"].(string)
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "server/discover":
		writeRPC(w, http.StatusOK, id, map[string]any{
			"resultType": "complete", "supportedVersions": []string{protocolVersion},
			"capabilities": map[string]any{"tools": map[string]any{}},
		})
	case "tools/list":
		writeRPC(w, http.StatusOK, id, map[string]any{"resultType": "complete", "tools": m.definition.Tools})
	case "tools/call":
		m.writeCallReply(w, id)
	default:
		writeRPCError(w, http.StatusBadRequest, id, -32601, "method not found", nil)
	}
}

func (m *mockDownstream) writeCallReply(w http.ResponseWriter, id any) {
	reply := m.definition.Call
	status := reply.Status
	if status == 0 {
		status = http.StatusOK
	}
	if reply.SessionID != "" {
		w.Header().Set("Mcp-Session-Id", reply.SessionID)
	}
	switch reply.Kind {
	case "malformed":
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{not json"))
	case "sse":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("event: message\ndata: ignored\n\n"))
	case "error":
		writeRPCError(w, status, id, reply.Error.Code, reply.Error.Message, reply.Error.Data)
	default:
		var result any
		_ = json.Unmarshal(reply.Result, &result)
		writeRPC(w, status, id, result)
	}
}

func (m *mockDownstream) record(record downstreamRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, record)
}

func (m *mockDownstream) recordsSnapshot() []downstreamRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]downstreamRequest(nil), m.records...)
}

func TestGoOracleFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "parity", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no parity fixtures")
	}
	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fixture scenario
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			got := runScenario(t, fixture)
			assertScenario(t, fixture, got)
		})
	}
}

func runScenario(t *testing.T, fixture scenario) observation {
	t.Helper()
	mocks := make(map[string]*mockDownstream, len(fixture.Downstreams))
	for _, downstream := range fixture.Downstreams {
		mocks[downstream.ID] = newMockDownstream(downstream)
	}
	defer func() {
		for _, mock := range mocks {
			mock.Close()
		}
	}()

	limits := config.Limits{
		MaxPagesPerDownstream: 4, MaxToolsPerDownstream: 16,
		MaxDownstreamCatalogBytes: 1 << 20, MaxAggregateTools: 16,
		MaxAggregateResponseBytes: 1 << 20, MaxToolResponseBytes: 1 << 20,
		CatalogRefreshInterval: time.Hour, RequestTimeout: time.Second, ToolCallTimeout: time.Second,
	}
	if fixture.Limits.MaxAggregateTools != 0 {
		limits.MaxAggregateTools = fixture.Limits.MaxAggregateTools
	}
	if fixture.Limits.MaxToolResponseBytes != 0 {
		limits.MaxToolResponseBytes = fixture.Limits.MaxToolResponseBytes
	}
	if fixture.Limits.MaxAggregateRespBytes != 0 {
		limits.MaxAggregateResponseBytes = fixture.Limits.MaxAggregateRespBytes
	}
	cfg := config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Audit:    config.Audit{Path: filepath.Join(t.TempDir(), "audit.jsonl")},
		Policy:   config.Policy{Allow: fixture.Policy.Allow, Deny: fixture.Policy.Deny},
		Limits:   limits,
	}
	for _, downstream := range fixture.Downstreams {
		cfg.Servers = append(cfg.Servers, config.Downstream{
			ID: downstream.ID, URL: mocks[downstream.ID].URL(), Headers: downstream.Headers,
		})
	}
	instance, err := app.Start(context.Background(), cfg, "parity-go")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- instance.Server.Serve(instance.Listener) }()
	closed := false
	defer func() {
		if closed {
			return
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := <-serveErr; !isExpectedServerClose(err) {
			t.Error(err)
		}
	}()

	baseURL := "http://" + instance.Listener.Addr().String()
	status := waitForInitialRefresh(t, baseURL, len(fixture.Downstreams))
	out := observation{Downstream: map[string][]downstreamRequest{}, Status: normalizeStatus(status)}
	out.Public = append(out.Public, publicMCP(t, baseURL, 99, "server/discover", map[string]any{}))
	out.Public = append(out.Public, publicMCP(t, baseURL, 100, "tools/list", map[string]any{}))
	if fixture.Call != nil {
		params := map[string]any{"name": fixture.Call.Name, "arguments": fixture.Call.Arguments}
		out.Public = append(out.Public, publicMCPWithHeaders(t, baseURL, 101, "tools/call", params, fixture.Call.Name, fixture.Call.CallerHeaders))
	}
	for id, mock := range mocks {
		out.Downstream[id] = mock.recordsSnapshot()
	}
	if err := instance.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; !isExpectedServerClose(err) {
		t.Fatal(err)
	}
	closed = true
	out.Audit = readAudit(t, cfg.Audit.Path)
	return out
}

func isExpectedServerClose(err error) bool {
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// http.Server can return this platform-specific listener-close error when
	// direct listener closure wins the race with Shutdown marking the server as
	// closed. The oracle controls that listener and has already completed a
	// successful App.Close, so it is a normal test-server teardown outcome.
	return strings.Contains(err.Error(), "use of closed network connection")
}

func waitForInitialRefresh(t *testing.T, baseURL string, downstreams int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/status")
		if err == nil {
			var status map[string]any
			_ = json.NewDecoder(response.Body).Decode(&status)
			response.Body.Close()
			entries, _ := status["downstreams"].([]any)
			if len(entries) == downstreams && allAttempted(entries) {
				return status
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("initial refresh did not complete")
	return nil
}

func allAttempted(entries []any) bool {
	for _, entry := range entries {
		value, _ := entry.(map[string]any)
		if value["state"] == "starting" {
			return false
		}
	}
	return true
}

func publicMCP(t *testing.T, baseURL string, id int, method string, params map[string]any) publicObservation {
	name := ""
	if method == "tools/call" {
		name, _ = params["name"].(string)
	}
	return publicMCPWithHeaders(t, baseURL, id, method, params, name, nil)
}

func publicMCPWithHeaders(t *testing.T, baseURL string, id int, method string, params map[string]any, name string, extra map[string]string) publicObservation {
	t.Helper()
	out, err := sendPublicMCP(baseURL, id, method, params, name, extra)
	if err != nil {
		t.Fatal(err)
	}
	if out.JSON != nil && out.Status == http.StatusOK && out.JSON["id"] != float64(id) {
		t.Fatalf("response ID=%v want=%d", out.JSON["id"], id)
	}
	return out
}

func sendPublicMCP(baseURL string, id int, method string, params map[string]any, name string, extra map[string]string) (publicObservation, error) {
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    protocolVersion,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return publicObservation{}, err
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return publicObservation{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", protocolVersion)
	request.Header.Set("Mcp-Method", method)
	if name != "" {
		request.Header.Set("Mcp-Name", name)
	}
	for key, value := range extra {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return publicObservation{}, err
	}
	defer response.Body.Close()
	out := publicObservation{Operation: method, Status: response.StatusCode}
	if strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(response.Body).Decode(&out.JSON); err != nil {
			return publicObservation{}, err
		}
	}
	return out, nil
}

func assertScenario(t *testing.T, fixture scenario, got observation) {
	t.Helper()
	if got.Status["ready"] != fixture.Expect.Ready {
		t.Fatalf("ready=%v want=%v observation=%s", got.Status["ready"], fixture.Expect.Ready, encodeObservation(t, got))
	}
	discover := got.Public[0]
	if fixture.Expect.Ready {
		result, _ := discover.JSON["result"].(map[string]any)
		versions, _ := result["supportedVersions"].([]any)
		if discover.Status != http.StatusOK || len(versions) != 1 || versions[0] != protocolVersion {
			t.Fatalf("discover=%+v", discover)
		}
	}
	tools := toolNames(got.Public[1].JSON)
	if strings.Join(tools, ",") != strings.Join(fixture.Expect.Tools, ",") {
		t.Fatalf("tools=%v want=%v", tools, fixture.Expect.Tools)
	}
	if fixture.Expect.Call != nil {
		if len(got.Public) != 3 {
			t.Fatalf("public=%s", encodeObservation(t, got))
		}
		assertCall(t, got.Public[2], *fixture.Expect.Call)
	}
	for _, want := range fixture.Expect.Downstream {
		assertDownstream(t, got.Downstream[want.ID], want)
	}
	var events []string
	for _, event := range got.Audit {
		events = append(events, event.Event)
	}
	if strings.Join(events, ",") != strings.Join(fixture.Expect.AuditEvents, ",") {
		t.Fatalf("audit events=%v want=%v", events, fixture.Expect.AuditEvents)
	}
}

func assertCall(t *testing.T, got publicObservation, want expectedCall) {
	t.Helper()
	if got.Status != want.Status {
		t.Fatalf("call status=%d want=%d body=%v", got.Status, want.Status, got.JSON)
	}
	if want.ErrorCode != nil {
		errorValue, _ := got.JSON["error"].(map[string]any)
		if errorValue == nil || int(errorValue["code"].(float64)) != *want.ErrorCode {
			t.Fatalf("call error=%v want=%d", got.JSON, *want.ErrorCode)
		}
		if want.ErrorMessage != "" && errorValue["message"] != want.ErrorMessage {
			t.Fatalf("error message=%q want=%q", errorValue["message"], want.ErrorMessage)
		}
		if len(want.ErrorData) > 0 && !equivalentJSON(t, errorValue["data"], want.ErrorData) {
			t.Fatalf("error data=%s want=%s", normalJSON(t, errorValue["data"]), want.ErrorData)
		}
		return
	}
	if !equivalentJSON(t, got.JSON["result"], want.Result) {
		t.Fatalf("result=%s want=%s", normalJSON(t, got.JSON["result"]), want.Result)
	}
}

func assertDownstream(t *testing.T, records []downstreamRequest, want expectedDownstream) {
	t.Helper()
	var matching []downstreamRequest
	for _, record := range records {
		if record.Method == want.Method && (want.MCPMethod == "" || record.MCPMethod == want.MCPMethod) {
			matching = append(matching, record)
		}
	}
	if len(matching) != want.Count {
		t.Fatalf("downstream %s %s count=%d want=%d records=%v", want.ID, want.Method, len(matching), want.Count, records)
	}
	for _, record := range matching {
		if want.MCPName != "" && record.MCPName != want.MCPName {
			t.Fatalf("mcp name=%q want=%q", record.MCPName, want.MCPName)
		}
		if want.SessionID != "" && record.SessionID != want.SessionID {
			t.Fatalf("session=%q want=%q", record.SessionID, want.SessionID)
		}
		for key, value := range want.Headers {
			if record.Headers[key] != value {
				t.Fatalf("header %s=%q want=%q all=%v", key, record.Headers[key], value, record.Headers)
			}
		}
		if want.ToolName != "" {
			params, _ := record.Body["params"].(map[string]any)
			if params["name"] != want.ToolName {
				t.Fatalf("downstream params=%v want tool=%q", params, want.ToolName)
			}
		}
	}
}

func writeRPC(w http.ResponseWriter, status int, id, result any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, status int, id any, code int, message string, data json.RawMessage) {
	errorValue := map[string]any{"code": code, "message": message}
	if len(data) > 0 {
		var decoded any
		_ = json.Unmarshal(data, &decoded)
		errorValue["data"] = decoded
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": errorValue})
}

func selectedHeaders(header http.Header) map[string]string {
	out := map[string]string{}
	for _, key := range []string{"MCP-Protocol-Version", "Mcp-Method", "Mcp-Name", "Mcp-Session-Id", "Authorization", "Cookie", "X-Configured", "X-Caller"} {
		if values := header.Values(key); len(values) > 0 {
			out[key] = strings.Join(values, ",")
		}
	}
	return out
}

func normalizeDownstreamBody(body map[string]any) map[string]any {
	copy := cloneObject(body)
	if _, ok := copy["id"]; ok {
		copy["id"] = "$DOWNSTREAM_REQUEST_ID"
	}
	if params, ok := copy["params"].(map[string]any); ok {
		if meta, ok := params["_meta"].(map[string]any); ok {
			if info, ok := meta["io.modelcontextprotocol/clientInfo"].(map[string]any); ok {
				info["version"] = "$BUILD_VERSION"
			}
		}
	}
	return copy
}

func normalizeStatus(status map[string]any) map[string]any {
	copy := cloneObject(status)
	entries, _ := copy["downstreams"].([]any)
	for _, entry := range entries {
		if value, ok := entry.(map[string]any); ok {
			delete(value, "last_success")
		}
	}
	return copy
}

func cloneObject(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var copy map[string]any
	_ = json.Unmarshal(encoded, &copy)
	return copy
}

func readAudit(t *testing.T, path string) []auditObservation {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []auditObservation
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatal(err)
		}
		out = append(out, auditObservation{
			Event: stringValue(raw["event"]), Outcome: stringValue(raw["outcome"]),
			CallID: normalizeCallID(stringValue(raw["call_id"])), Tool: stringValue(raw["public_tool"]),
			ServerID: stringValue(raw["server_id"]), ToolName: stringValue(raw["downstream_tool_name"]),
		})
	}
	return out
}

func normalizeCallID(value string) string {
	if value == "" {
		return ""
	}
	return "$CALL_ID"
}

func stringValue(value any) string {
	stringValue, _ := value.(string)
	return stringValue
}

func toolNames(value map[string]any) []string {
	result, _ := value["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if value, ok := tool.(map[string]any); ok {
			out = append(out, stringValue(value["name"]))
		}
	}
	return out
}

func normalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func equivalentJSON(t *testing.T, actual any, expected json.RawMessage) bool {
	t.Helper()
	var expectedValue any
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(actual, expectedValue)
}

func encodeObservation(t *testing.T, value observation) string {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
