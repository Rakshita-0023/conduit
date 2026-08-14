package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduit-mcp/conduit/internal/config"
)

func limits() config.Limits {
	return config.Limits{MaxPagesPerDownstream: 3, MaxToolsPerDownstream: 4, MaxDownstreamCatalogBytes: 4096, RequestTimeout: time.Second}
}
func tool(name string) map[string]any {
	return map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}}
}
func fixture(t *testing.T, pages map[string]map[string]any, seen *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		var q struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Method == "server/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
			return
		}
		*seen = append(*seen, q.Params.Cursor)
		result := pages[q.Params.Cursor]
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result})
	}))
}
func refresh(t *testing.T, s *httptest.Server, lim config.Limits) (int, error) {
	t.Helper()
	c, e := Refresh(context.Background(), config.Downstream{ID: "x", URL: s.URL}, lim)
	return len(c.Tools), e
}
func TestRefreshPaginationAndOpaqueCursor(t *testing.T) {
	var got []string
	s := fixture(t, map[string]map[string]any{"": {"resultType": "complete", "tools": []any{tool("one")}, "nextCursor": "opaque+/="}, "opaque+/=": {"resultType": "complete", "tools": []any{tool("two")}}}, &got)
	defer s.Close()
	n, e := refresh(t, s, limits())
	if e != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, e)
	}
	if strings.Join(got, ",") != ",opaque+/=" {
		t.Fatalf("cursor forwarding=%q", got)
	}
}
func TestRefreshRejectsFailures(t *testing.T) {
	cases := map[string]struct {
		pages map[string]map[string]any
		lim   config.Limits
	}{"repeated": {map[string]map[string]any{"": {"resultType": "complete", "tools": []any{tool("a")}, "nextCursor": "x"}, "x": {"resultType": "complete", "tools": []any{tool("b")}, "nextCursor": "x"}}, limits()}, "malformed": {map[string]map[string]any{"": {"resultType": "complete", "tools": []any{map[string]any{"name": "a"}}}}, limits()}, "pages": {map[string]map[string]any{"": {"resultType": "complete", "tools": []any{tool("a")}, "nextCursor": "x"}, "x": {"resultType": "complete", "tools": []any{tool("b")}, "nextCursor": "y"}, "y": {"resultType": "complete", "tools": []any{tool("c")}}}, func() config.Limits { l := limits(); l.MaxPagesPerDownstream = 2; return l }()}, "tools": {map[string]map[string]any{"": {"resultType": "complete", "tools": []any{tool("a"), tool("b"), tool("c")}}}, func() config.Limits { l := limits(); l.MaxToolsPerDownstream = 2; return l }()}}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var seen []string
			s := fixture(t, tc.pages, &seen)
			defer s.Close()
			if _, e := refresh(t, s, tc.lim); e == nil {
				t.Fatal("want error")
			}
		})
	}
}
func TestRefreshByteLimitAndUnavailable(t *testing.T) {
	var seen []string
	s := fixture(t, map[string]map[string]any{"": {"resultType": "complete", "tools": []any{map[string]any{"name": "a", "description": strings.Repeat("x", 5000), "inputSchema": map[string]any{"type": "object"}}}}}, &seen)
	defer s.Close()
	l := limits()
	l.MaxDownstreamCatalogBytes = 100
	if _, e := refresh(t, s, l); e == nil {
		t.Fatal("want byte limit")
	}
	if _, e := Refresh(context.Background(), config.Downstream{ID: "bad", URL: "http://127.0.0.1:1"}, limits()); e == nil {
		t.Fatal("want unavailable error")
	}
}

func TestRefreshRejectsRedirectWithoutContactingTarget(t *testing.T) {
	var targetRequests atomic.Int32
	var targetAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		targetAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	_, err := Refresh(context.Background(), config.Downstream{ID: "x", URL: source.URL, Headers: map[string]string{"Authorization": "Bearer secret"}}, limits())
	if err == nil {
		t.Fatal("want redirect error")
	}
	if targetRequests.Load() != 0 || targetAuthorization != "" {
		t.Fatalf("redirect target received requests=%d authorization=%q", targetRequests.Load(), targetAuthorization)
	}
}

func TestRefreshRejectsOversizedDiscoverResponse(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID any `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": strings.Repeat("x", 5000)}})
	}))
	defer s.Close()
	l := limits()
	l.MaxDownstreamCatalogBytes = 100
	if _, err := refresh(t, s, l); err == nil {
		t.Fatal("want oversized discovery error")
	}
}

func TestRefreshNeverSendsLegacyInitialize(t *testing.T) {
	var methods []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		methods = append(methods, req.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	}))
	defer s.Close()
	if _, err := refresh(t, s, limits()); err == nil {
		t.Fatal("want discovery error")
	}
	if strings.Join(methods, ",") != "server/discover" {
		t.Fatalf("methods=%v", methods)
	}
}

func TestRefreshCanceledContextDoesNotReachDownstream(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Refresh(ctx, config.Downstream{ID: "x", URL: s.URL}, limits()); err == nil {
		t.Fatal("want cancellation error")
	}
	if requests.Load() != 0 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestRefreshRejectsToolFilteredBySDK(t *testing.T) {
	var seen []string
	invalid := map[string]any{"name": "bad", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "number", "x-mcp-header": "X-Value"}}}}
	s := fixture(t, map[string]map[string]any{"": {"resultType": "complete", "tools": []any{tool("good"), invalid}}}, &seen)
	defer s.Close()
	if n, err := refresh(t, s, limits()); err == nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestRefreshAllowsExactLimits(t *testing.T) {
	t.Run("pages", func(t *testing.T) {
		var seen []string
		s := fixture(t, map[string]map[string]any{"": {"resultType": "complete", "tools": []any{tool("a")}, "nextCursor": "x"}, "x": {"resultType": "complete", "tools": []any{tool("b")}}}, &seen)
		defer s.Close()
		l := limits()
		l.MaxPagesPerDownstream = 2
		if n, err := refresh(t, s, l); err != nil || n != 2 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	t.Run("tools", func(t *testing.T) {
		var seen []string
		s := fixture(t, map[string]map[string]any{"": {"resultType": "complete", "tools": []any{tool("a"), tool("b")}}}, &seen)
		defer s.Close()
		l := limits()
		l.MaxToolsPerDownstream = 2
		if n, err := refresh(t, s, l); err != nil || n != 2 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		discoverBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"resultType": "complete", "supportedVersions": []string{"2026-07-28"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
		if err != nil {
			t.Fatal(err)
		}
		listBody, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"resultType": "complete", "tools": []any{}}})
		if err != nil {
			t.Fatal(err)
		}
		discoverBody = append(discoverBody, '\n')
		listBody = append(listBody, '\n')
		max := len(discoverBody)
		if len(listBody) > max {
			max = len(listBody)
		}
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			if req.Method == "server/discover" {
				_, _ = w.Write(discoverBody)
				return
			}
			_, _ = w.Write(listBody)
		}))
		defer s.Close()
		l := limits()
		l.MaxDownstreamCatalogBytes = int64(max)
		if n, err := refresh(t, s, l); err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
}
