package registry

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/Rakshita-0023/conduit/internal/config"
	"github.com/Rakshita-0023/conduit/internal/policy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testLimits() config.Limits {
	return config.Limits{MaxAggregateTools: 16, MaxAggregateResponseBytes: 1 << 20}
}

func newRegistry(t *testing.T, p config.Policy, limits config.Limits) *Registry {
	t.Helper()
	compiled, err := policy.Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	return New(limits, compiled, mcp.Implementation{Name: "conduit", Version: "test"})
}

func publish(t *testing.T, r *Registry, c Catalog) *Snapshot {
	t.Helper()
	snapshot, err := r.Publish(c)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testTool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: "description " + name, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}, OutputSchema: map[string]any{"type": "object"}, Meta: map[string]any{"source": name}}
}

func TestFederatesNamespacedToolsAndRoutes(t *testing.T) {
	r := newRegistry(t, config.Policy{Allow: []string{"github.*", "postgres.*"}}, testLimits())
	publish(t, r, Catalog{ServerID: "postgres", Tools: []*mcp.Tool{testTool("query")}})
	s := publish(t, r, Catalog{ServerID: "github", Tools: []*mcp.Tool{testTool("search"), testTool("create")}})
	if s.State != StateReady || s.ToolCount != 3 {
		t.Fatalf("snapshot=%+v", s)
	}
	got := []string{s.tools[0].Name, s.tools[1].Name, s.tools[2].Name}
	if want := []string{"github.create", "github.search", "postgres.query"}; !sameStrings(got, want) {
		t.Fatalf("tools=%v want=%v", got, want)
	}
	if s.tools[0].Description != "description create" || s.tools[0].InputSchema == nil || s.tools[0].OutputSchema == nil || s.tools[0].Meta["source"] != "create" {
		t.Fatalf("tool fields not preserved: %#v", s.tools[0])
	}
	route, ok := s.Resolve("github.search")
	if !ok || route != (Route{PublicName: "github.search", ServerID: "github", DownstreamToolName: "search"}) {
		t.Fatalf("route=%+v ok=%t", route, ok)
	}
	if _, ok := s.Resolve("github.missing"); ok {
		t.Fatal("unexpected route")
	}
}

func TestRouteIsStoredNotReconstructedFromPublicName(t *testing.T) {
	r := newRegistry(t, config.Policy{Allow: []string{"a.*"}}, testLimits())
	s := publish(t, r, Catalog{ServerID: "a.b", Tools: []*mcp.Tool{testTool("c")}})
	route, ok := s.Resolve("a.b.c")
	if !ok || route.ServerID != "a.b" || route.DownstreamToolName != "c" {
		t.Fatalf("route=%+v ok=%t", route, ok)
	}
}

func TestOrderingDoesNotDependOnPublicationOrder(t *testing.T) {
	limits := testLimits()
	policy := config.Policy{Allow: []string{"a.*", "b.*"}}
	r1 := newRegistry(t, policy, limits)
	publish(t, r1, Catalog{ServerID: "b", Tools: []*mcp.Tool{testTool("z"), testTool("a")}})
	s1 := publish(t, r1, Catalog{ServerID: "a", Tools: []*mcp.Tool{testTool("m")}})
	r2 := newRegistry(t, policy, limits)
	publish(t, r2, Catalog{ServerID: "a", Tools: []*mcp.Tool{testTool("m")}})
	s2 := publish(t, r2, Catalog{ServerID: "b", Tools: []*mcp.Tool{testTool("a"), testTool("z")}})
	if string(s1.ResultJSON()) != string(s2.ResultJSON()) {
		t.Fatalf("results differ:\n%s\n%s", s1.ResultJSON(), s2.ResultJSON())
	}
}

func TestCollisionAndLimitsPublishNoPartialAggregate(t *testing.T) {
	t.Run("collision", func(t *testing.T) {
		r := newRegistry(t, config.Policy{}, testLimits())
		publish(t, r, Catalog{ServerID: "a.b", Tools: []*mcp.Tool{testTool("c")}})
		s := publish(t, r, Catalog{ServerID: "a", Tools: []*mcp.Tool{testTool("b.c")}})
		if s.State != StateCollision || len(s.tools) != 0 || len(s.ResultJSON()) != 0 {
			t.Fatalf("snapshot=%+v", s)
		}
	})
	t.Run("tool limit", func(t *testing.T) {
		limits := testLimits()
		limits.MaxAggregateTools = 1
		r := newRegistry(t, config.Policy{Allow: []string{"x.*"}}, limits)
		if s := publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("one")}}); s.State != StateReady {
			t.Fatalf("first=%+v", s)
		}
		s := publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("one"), testTool("two")}})
		if s.State != StateOverLimit || len(s.tools) != 0 || len(s.ResultJSON()) != 0 {
			t.Fatalf("snapshot=%+v", s)
		}
	})
	t.Run("byte boundary", func(t *testing.T) {
		base := newRegistry(t, config.Policy{Allow: []string{"x.*"}}, testLimits())
		ready := publish(t, base, Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("one")}})
		exact := testLimits()
		exact.MaxAggregateResponseBytes = ready.ResultSize()
		if s := publish(t, newRegistry(t, config.Policy{Allow: []string{"x.*"}}, exact), Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("one")}}); s.State != StateReady {
			t.Fatalf("exact=%+v", s)
		}
		exact.MaxAggregateResponseBytes--
		if s := publish(t, newRegistry(t, config.Policy{Allow: []string{"x.*"}}, exact), Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("one")}}); s.State != StateOverLimit || len(s.tools) != 0 {
			t.Fatalf("over=%+v", s)
		}
	})
}

func TestPolicyFilteringAndEmptyAggregate(t *testing.T) {
	r := newRegistry(t, config.Policy{Allow: []string{"github.*"}, Deny: []string{"github.delete"}}, testLimits())
	s := publish(t, r, Catalog{ServerID: "github", Tools: []*mcp.Tool{testTool("read"), testTool("delete")}})
	if s.State != StateReady || s.ToolCount != 1 || s.tools[0].Name != "github.read" {
		t.Fatalf("snapshot=%+v", s)
	}
	empty := publish(t, newRegistry(t, config.Policy{}, testLimits()), Catalog{ServerID: "github", Tools: []*mcp.Tool{testTool("read")}})
	if empty.State != StateReady || empty.ToolCount != 0 || len(empty.tools) != 0 {
		t.Fatalf("empty=%+v", empty)
	}
}

func TestPublishTakesOwnershipOfNestedToolFields(t *testing.T) {
	truth := true
	original := &mcp.Tool{
		Name:         "owned",
		Description:  "original",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"nested": map[string]any{"type": "string"}}},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}},
		Meta:         map[string]any{"nested": map[string]any{"source": "original"}},
		Annotations:  &mcp.ToolAnnotations{Title: "original title", DestructiveHint: &truth},
		Icons:        []mcp.Icon{{Source: "https://example.test/original.svg"}},
	}
	r := newRegistry(t, config.Policy{Allow: []string{"x.*"}}, testLimits())
	published := publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{original}})
	expected := string(published.ResultJSON())

	original.Description = "mutated"
	original.InputSchema.(map[string]any)["type"] = "array"
	original.InputSchema.(map[string]any)["properties"].(map[string]any)["nested"].(map[string]any)["type"] = "number"
	original.OutputSchema.(map[string]any)["properties"].(map[string]any)["answer"].(map[string]any)["type"] = "number"
	original.Meta["nested"].(map[string]any)["source"] = "mutated"
	original.Annotations.Title = "mutated title"
	original.Icons[0].Source = "https://example.test/mutated.svg"

	tool := published.tools[0]
	if tool.Description != "original" || tool.InputSchema.(map[string]any)["type"] != "object" || tool.OutputSchema.(map[string]any)["properties"].(map[string]any)["answer"].(map[string]any)["type"] != "string" || tool.Meta["nested"].(map[string]any)["source"] != "original" || tool.Annotations.Title != "original title" || tool.Icons[0].Source != "https://example.test/original.svg" {
		t.Fatalf("published tool changed: %#v", tool)
	}

	rebuilt := publish(t, r, Catalog{ServerID: "other", Tools: []*mcp.Tool{testTool("ignored")}})
	if got := string(rebuilt.ResultJSON()); got != expected {
		t.Fatalf("rebuild used externally mutated tool:\n%s\nwant\n%s", got, expected)
	}
}

func TestAggregateEncodingStopsAtConfiguredLimit(t *testing.T) {
	buffer := &boundedBuffer{limit: 64}
	if err := buffer.Write(bytes.Repeat([]byte("x"), 4096)); !errors.Is(err, errAggregateResponseOverLimit) {
		t.Fatalf("write error=%v", err)
	}
	if buffer.Len() != 65 {
		t.Fatalf("buffer retained %d bytes, want 65", buffer.Len())
	}

	limits := testLimits()
	limits.MaxAggregateResponseBytes = 512
	r := newRegistry(t, config.Policy{Allow: []string{"x.*"}}, limits)
	ready := publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{{Name: "small", InputSchema: map[string]any{"type": "object"}}}})
	if ready.State != StateReady {
		t.Fatalf("initial snapshot=%+v", ready)
	}
	tool := testTool("large")
	tool.Description = strings.Repeat("x", 4096)
	s := publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{tool}})
	if s == ready || s.State != StateOverLimit || len(s.ResultJSON()) != 0 {
		t.Fatalf("snapshot=%+v", s)
	}
	if _, ok := s.Resolve("x.large"); ok {
		t.Fatal("oversized aggregate published a route")
	}
}

func TestCloneFailureDoesNotPublishCatalog(t *testing.T) {
	r := newRegistry(t, config.Policy{Allow: []string{"x.*"}}, testLimits())
	before := publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("good")}})
	if _, err := r.Publish(Catalog{ServerID: "x", Tools: []*mcp.Tool{{Name: "bad", InputSchema: math.Inf(1)}}}); err == nil {
		t.Fatal("want clone failure")
	}
	if after := r.Snapshot(); after != before || string(after.ResultJSON()) != string(before.ResultJSON()) {
		t.Fatalf("clone failure changed active snapshot: before=%+v after=%+v", before, after)
	}
}

func TestUntrustedToolDoesNotPanicAndConcurrentSnapshotsAreAtomic(t *testing.T) {
	r := newRegistry(t, config.Policy{Allow: []string{"x.*"}}, testLimits())
	if s := publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{nil}}); s.State != StateUnavailable || len(s.tools) != 0 {
		t.Fatalf("invalid=%+v", s)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("one"), testTool("two")}})
				s := r.Snapshot()
				if s.State != StateReady || s.ToolCount != 2 || len(s.ResultJSON()) == 0 {
					t.Errorf("partial snapshot=%+v", s)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestAuthorizationLinearizationBlocksPublicationWithoutDeadlock(t *testing.T) {
	r := newRegistry(t, config.Policy{Allow: []string{"x.*"}}, testLimits())
	publish(t, r, Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("one")}})
	prepared, err := r.PrepareExecution("x.one")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	committed := make(chan error, 1)
	go func() {
		committed <- r.CommitAuthorization(prepared, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	published := make(chan error, 1)
	publicationStarted := make(chan struct{})
	go func() {
		close(publicationStarted)
		_, err := r.Publish(Catalog{ServerID: "x", Tools: []*mcp.Tool{testTool("two")}})
		published <- err
	}()
	<-publicationStarted
	select {
	case err := <-published:
		t.Fatalf("publication completed during authorization: %v", err)
	default:
	}
	close(release)
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	if err := r.CommitAuthorization(prepared, func() error { return nil }); !errors.Is(err, ErrRouteChanged) {
		t.Fatalf("stale authorization error=%v", err)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
