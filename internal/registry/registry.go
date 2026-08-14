package registry

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sync"
)

type Catalog struct {
	ServerID string
	Tools    []*mcp.Tool
}
type Registry struct {
	mu       sync.RWMutex
	catalogs map[string]Catalog
}

func New() *Registry                  { return &Registry{catalogs: map[string]Catalog{}} }
func (r *Registry) Publish(c Catalog) { r.mu.Lock(); defer r.mu.Unlock(); r.catalogs[c.ServerID] = c }
func (r *Registry) Count(id string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.catalogs[id].Tools)
}
