package ingress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/dispatch"
	"github.com/conduit-mcp/conduit/internal/health"
	"github.com/conduit-mcp/conduit/internal/mcpheaders"
	"github.com/conduit-mcp/conduit/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "2026-07-28"

type Server struct {
	handler    http.Handler
	health     *health.State
	dispatcher *dispatch.Dispatcher
}

func New(cfg config.Config, state *health.State, catalogRegistry *registry.Registry, buildVersion string, dispatchers ...*dispatch.Dispatcher) *Server {
	var toolDispatcher *dispatch.Dispatcher
	if len(dispatchers) > 0 {
		toolDispatcher = dispatchers[0]
	}
	caps := &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "conduit", Version: buildVersion}, &mcp.ServerOptions{Capabilities: caps})
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, PropagateRequestCancellation: true})
	return &Server{health: state, handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, state.Snapshot())
			return
		}
		if r.URL.Path == "/status" {
			writeJSON(w, http.StatusOK, state.Snapshot())
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if !allowedOrigin(r.Header.Get("Origin"), cfg.Listener.AllowedOrigins) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		request, err := validate2026(r)
		if err != nil {
			http.Error(w, "invalid MCP request", http.StatusBadRequest)
			return
		}
		if !supportedMethod(request.Method) {
			if !sdkTransportAdmits(w, r, sdk) {
				return
			}
			writeMethodNotFound(w, request.ID)
			return
		}
		if request.Method == "server/discover" {
			if !state.Snapshot().Ready {
				http.Error(w, "conduit not ready", http.StatusServiceUnavailable)
				return
			}
			serveStrictDiscover(w, r, sdk)
			return
		}
		if request.Method == "tools/list" {
			serveToolsList(w, r, request, sdk, state, catalogRegistry)
			return
		}
		serveToolsCall(w, r, request, sdk, state, catalogRegistry, toolDispatcher)
	})}
}
func (s *Server) Handler() http.Handler { return s.handler }
func HTTPServer(addr string, h http.Handler, toolTimeout ...time.Duration) *http.Server {
	writeTimeout := 15 * time.Second
	if len(toolTimeout) > 0 && toolTimeout[0] > 0 {
		writeTimeout = saturatingDurationAdd(toolTimeout[0], 5*time.Second)
	}
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: writeTimeout, IdleTimeout: 30 * time.Second}
}

func saturatingDurationAdd(a, b time.Duration) time.Duration {
	if a > time.Duration(1<<63-1)-b {
		return time.Duration(1<<63 - 1)
	}
	return a + b
}
func allowedOrigin(origin string, allowed []string) bool {
	if origin == "" {
		return true
	}
	if origin == "null" {
		return false
	}
	for _, v := range allowed {
		if origin == v {
			return true
		}
	}
	return false
}

type requestInfo struct {
	ID     json.RawMessage
	Method string
	Cursor string
	Params json.RawMessage
}

func validate2026(r *http.Request) (requestInfo, error) {
	b, err := ioRead(r, 1<<20)
	if err != nil {
		return requestInfo{}, err
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(b))
	var v struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if json.Unmarshal(b, &v) != nil || v.JSONRPC != "2.0" || len(v.ID) == 0 || v.Method == "" {
		return requestInfo{}, errInvalid
	}
	var params struct {
		Meta   map[string]any `json:"_meta"`
		Cursor string         `json:"cursor"`
	}
	if json.Unmarshal(v.Params, &params) != nil {
		return requestInfo{}, errInvalid
	}
	if err := validRequestID(v.ID); err != nil {
		return requestInfo{}, errInvalid
	}
	if r.Header.Get("MCP-Protocol-Version") != Version || r.Header.Get("Mcp-Method") != v.Method {
		return requestInfo{}, errInvalid
	}
	meta := params.Meta
	if meta == nil {
		return requestInfo{}, errInvalid
	}
	pv, ok := meta["io.modelcontextprotocol/protocolVersion"].(string)
	if !ok || pv != Version {
		return requestInfo{}, errInvalid
	}
	if _, ok := meta["io.modelcontextprotocol/clientCapabilities"]; !ok {
		return requestInfo{}, errInvalid
	}
	return requestInfo{ID: v.ID, Method: v.Method, Cursor: params.Cursor, Params: v.Params}, nil
}

func serveToolsCall(w http.ResponseWriter, r *http.Request, request requestInfo, sdk http.Handler, state *health.State, catalogs *registry.Registry, toolDispatcher *dispatch.Dispatcher) {
	if !sdkTransportAdmits(w, r, sdk) {
		return
	}
	if catalogs == nil {
		http.Error(w, "conduit not ready", http.StatusServiceUnavailable)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(request.Params, &fields) != nil {
		http.Error(w, "invalid MCP request", http.StatusBadRequest)
		return
	}
	var name string
	if raw := fields["name"]; len(raw) == 0 || json.Unmarshal(raw, &name) != nil || name == "" {
		http.Error(w, "invalid MCP request", http.StatusBadRequest)
		return
	}
	args := fields["arguments"]
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if prepared, err := catalogs.PrepareExecution(name); err == nil {
		if err := mcpheaders.ValidateCall(r.Header, name, args, prepared.Tool); err != nil {
			http.Error(w, "invalid MCP request", http.StatusBadRequest)
			return
		}
	} else if r.Header.Get("Mcp-Name") != name {
		http.Error(w, "invalid MCP request", http.StatusBadRequest)
		return
	}
	if !state.Snapshot().Ready || toolDispatcher == nil {
		http.Error(w, "conduit not ready", http.StatusServiceUnavailable)
		return
	}
	if raw := fields["requestState"]; len(raw) > 0 {
		var state string
		if json.Unmarshal(raw, &state) != nil {
			http.Error(w, "invalid MCP request", http.StatusBadRequest)
			return
		}
	}
	response, err := toolDispatcher.Execute(r.Context(), dispatch.Call{PublicName: name, Arguments: args, InputResponses: fields["inputResponses"], RequestState: fields["requestState"]})
	if err != nil {
		if local, ok := err.(*dispatch.Error); ok {
			writeRPCError(w, http.StatusOK, request.ID, int(local.Code), local.Message)
			return
		}
		writeRPCError(w, http.StatusOK, request.ID, -32011, "tool dispatch failed")
		return
	}
	if response.Error != nil {
		writeRPCWireError(w, request.ID, response.Error)
		return
	}
	writeRPCResult(w, request.ID, response.Result)
}

func serveToolsList(w http.ResponseWriter, r *http.Request, request requestInfo, sdk http.Handler, state *health.State, catalogs *registry.Registry) {
	if !sdkValidatesToolsList(w, r, sdk) {
		return
	}
	if request.Cursor != "" {
		writeRPCError(w, http.StatusOK, request.ID, -32602, "conduit does not support tools/list pagination")
		return
	}
	if catalogs == nil {
		http.Error(w, "conduit not ready", http.StatusServiceUnavailable)
		return
	}
	snapshot := catalogs.Snapshot()
	if snapshot.State == registry.StateOverLimit {
		writeRPCError(w, http.StatusOK, request.ID, -32603, "conduit catalog limit exceeded")
		return
	}
	if snapshot.State != registry.StateReady {
		if snapshot.State == registry.StateCollision {
			writeRPCError(w, http.StatusOK, request.ID, -32603, "conduit catalog unavailable")
			return
		}
		http.Error(w, "conduit not ready", http.StatusServiceUnavailable)
		return
	}
	if !state.Snapshot().Ready {
		http.Error(w, "conduit not ready", http.StatusServiceUnavailable)
		return
	}
	writeRPCResult(w, request.ID, snapshot.ResultJSON())
}

// sdkValidatesToolsList keeps Streamable HTTP transport, MCP header, and
// typed request validation in the official SDK. The recorder's empty local
// tools/list result is discarded: Conduit's registry owns the actual result.
func sdkValidatesToolsList(w http.ResponseWriter, r *http.Request, sdk http.Handler) bool {
	body, err := ioRead(r, 1<<20)
	if err != nil {
		http.Error(w, "invalid MCP request", http.StatusBadRequest)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	probe := r.Clone(r.Context())
	probe.Header = r.Header.Clone()
	probe.Body = io.NopCloser(bytes.NewReader(body))
	probe.ContentLength = int64(len(body))
	recorder := httptest.NewRecorder()
	sdk.ServeHTTP(recorder, probe)
	if recorder.Code == http.StatusOK {
		return true
	}
	copyRecordedResponse(w, recorder)
	return false
}

// validRequestID preserves the request ID token for a manual JSON-RPC error.
func validRequestID(id json.RawMessage) error {
	id = bytes.TrimSpace(id)
	if bytes.Equal(id, []byte("null")) {
		return errInvalid
	}
	var stringID string
	if json.Unmarshal(id, &stringID) == nil {
		return nil
	}
	var numberID json.Number
	if json.Unmarshal(id, &numberID) == nil {
		return nil
	}
	return errInvalid
}

func supportedMethod(method string) bool {
	return method == "server/discover" || method == "tools/list" || method == "tools/call"
}

func writeMethodNotFound(w http.ResponseWriter, id json.RawMessage) {
	writeJSON(w, http.StatusNotFound, struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: -32601, Message: "method not found"}})
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":`))
	_, _ = w.Write(id)
	_, _ = w.Write([]byte(`,"result":`))
	_, _ = w.Write(result)
	_, _ = w.Write([]byte("}\n"))
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	writeJSON(w, status, struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}

func writeRPCWireError(w http.ResponseWriter, id json.RawMessage, err *jsonrpc.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":`))
	_, _ = w.Write(id)
	_, _ = w.Write([]byte(`,"error":{"code":`))
	_, _ = w.Write([]byte(strconv.FormatInt(err.Code, 10)))
	encoded, _ := json.Marshal(err.Message)
	_, _ = w.Write([]byte(`,"message":`))
	_, _ = w.Write(encoded)
	if len(err.Data) > 0 {
		_, _ = w.Write([]byte(`,"data":`))
		_, _ = w.Write(err.Data)
	}
	_, _ = w.Write([]byte("}}\n"))
}

var transportProbeBody = []byte(`{"jsonrpc":"2.0","id":"conduit-transport-probe","method":"conduit/transport-probe","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)

// sdkTransportAdmits delegates Streamable HTTP validation to the SDK without
// allowing the original unsupported method to reach a standard SDK handler.
func sdkTransportAdmits(w http.ResponseWriter, r *http.Request, sdk http.Handler) bool {
	probe := r.Clone(r.Context())
	probe.Header = r.Header.Clone()
	probe.Header.Set("Mcp-Method", "conduit/transport-probe")
	probe.Body = io.NopCloser(bytes.NewReader(transportProbeBody))
	probe.ContentLength = int64(len(transportProbeBody))
	recorder := httptest.NewRecorder()
	sdk.ServeHTTP(recorder, probe)
	if recorder.Code == http.StatusNotFound {
		return true
	}
	copyRecordedResponse(w, recorder)
	return false
}

func serveStrictDiscover(w http.ResponseWriter, r *http.Request, sdk http.Handler) {
	recorder := httptest.NewRecorder()
	sdk.ServeHTTP(recorder, r)
	if recorder.Code != http.StatusOK {
		copyRecordedResponse(w, recorder)
		return
	}
	var response map[string]any
	if json.Unmarshal(recorder.Body.Bytes(), &response) != nil {
		copyRecordedResponse(w, recorder)
		return
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		copyRecordedResponse(w, recorder)
		return
	}
	result["supportedVersions"] = []string{Version}
	for key, values := range recorder.Header() {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.Header().Del("Content-Length")
	writeJSON(w, recorder.Code, response)
}

func copyRecordedResponse(w http.ResponseWriter, recorder *httptest.ResponseRecorder) {
	for key, values := range recorder.Header() {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(recorder.Code)
	_, _ = w.Write(recorder.Body.Bytes())
}

var errInvalid = fmt.Errorf("invalid request")

func ioRead(r *http.Request, n int64) ([]byte, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, n+1))
	if int64(len(b)) > n {
		return nil, fmt.Errorf("request too large")
	}
	return b, err
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
