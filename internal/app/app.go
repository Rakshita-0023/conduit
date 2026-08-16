package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/conduit-mcp/conduit/internal/audit"
	"github.com/conduit-mcp/conduit/internal/catalog"
	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/health"
	"github.com/conduit-mcp/conduit/internal/ingress"
	"github.com/conduit-mcp/conduit/internal/policy"
	"github.com/conduit-mcp/conduit/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type App struct {
	Config      config.Config
	Health      *health.State
	Registry    *registry.Registry
	Audit       *audit.Log
	Server      *http.Server
	Listener    net.Listener
	cancel      context.CancelFunc
	refreshWG   sync.WaitGroup
	refreshDone chan struct{}
	publishMu   sync.Mutex
	// testBeforePublicationLock and testAfterPublicationLock are nil in
	// production. They provide deterministic synchronization around the real
	// publication critical section in package tests.
	testBeforePublicationLock func()
	testAfterPublicationLock  func()
}

func Start(ctx context.Context, c config.Config, build string) (*App, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := c.Validate(); e != nil {
		return nil, e
	}
	compiled, e := policy.Compile(c.Policy)
	if e != nil {
		return nil, e
	}
	a, e := audit.Open(c.Audit.Path)
	if e != nil {
		return nil, e
	}
	ids := make([]string, len(c.Servers))
	for i, s := range c.Servers {
		ids[i] = s.ID
	}
	h := health.New(ids)
	impl := mcp.Implementation{Name: "conduit", Version: build}
	r := registry.New(c.Limits, compiled, impl)
	in := ingress.New(c, h, r, build)
	listener, e := net.Listen("tcp", c.Listener.Address)
	if e != nil {
		_ = a.Close()
		return nil, fmt.Errorf("bind listener: %w", e)
	}
	if e := ctx.Err(); e != nil {
		_ = listener.Close()
		_ = a.Close()
		return nil, e
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	app := &App{Config: c, Health: h, Registry: r, Audit: a, Server: ingress.HTTPServer(c.Listener.Address, in.Handler()), Listener: listener, cancel: cancel, refreshDone: make(chan struct{})}
	h.SetLive(true)
	for _, s := range c.Servers {
		app.refreshWG.Add(1)
		go app.refreshLoop(refreshCtx, s)
	}
	go func() { app.refreshWG.Wait(); close(app.refreshDone) }()
	return app, nil
}

func (a *App) refreshLoop(ctx context.Context, server config.Downstream) {
	defer a.refreshWG.Done()
	a.refreshOnce(ctx, server)
	ticker := time.NewTicker(a.Config.Limits.CatalogRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshOnce(ctx, server)
		}
	}
}

func (a *App) refreshOnce(parent context.Context, server config.Downstream) {
	ctx, cancel := context.WithTimeout(parent, a.Config.Limits.RequestTimeout)
	defer cancel()
	cat, err := catalog.Refresh(ctx, server, a.Config.Limits)

	// Different downstream refreshes may complete concurrently. Keep registry
	// publication and its matching health transition in one ordered critical
	// section so health cannot be overwritten by an older snapshot.
	if a.testBeforePublicationLock != nil {
		a.testBeforePublicationLock()
	}
	a.publishMu.Lock()
	defer a.publishMu.Unlock()
	if a.testAfterPublicationLock != nil {
		a.testAfterPublicationLock()
	}
	current := a.Registry.Snapshot()
	a.Health.SetAggregate(current.Generation, string(registry.StateUnavailable), 0)
	if err != nil {
		a.Health.SetServer(server.ID, "degraded", 0, "catalog refresh failed")
		snapshot := a.Registry.Remove(server.ID)
		a.Health.SetAggregate(snapshot.Generation, string(snapshot.State), snapshot.ToolCount)
		return
	}
	snapshot, err := a.Registry.Publish(cat)
	if err != nil {
		a.Health.SetServer(server.ID, "degraded", 0, "catalog refresh failed")
		snapshot = a.Registry.Remove(server.ID)
		a.Health.SetAggregate(snapshot.Generation, string(snapshot.State), snapshot.ToolCount)
		return
	}
	a.Health.SetServer(server.ID, "healthy", len(cat.Tools), "")
	a.Health.SetAggregate(snapshot.Generation, string(snapshot.State), snapshot.ToolCount)
}
func (a *App) Close(ctx context.Context) error {
	a.cancel()
	a.Health.SetLive(false)
	e := a.Server.Shutdown(ctx)
	select {
	case <-a.refreshDone:
	case <-ctx.Done():
		if e == nil {
			e = ctx.Err()
		}
	}
	if le := a.Listener.Close(); le != nil && !errors.Is(le, net.ErrClosed) && e == nil {
		e = fmt.Errorf("close listener: %w", le)
	}
	ae := a.Audit.Close()
	if e != nil {
		return e
	}
	if ae != nil {
		return fmt.Errorf("close audit: %w", ae)
	}
	return nil
}
