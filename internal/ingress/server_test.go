package ingress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/health"
)

func readyState() *health.State {
	s := health.New([]string{"x"})
	s.SetLive(true)
	s.SetServer("x", "healthy", 0, "")
	return s
}

func request(method, version string) *http.Request {
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": version, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}}
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	r.Header.Set("MCP-Protocol-Version", version)
	r.Header.Set("Mcp-Method", method)
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func requestWithRawID(method, id string) *http.Request {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":%q,"params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, id, method)
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	r.Header.Set("MCP-Protocol-Version", Version)
	r.Header.Set("Mcp-Method", method)
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestDiscoverAndProtocolAdmission(t *testing.T) {
	cfg := config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}
	s := New(cfg, readyState(), "test")
	cases := []struct {
		name   string
		r      *http.Request
		status int
	}{
		{"discover", request("server/discover", Version), 200},
		{"wrong version", request("server/discover", "2025-11-25"), 400},
		{"version header mismatch", func() *http.Request {
			r := request("server/discover", "2025-11-25")
			r.Header.Set("MCP-Protocol-Version", Version)
			return r
		}(), 400},
		{"method mismatch", func() *http.Request {
			r := request("server/discover", Version)
			r.Header.Set("Mcp-Method", "tools/list")
			return r
		}(), 400},
		{"unsupported", request("prompts/list", Version), 404},
		{"missing metadata", func() *http.Request {
			r := request("server/discover", Version)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			delete(body["params"].(map[string]any)["_meta"].(map[string]any), "io.modelcontextprotocol/clientCapabilities")
			b, _ := json.Marshal(body)
			r.Body = io.NopCloser(bytes.NewReader(b))
			return r
		}(), 400},
		{"malformed", httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString("{")), 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, tc.r)
			if w.Code != tc.status {
				t.Fatalf("got %d body=%s", w.Code, w.Body.String())
			}
			if tc.status == 200 && !bytes.Contains(w.Body.Bytes(), []byte("supportedVersions")) {
				t.Fatal("missing discovery")
			}
			if tc.name == "unsupported" && !bytes.Contains(w.Body.Bytes(), []byte("-32601")) {
				t.Fatalf("missing method-not-found response: %s", w.Body.String())
			}
		})
	}
}

func TestDiscoverAdvertisesExactProfile(t *testing.T) {
	s := New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, readyState(), "test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request("server/discover", Version))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Result struct {
			SupportedVersions []string       `json:"supportedVersions"`
			Capabilities      map[string]any `json:"capabilities"`
			Meta              map[string]any `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Result.SupportedVersions) != 1 || response.Result.SupportedVersions[0] != Version {
		t.Fatalf("versions=%v", response.Result.SupportedVersions)
	}
	if len(response.Result.Capabilities) != 1 || response.Result.Capabilities["tools"] == nil {
		t.Fatalf("capabilities=%v", response.Result.Capabilities)
	}
	if _, ok := response.Result.Meta["io.modelcontextprotocol/serverInfo"]; !ok {
		t.Fatalf("missing Conduit identity: %v", response.Result.Meta)
	}
}

func TestSDKContentNegotiation(t *testing.T) {
	s := New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, readyState(), "test")
	for name, mutate := range map[string]func(*http.Request){
		"accept":       func(r *http.Request) { r.Header.Set("Accept", "application/json") },
		"content-type": func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := request("server/discover", Version)
			mutate(r)
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest && w.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestUnsupportedMethodStillRequiresValidTransport(t *testing.T) {
	s := New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, readyState(), "test")
	for name, tc := range map[string]struct {
		mutate     func(*http.Request)
		status     int
		methodCode bool
	}{
		"valid":                {func(*http.Request) {}, http.StatusNotFound, true},
		"invalid accept":       {func(r *http.Request) { r.Header.Set("Accept", "application/json") }, http.StatusBadRequest, false},
		"invalid content type": {func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := request("prompts/list", Version)
			tc.mutate(r)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if bytes.Contains(w.Body.Bytes(), []byte("-32601")) != tc.methodCode {
				t.Fatalf("method-not-found=%t body=%s", bytes.Contains(w.Body.Bytes(), []byte("-32601")), w.Body.String())
			}
		})
	}
}

func TestUnsupportedMethodPreservesValidRequestIDs(t *testing.T) {
	s := New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, readyState(), "test")
	for name, id := range map[string]string{
		"string": "\"exact-id\"",
		"number": "42",
		"large":  "9007199254740993",
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, requestWithRawID("prompts/list", id))
			if w.Code != http.StatusNotFound {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			var response struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if string(response.ID) != id {
				t.Fatalf("id=%s want %s", response.ID, id)
			}
		})
	}
}

func TestUnsupportedMethodRejectsInvalidRequestIDs(t *testing.T) {
	s := New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, readyState(), "test")
	for name, id := range map[string]string{"object": `{}`, "array": `[]`} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, requestWithRawID("prompts/list", id))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":,"method":"prompts/list"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed request got %d: %s", w.Code, w.Body.String())
	}
}

func TestUnsupportedMethodNullIDMatchesSDKNotificationBehavior(t *testing.T) {
	s := New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, readyState(), "test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, requestWithRawID("prompts/list", "null"))
	if w.Code != http.StatusBadRequest || bytes.Contains(w.Body.Bytes(), []byte("-32601")) {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}

func TestOriginPolicy(t *testing.T) {
	cfg := config.Config{Listener: config.Listener{Address: "127.0.0.1:0", AllowedOrigins: []string{"http://localhost:3000"}}}
	s := New(cfg, readyState(), "test")
	cases := map[string]struct {
		origin string
		want   int
	}{"none": {"", 200}, "configured": {"http://localhost:3000", 200}, "arbitrary": {"https://evil.example", 403}, "null": {"null", 403}, "malformed": {"not an origin", 403}}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := request("server/discover", Version)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d", w.Code)
			}
		})
	}
}

func TestHealthAndNotReady(t *testing.T) {
	s := New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, health.New([]string{"x"}), "test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("\"ready\":false")) {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request("server/discover", Version))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", w.Code)
	}
}

func TestSDKValidatesToolNameHeaderBeforeUnknownTool(t *testing.T) {
	s := New(config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}, readyState(), "test")
	r := request("tools/call", Version)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	body["params"].(map[string]any)["name"] = "public.tool"
	b, _ := json.Marshal(body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.Header.Set("Mcp-Name", "different.tool")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}
