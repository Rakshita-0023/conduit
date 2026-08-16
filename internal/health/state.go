package health

import (
	"sort"
	"sync"
	"time"
)

type Downstream struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	ToolCount   int        `json:"tool_count"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	Error       string     `json:"error,omitempty"`
}
type Aggregate struct {
	Generation uint64 `json:"generation"`
	State      string `json:"state"`
	ToolCount  int    `json:"tool_count"`
}
type Status struct {
	Live         bool         `json:"live"`
	Ready        bool         `json:"ready"`
	AuditHealthy bool         `json:"audit_healthy"`
	Aggregate    Aggregate    `json:"aggregate"`
	Downstreams  []Downstream `json:"downstreams"`
}
type State struct {
	mu          sync.RWMutex
	live, audit bool
	servers     map[string]Downstream
	attempted   map[string]bool
	aggregate   Aggregate
}

func New(ids []string) *State {
	s := &State{audit: true, servers: map[string]Downstream{}, attempted: map[string]bool{}, aggregate: Aggregate{State: "starting"}}
	for _, id := range ids {
		s.servers[id] = Downstream{ID: id, State: "starting"}
	}
	return s
}
func (s *State) SetAudit(ok bool) { s.mu.Lock(); defer s.mu.Unlock(); s.audit = ok }
func (s *State) SetLive(ok bool)  { s.mu.Lock(); defer s.mu.Unlock(); s.live = ok }

// SetAggregate records aggregate state only when it is at least as recent as
// the state already observed. Registry snapshot generations are the ordering
// authority for concurrent downstream refresh completions.
func (s *State) SetAggregate(generation uint64, state string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation < s.aggregate.Generation {
		return
	}
	s.aggregate = Aggregate{Generation: generation, State: state, ToolCount: n}
}
func (s *State) SetServer(id, state string, n int, err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.servers[id]
	s.attempted[id] = true
	d.State = state
	d.ToolCount = n
	d.Error = err
	if state == "healthy" {
		now := time.Now().UTC()
		d.LastSuccess = &now
	}
	s.servers[id] = d
}
func (s *State) Snapshot() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Status{Live: s.live, AuditHealthy: s.audit, Aggregate: s.aggregate}
	initialComplete := true
	healthy := false
	for _, d := range s.servers {
		out.Downstreams = append(out.Downstreams, d)
		if !s.attempted[d.ID] {
			initialComplete = false
		}
		if d.State == "healthy" {
			healthy = true
		}
	}
	sort.Slice(out.Downstreams, func(i, j int) bool { return out.Downstreams[i].ID < out.Downstreams[j].ID })
	out.Ready = healthy && out.Live && initialComplete && out.AuditHealthy && out.Aggregate.State == "ready"
	return out
}
