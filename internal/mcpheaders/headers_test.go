package mcpheaders

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func headerTool() *mcp.Tool {
	return &mcp.Tool{Name: "t", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
		"text":  map[string]any{"type": "string", "x-mcp-header": "Text"},
		"flag":  map[string]any{"type": "boolean", "x-mcp-header": "Flag"},
		"count": map[string]any{"type": "integer", "x-mcp-header": "Count"},
	}}}
}

func TestGenerateAndValidateSEP2243Values(t *testing.T) {
	args := json.RawMessage(`{"text":" héllo ","flag":true,"count":42}`)
	headers, err := Generate(headerTool(), args)
	if err != nil {
		t.Fatal(err)
	}
	if got := headers.Get("Mcp-Param-Text"); got != "=?base64?IGjDqWxsbyA=?=" {
		t.Fatalf("text=%q", got)
	}
	if headers.Get("Mcp-Param-Flag") != "true" || headers.Get("Mcp-Param-Count") != "42" {
		t.Fatalf("headers=%v", headers)
	}
	headers.Set("Mcp-Name", "public.t")
	if err := ValidateCall(headers, "public.t", args, headerTool()); err != nil {
		t.Fatal(err)
	}
	headers.Set("Mcp-Param-Count", "43")
	if err := ValidateCall(headers, "public.t", args, headerTool()); err == nil {
		t.Fatal("want mismatch")
	}
}

func TestGenerateFailsClosedForUnsupportedBoundValues(t *testing.T) {
	if _, err := Generate(headerTool(), json.RawMessage(`{"count":9007199254740993}`)); err == nil {
		t.Fatal("want unsafe number rejection")
	}
	bad := &mcp.Tool{Name: "bad", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "number", "x-mcp-header": "X"}}}}
	if _, err := Generate(bad, json.RawMessage(`{"x":1}`)); err == nil {
		t.Fatal("want invalid annotation rejection")
	}
}

func TestAbsentHeadersMustNotAppear(t *testing.T) {
	h := http.Header{"Mcp-Name": []string{"public.t"}, "Mcp-Param-Text": []string{"x"}}
	if err := ValidateCall(h, "public.t", json.RawMessage(`{}`), headerTool()); err == nil {
		t.Fatal("want unexpected header")
	}
}

func TestGenerateMatchesSDKOutboundHeaderBehavior(t *testing.T) {
	tool := headerTool()
	var mu sync.Mutex
	got := http.Header{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "tools/call" {
			mu.Lock()
			got = r.Header.Clone()
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"resultType": "complete", "tools": []*mcp.Tool{tool}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
		}
	}))
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: server.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "t", Arguments: map[string]any{"text": " héllo ", "flag": true, "count": 42}}); err != nil {
		t.Fatal(err)
	}
	want, err := Generate(tool, json.RawMessage(`{"text":" héllo ","flag":true,"count":42}`))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for key, values := range want {
		if got.Get(key) != values[0] {
			t.Fatalf("%s=%q want %q", key, got.Get(key), values[0])
		}
	}
}

func TestValidateNumericHeadersMatchesPinnedSDK(t *testing.T) {
	tool := headerTool()
	for _, test := range []struct {
		name   string
		args   string
		header string
	}{
		{"integer", `{"count":42}`, "42"},
		{"decimal equivalent", `{"count":42}`, "42.0"},
		{"negative", `{"count":-42}`, "-42.0"},
		{"zero", `{"count":0}`, "0.0"},
		{"safe upper boundary", `{"count":9007199254740991}`, "9007199254740991.0"},
		{"outside safe range", `{"count":9007199254740992}`, "9007199254740992"},
		{"body decimal", `{"count":42.0}`, "42"},
		{"fractional header", `{"count":42}`, "42.5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{"Mcp-Name": []string{"t"}, "Mcp-Param-Count": []string{test.header}}
			conduitAccepted := ValidateCall(header, "t", json.RawMessage(test.args), tool) == nil
			sdkAccepted := sdkAcceptsNumericHeader(t, tool, test.args, test.header)
			if conduitAccepted != sdkAccepted {
				t.Fatalf("Conduit accepted=%v SDK accepted=%v args=%s header=%q", conduitAccepted, sdkAccepted, test.args, test.header)
			}
		})
	}
}

func sdkAcceptsNumericHeader(t *testing.T, tool *mcp.Tool, args, count string) bool {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	server.AddTool(tool, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t","arguments":` + args + `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "t")
	req.Header.Set("Mcp-Param-Count", count)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response.Code != http.StatusBadRequest
}
