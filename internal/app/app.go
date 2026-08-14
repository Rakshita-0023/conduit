package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/conduit-mcp/conduit/internal/audit"
	"github.com/conduit-mcp/conduit/internal/catalog"
	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/health"
	"github.com/conduit-mcp/conduit/internal/ingress"
	"github.com/conduit-mcp/conduit/internal/policy"
	"github.com/conduit-mcp/conduit/internal/registry"
)

type App struct {
	Config   config.Config
	Health   *health.State
	Registry *registry.Registry
	Audit    *audit.Log
	Server   *http.Server
	Listener net.Listener
	cancel   context.CancelFunc
}

func Start(ctx context.Context, c config.Config, build string) (*App, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if _, e := policy.Compile(c.Policy); e != nil {
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
	r := registry.New()
	in := ingress.New(c, h, build)
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
	app := &App{Config: c, Health: h, Registry: r, Audit: a, Server: ingress.HTTPServer(c.Listener.Address, in.Handler()), Listener: listener, cancel: cancel}
	h.SetLive(true)
	for _, s := range c.Servers {
		go func(s config.Downstream) {
			ctx, cancel := context.WithTimeout(refreshCtx, c.Limits.RequestTimeout)
			defer cancel()
			cat, e := catalog.Refresh(ctx, s, c.Limits)
			if e != nil {
				h.SetServer(s.ID, "degraded", 0, "catalog refresh failed")
				return
			}
			r.Publish(cat)
			h.SetServer(s.ID, "healthy", len(cat.Tools), "")
		}(s)
	}
	return app, nil
}
func (a *App) Close(ctx context.Context) error {
	a.cancel()
	a.Health.SetLive(false)
	e := a.Server.Shutdown(ctx)
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
