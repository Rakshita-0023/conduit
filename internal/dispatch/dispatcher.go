// Package dispatch is Conduit's sole production owner of downstream tools/call.
package dispatch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conduit-mcp/conduit/internal/audit"
	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/health"
	"github.com/conduit-mcp/conduit/internal/mcpheaders"
	"github.com/conduit-mcp/conduit/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	CodeToolUnavailable         int64 = -32010
	CodeToolDispatchFailed      int64 = -32011
	CodeToolOutcomeUnknown      int64 = -32012
	CodeToolResponseUnsupported int64 = -32013
	CodeAuditUnavailable        int64 = -32014
	protocolVersion                   = "2026-07-28"
	maxPreparationAttempts            = 3
)

type Error struct {
	Code    int64
	Message string
}

func (e *Error) Error() string { return e.Message }

type Call struct {
	PublicName     string
	Arguments      json.RawMessage
	InputResponses json.RawMessage
	RequestState   json.RawMessage
}

type Response struct {
	Result json.RawMessage
	Error  *jsonrpc.Error
}

type Dispatcher struct {
	registry *registry.Registry
	audit    *audit.Log
	health   *health.State
	limits   config.Limits
	servers  map[string]config.Downstream
	impl     mcp.Implementation
	// lifecycleMu serializes admission with shutdown so Wait cannot race an
	// Add. Active invocations include their terminal audit write.
	lifecycleMu sync.Mutex
	closing     bool
	shutdownCtx context.Context
	cancel      context.CancelFunc
	active      sync.WaitGroup
	// testBeforeCommit is nil in production. It synchronizes the real route
	// preparation-to-authorization boundary in package tests.
	testBeforeCommit func()
}

func New(c config.Config, r *registry.Registry, a *audit.Log, h *health.State, build string) *Dispatcher {
	servers := make(map[string]config.Downstream, len(c.Servers))
	for _, server := range c.Servers {
		servers[server.ID] = server
	}
	shutdownCtx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{registry: r, audit: a, health: h, limits: c.Limits, servers: servers, impl: mcp.Implementation{Name: "conduit", Version: build}, shutdownCtx: shutdownCtx, cancel: cancel}
}

func (d *Dispatcher) Execute(ctx context.Context, call Call) (Response, error) {
	if !d.admit() {
		return Response{}, local(CodeToolUnavailable, "tool unavailable")
	}
	defer d.active.Done()
	workCtx, cancel := context.WithCancel(ctx)
	stopShutdownCancel := context.AfterFunc(d.shutdownCtx, cancel)
	defer func() {
		stopShutdownCancel()
		cancel()
	}()

	callID, err := newCallID()
	if err != nil {
		return Response{}, local(CodeAuditUnavailable, "audit unavailable")
	}
	if !d.audit.Available() {
		d.markAuditFailed()
		return Response{}, local(CodeAuditUnavailable, "audit unavailable")
	}
	if !d.health.Snapshot().Ready {
		return Response{}, local(CodeToolUnavailable, "tool unavailable")
	}

	var prepared registry.PreparedRoute
	var outbound json.RawMessage
	var paramHeaders http.Header
	for attempt := 0; attempt < maxPreparationAttempts; attempt++ {
		prepared, err = d.registry.PrepareExecution(call.PublicName)
		if err != nil {
			return Response{}, d.deny(callID, call.PublicName, "unavailable")
		}
		outbound, err = downstreamParams(prepared.Route.DownstreamToolName, call, d.impl)
		if err != nil {
			return Response{}, d.deny(callID, call.PublicName, "invalid_call")
		}
		paramHeaders, err = mcpheaders.Generate(prepared.Tool, call.Arguments)
		if err != nil {
			return Response{}, d.deny(callID, call.PublicName, "invalid_headers")
		}
		if d.testBeforeCommit != nil {
			d.testBeforeCommit()
		}
		err = d.registry.CommitAuthorization(prepared, func() error {
			return d.audit.Append(audit.Event{Event: "tool_call_authorized", CallID: callID, PublicTool: prepared.Route.PublicName, ServerID: prepared.Route.ServerID, DownstreamToolName: prepared.Route.DownstreamToolName, RegistryGeneration: prepared.Generation, PolicyDigest: prepared.PolicyDigest})
		})
		if errors.Is(err, registry.ErrRouteChanged) {
			continue
		}
		if err != nil {
			if errors.Is(err, registry.ErrRouteDenied) || errors.Is(err, registry.ErrRouteMissing) {
				return Response{}, d.deny(callID, call.PublicName, "denied")
			}
			d.markAuditFailed()
			return Response{}, local(CodeAuditUnavailable, "audit unavailable")
		}
		break
	}
	if err != nil {
		return Response{}, d.deny(callID, call.PublicName, "route_changing")
	}
	if workCtx.Err() != nil {
		d.terminal(callID, prepared, "tool_call_downstream_error", "cancelled_before_dispatch", 0)
		return Response{}, local(CodeToolDispatchFailed, "tool dispatch failed")
	}
	server, ok := d.servers[prepared.Route.ServerID]
	if !ok {
		d.terminal(callID, prepared, "tool_call_downstream_error", "missing_server", 0)
		return Response{}, local(CodeToolDispatchFailed, "tool dispatch failed")
	}

	started := new(atomic.Bool)
	capture := &responseCapture{}
	transport := &callTransport{base: http.DefaultTransport, max: d.limits.MaxToolResponseBytes, headers: server.Headers, toolName: prepared.Route.DownstreamToolName, paramHeaders: paramHeaders, started: started, capture: capture, endpoint: server.URL}
	hc := &http.Client{Timeout: d.limits.ToolCallTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("downstream redirects are not permitted")
	}}
	callCtx, callCancel := context.WithTimeout(workCtx, d.limits.ToolCallTimeout)
	defer callCancel()
	begin := time.Now()
	connection, err := (&mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}).Connect(callCtx)
	if err == nil {
		defer func() {
			transport.cleanup(callCtx)
			// The SDK's Close cannot accept callCtx. cleanup claims the session
			// first, so this connection-close attempt is rejected locally and
			// cannot send a second DELETE on its detached context.
			_ = connection.Close()
		}()
		requestID, _ := jsonrpc.MakeID(callID)
		request := &jsonrpc.Request{ID: requestID, Method: "tools/call", Params: outbound}
		err = connection.Write(callCtx, request)
		if err == nil {
			var message jsonrpc.Message
			message, err = connection.Read(callCtx)
			if err == nil {
				response, ok := message.(*jsonrpc.Response)
				if !ok || response.ID.Raw() != callID {
					err = errors.New("unexpected downstream message")
				} else if response.Error != nil {
					wire, ok := response.Error.(*jsonrpc.Error)
					if !ok {
						err = errors.New("invalid downstream error")
					} else {
						d.terminal(callID, prepared, "tool_call_downstream_error", "downstream_jsonrpc_error", time.Since(begin))
						return Response{Error: wire}, nil
					}
				} else if err = validateResult(response.Result); err == nil {
					d.terminal(callID, prepared, "tool_call_completed", "completed", time.Since(begin))
					return Response{Result: append(json.RawMessage(nil), response.Result...)}, nil
				} else if errors.Is(err, errUnsupportedResult) {
					d.terminal(callID, prepared, "tool_call_downstream_error", "unsupported_response", time.Since(begin))
					return Response{}, local(CodeToolResponseUnsupported, "tool response unsupported")
				}
			}
		}
	}
	if wire, ok := capture.jsonrpcError(callID); ok {
		d.terminal(callID, prepared, "tool_call_downstream_error", "downstream_jsonrpc_error", time.Since(begin))
		return Response{Error: wire}, nil
	}
	if started.Load() {
		d.terminal(callID, prepared, "tool_call_unknown_after_dispatch", "unknown", time.Since(begin))
		return Response{}, local(CodeToolOutcomeUnknown, "tool outcome unknown")
	}
	d.terminal(callID, prepared, "tool_call_downstream_error", "local_failure", time.Since(begin))
	return Response{}, local(CodeToolDispatchFailed, "tool dispatch failed")
}

// BeginShutdown rejects new executions. CancelActive then cancels executions
// already admitted; callers must Wait before closing audit storage.
func (d *Dispatcher) BeginShutdown() {
	d.lifecycleMu.Lock()
	d.closing = true
	d.lifecycleMu.Unlock()
}

func (d *Dispatcher) CancelActive() {
	d.BeginShutdown()
	d.cancel()
}

func (d *Dispatcher) Wait() { d.active.Wait() }

func (d *Dispatcher) admit() bool {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.closing {
		return false
	}
	d.active.Add(1)
	return true
}

func (d *Dispatcher) deny(callID, name, outcome string) error {
	if err := d.audit.Append(audit.Event{Event: "tool_call_denied", CallID: callID, PublicTool: name, Outcome: outcome}); err != nil {
		d.markAuditFailed()
		return local(CodeAuditUnavailable, "audit unavailable")
	}
	return local(CodeToolUnavailable, "tool unavailable")
}

func (d *Dispatcher) terminal(callID string, prepared registry.PreparedRoute, event, outcome string, duration time.Duration) {
	if err := d.audit.Append(audit.Event{Event: event, CallID: callID, PublicTool: prepared.Route.PublicName, ServerID: prepared.Route.ServerID, DownstreamToolName: prepared.Route.DownstreamToolName, RegistryGeneration: prepared.Generation, PolicyDigest: prepared.PolicyDigest, Outcome: outcome, DurationMS: duration.Milliseconds()}); err != nil {
		d.markAuditFailed()
	}
}

func (d *Dispatcher) markAuditFailed()       { d.health.SetAudit(false) }
func local(code int64, message string) error { return &Error{Code: code, Message: message} }

func newCallID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func downstreamParams(name string, call Call, impl mcp.Implementation) (json.RawMessage, error) {
	params := map[string]json.RawMessage{}
	nameJSON, _ := json.Marshal(name)
	params["name"] = nameJSON
	if len(call.Arguments) > 0 {
		params["arguments"] = append(json.RawMessage(nil), call.Arguments...)
	}
	if len(call.InputResponses) > 0 {
		params["inputResponses"] = append(json.RawMessage(nil), call.InputResponses...)
	}
	if len(call.RequestState) > 0 {
		params["requestState"] = append(json.RawMessage(nil), call.RequestState...)
	}
	meta, err := json.Marshal(map[string]any{"io.modelcontextprotocol/protocolVersion": protocolVersion, "io.modelcontextprotocol/clientInfo": impl, "io.modelcontextprotocol/clientCapabilities": map[string]any{}})
	if err != nil {
		return nil, err
	}
	params["_meta"] = meta
	b, err := json.Marshal(params)
	return b, err
}

var errUnsupportedResult = errors.New("unsupported result")

func validateResult(raw json.RawMessage) error {
	var result struct {
		ResultType    string          `json:"resultType"`
		InputRequests json.RawMessage `json:"inputRequests"`
		RequestState  string          `json:"requestState"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	var typed mcp.CallToolResult
	if err := json.Unmarshal(raw, &typed); err != nil {
		return err
	}
	if result.ResultType != "" && result.ResultType != "complete" && result.ResultType != "input_required" {
		return errors.New("invalid result type")
	}
	if result.ResultType != "input_required" {
		return nil
	}
	hasRequests := len(result.InputRequests) > 0 && string(result.InputRequests) != "null"
	if !hasRequests && result.RequestState == "" {
		return errUnsupportedResult
	}
	if hasRequests {
		var requests map[string]json.RawMessage
		if json.Unmarshal(result.InputRequests, &requests) != nil {
			return errors.New("invalid input requests")
		}
		if len(requests) > 0 {
			return errUnsupportedResult
		}
	}
	return nil
}

type callTransport struct {
	base         http.RoundTripper
	max          int64
	headers      map[string]string
	toolName     string
	paramHeaders http.Header
	started      *atomic.Bool
	capture      *responseCapture
	endpoint     string
	sessionMu    sync.Mutex
	sessionID    string
	cleanupUsed  bool
}

func (t *callTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodDelete {
		return t.cleanupRoundTrip(r)
	}
	if r.Method != http.MethodPost {
		return nil, errors.New("unsupported downstream HTTP method")
	}
	for key, value := range t.headers {
		if routingHeader(key) {
			return nil, errors.New("configured MCP routing headers are not permitted")
		}
		r.Header.Set(key, value)
	}
	if r.Header.Get("Mcp-Protocol-Version") != protocolVersion || r.Header.Get("Mcp-Method") != "tools/call" {
		return nil, errors.New("non-modern downstream MCP request blocked")
	}
	r.Header.Set("Mcp-Name", t.toolName)
	for key := range r.Header {
		if strings.HasPrefix(strings.ToLower(key), "mcp-param-") {
			r.Header.Del(key)
		}
	}
	for key, values := range t.paramHeaders {
		r.Header[key] = append([]string(nil), values...)
	}
	// A non-nil GetBody permits net/http to replay a failed idempotent request.
	r.GetBody = nil
	t.started.Store(true)
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	t.rememberSession(resp.Header.Get("Mcp-Session-Id"))
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		resp.Body.Close()
		return nil, errors.New("downstream SSE is unsupported")
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, max: t.max}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.capture.set(resp.StatusCode, nil, true)
			resp.Body = errorBody{err: readErr}
			return resp, nil
		}
		t.capture.set(resp.StatusCode, body, false)
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return resp, nil
}

// cleanup uses callCtx rather than the SDK connection's detached context.
// Errors are intentionally ignored: cleanup follows a completed invocation
// and must not replace its result.
func (t *callTransport) cleanup(ctx context.Context) {
	sessionID, ok := t.claimCleanup()
	if !ok {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", sessionID)
	if err := t.validateCleanup(req); err != nil {
		return
	}
	if err := t.prepareCleanup(req); err != nil {
		return
	}
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
	}
}

// cleanupRoundTrip only receives the SDK's later connection-close DELETE.
// cleanup has already claimed the session, so this must never reach network.
func (t *callTransport) cleanupRoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.validateCleanup(r); err != nil {
		return nil, err
	}
	if _, ok := t.claimCleanup(); !ok {
		return nil, errors.New("downstream session cleanup already attempted")
	}
	if err := t.prepareCleanup(r); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(r)
}

func (t *callTransport) validateCleanup(r *http.Request) error {
	if r.URL.String() != t.endpoint || r.Body != nil || r.Header.Get("Mcp-Session-Id") == "" || !t.matchesSession(r.Header.Get("Mcp-Session-Id")) {
		return errors.New("invalid downstream session cleanup request")
	}
	return nil
}

func (t *callTransport) prepareCleanup(r *http.Request) error {
	for key, value := range t.headers {
		if routingHeader(key) {
			return errors.New("configured MCP routing headers are not permitted")
		}
		r.Header.Set(key, value)
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Mcp-Session-Id") {
			continue // validated against the session returned by this invocation
		}
		if routingHeader(key) {
			return errors.New("MCP routing headers are not permitted on session cleanup")
		}
	}
	r.GetBody = nil
	return nil
}

func (t *callTransport) rememberSession(id string) {
	t.sessionMu.Lock()
	t.sessionID = id
	t.sessionMu.Unlock()
}

func (t *callTransport) claimCleanup() (string, bool) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	if t.sessionID == "" || t.cleanupUsed {
		return "", false
	}
	t.cleanupUsed = true
	return t.sessionID, true
}

func (t *callTransport) matchesSession(id string) bool {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	return t.sessionID != "" && t.sessionID == id
}

func routingHeader(key string) bool {
	key = strings.ToLower(key)
	return key == "mcp-protocol-version" || key == "mcp-method" || key == "mcp-name" || key == "mcp-session-id" || strings.HasPrefix(key, "mcp-param-")
}

type boundedBody struct {
	io.ReadCloser
	max, used int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	remaining := b.max - b.used
	if remaining < 0 {
		return 0, errors.New("downstream tool response exceeds byte limit")
	}
	allowed := remaining
	if allowed < math.MaxInt64 {
		allowed++
	}
	if int64(len(p)) > allowed {
		p = p[:allowed]
	}
	n, err := b.ReadCloser.Read(p)
	b.used += int64(n)
	if b.used > b.max {
		return n, errors.New("downstream tool response exceeds byte limit")
	}
	return n, err
}

type errorBody struct{ err error }

func (b errorBody) Read([]byte) (int, error) { return 0, b.err }
func (errorBody) Close() error               { return nil }

// responseCapture is per invocation. It holds a non-2xx body only when the
// bounded read completed, so malformed, truncated and oversized responses can
// never be relayed.
type responseCapture struct {
	mu        sync.Mutex
	status    int
	body      []byte
	overLimit bool
}

func (c *responseCapture) set(status int, body []byte, overLimit bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
	c.overLimit = overLimit
	if !overLimit {
		c.body = append(c.body[:0], body...)
	}
}

func (c *responseCapture) jsonrpcError(callID string) (*jsonrpc.Error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status == 0 || (c.status >= http.StatusOK && c.status < http.StatusMultipleChoices) || c.overLimit {
		return nil, false
	}
	message, err := jsonrpc.DecodeMessage(c.body)
	if err != nil {
		return nil, false
	}
	response, ok := message.(*jsonrpc.Response)
	if !ok || response.ID.Raw() != callID || response.Error == nil {
		return nil, false
	}
	wire, ok := response.Error.(*jsonrpc.Error)
	return wire, ok
}
