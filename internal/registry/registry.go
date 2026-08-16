package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/policy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Catalog struct {
	ServerID string
	Tools    []*mcp.Tool
}

// Route is the complete destination information for an advertised tool. It is
// deliberately not derived from PublicName at lookup time.
type Route struct {
	PublicName         string
	ServerID           string
	DownstreamToolName string
}

type AggregateState string

const (
	StateStarting    AggregateState = "starting"
	StateReady       AggregateState = "ready"
	StateUnavailable AggregateState = "unavailable"
	StateCollision   AggregateState = "aggregate_collision"
	StateOverLimit   AggregateState = "aggregate_over_limit"
)

// Snapshot is immutable once returned by Registry. Tools and routes are built
// before publication and are never subsequently modified by Registry.
type Snapshot struct {
	Generation uint64
	CreatedAt  time.Time
	State      AggregateState
	ToolCount  int

	tools      []*mcp.Tool
	routes     map[string]Route
	resultJSON json.RawMessage
	resultSize int64
}

func (s *Snapshot) Resolve(name string) (Route, bool) {
	route, ok := s.routes[name]
	return route, ok
}

func (s *Snapshot) ResultJSON() []byte { return append([]byte(nil), s.resultJSON...) }
func (s *Snapshot) ResultSize() int64  { return s.resultSize }

type Registry struct {
	mu       sync.RWMutex
	catalogs map[string]Catalog
	policy   policy.Compiled
	limits   config.Limits
	impl     mcp.Implementation
	active   *Snapshot
}

var errAggregateResponseOverLimit = errors.New("aggregate response exceeds byte limit")

func New(limits config.Limits, compiled policy.Compiled, impl mcp.Implementation) *Registry {
	return &Registry{
		catalogs: map[string]Catalog{},
		policy:   compiled,
		limits:   limits,
		impl:     impl,
		active:   &Snapshot{State: StateStarting, CreatedAt: time.Now().UTC()},
	}
}

// Publish takes ownership of one complete validated catalog, then atomically
// publishes a complete aggregate state derived from all current catalogs.
func (r *Registry) Publish(c Catalog) (*Snapshot, error) {
	owned, err := cloneCatalog(c)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.catalogs[owned.ServerID] = owned
	r.rebuildLocked()
	return r.active, nil
}

// Remove discards a catalog that is no longer currently valid. No stale tools
// from that downstream can survive the resulting rebuild.
func (r *Registry) Remove(serverID string) *Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.catalogs, serverID)
	r.rebuildLocked()
	return r.active
}

func (r *Registry) Snapshot() *Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active
}

func (r *Registry) Count(id string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.catalogs[id].Tools)
}

func (r *Registry) rebuildLocked() {
	next := &Snapshot{Generation: r.active.Generation + 1, CreatedAt: time.Now().UTC(), State: StateUnavailable}
	if len(r.catalogs) == 0 {
		r.active = next
		return
	}

	type candidate struct {
		tool  *mcp.Tool
		route Route
	}
	byName := make(map[string]candidate)
	for _, catalog := range r.catalogs {
		for _, downstream := range catalog.Tools {
			if downstream == nil || downstream.Name == "" {
				r.active = next
				return
			}
			publicName := catalog.ServerID + "." + downstream.Name
			if _, exists := byName[publicName]; exists {
				next.State = StateCollision
				r.active = next
				return
			}
			publicTool := *downstream
			publicTool.Name = publicName
			byName[publicName] = candidate{tool: &publicTool, route: Route{PublicName: publicName, ServerID: catalog.ServerID, DownstreamToolName: downstream.Name}}
		}
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		if r.policy.Allowed(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > r.limits.MaxAggregateTools {
		next.State = StateOverLimit
		r.active = next
		return
	}
	next.tools = make([]*mcp.Tool, 0, len(names))
	next.routes = make(map[string]Route, len(names))
	for _, name := range names {
		entry := byName[name]
		next.tools = append(next.tools, entry.tool)
		next.routes[name] = entry.route
	}
	encoded, err := encodeListResult(next.tools, r.impl, r.limits.MaxAggregateResponseBytes)
	if err != nil {
		if errors.Is(err, errAggregateResponseOverLimit) {
			next.State = StateOverLimit
		}
		next.tools = nil
		next.routes = nil
		r.active = next
		return
	}
	next.State = StateReady
	next.ToolCount = len(next.tools)
	next.resultJSON = encoded
	next.resultSize = int64(len(encoded))
	r.active = next
}

// encodeListResult is the sole client-facing tools/list result constructor.
// Registry uses its bytes for the aggregate limit and ingress writes these
// exact bytes as the JSON-RPC result value.
func encodeListResult(tools []*mcp.Tool, impl mcp.Implementation, limit int64) ([]byte, error) {
	var result boundedBuffer
	result.limit = limit
	if err := result.writeString(`{"resultType":"complete","_meta":{"io.modelcontextprotocol/serverInfo":`); err != nil {
		return nil, err
	}
	serverInfo, err := json.Marshal(impl)
	if err != nil {
		return nil, fmt.Errorf("encode aggregate server information: %w", err)
	}
	if err := result.Write(serverInfo); err != nil {
		return nil, err
	}
	if err := result.writeString(`},"ttlMs":0,"cacheScope":"public","tools":[`); err != nil {
		return nil, err
	}
	for i, tool := range tools {
		if i > 0 {
			if err := result.writeString(","); err != nil {
				return nil, err
			}
		}
		encodedTool, err := json.Marshal(tool)
		if err != nil {
			return nil, fmt.Errorf("encode aggregate tool: %w", err)
		}
		if err := result.Write(encodedTool); err != nil {
			return nil, err
		}
	}
	if err := result.writeString(`]}`); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}

// boundedBuffer retains at most limit+1 bytes. The extra byte distinguishes
// an exact limit from overflow without retaining a complete oversized result.
type boundedBuffer struct {
	bytes.Buffer
	limit int64
}

func (b *boundedBuffer) Write(p []byte) error {
	maximumRetained := b.limit
	if maximumRetained < math.MaxInt64 {
		maximumRetained++
	}
	remaining := maximumRetained - int64(b.Len())
	if remaining <= 0 {
		return errAggregateResponseOverLimit
	}
	if int64(len(p)) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		return errAggregateResponseOverLimit
	}
	_, _ = b.Buffer.Write(p)
	if int64(b.Len()) > b.limit {
		return errAggregateResponseOverLimit
	}
	return nil
}

func (b *boundedBuffer) writeString(s string) error { return b.Write([]byte(s)) }

func cloneCatalog(c Catalog) (Catalog, error) {
	owned := Catalog{ServerID: c.ServerID, Tools: make([]*mcp.Tool, len(c.Tools))}
	for i, tool := range c.Tools {
		if tool == nil {
			continue
		}
		data, err := json.Marshal(tool)
		if err != nil {
			return Catalog{}, fmt.Errorf("clone catalog tool: %w", err)
		}
		copy := new(mcp.Tool)
		if err := json.Unmarshal(data, copy); err != nil {
			return Catalog{}, fmt.Errorf("clone catalog tool: %w", err)
		}
		owned.Tools[i] = copy
	}
	return owned, nil
}
