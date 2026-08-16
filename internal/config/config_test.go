package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func valid() Config {
	return Config{Listener: Listener{Address: "127.0.0.1:8080", AllowedOrigins: []string{"http://localhost:3000"}}, Audit: Audit{Path: "audit.jsonl"}, Limits: Limits{MaxPagesPerDownstream: 2, MaxToolsPerDownstream: 2, MaxDownstreamCatalogBytes: 1024, MaxAggregateTools: 2, MaxAggregateResponseBytes: 1024, CatalogRefreshInterval: time.Minute}, Servers: []Downstream{{ID: "one", URL: "http://127.0.0.1:9000/mcp"}}}
}
func TestValidate(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"valid": func(*Config) {}, "duplicate": func(c *Config) { c.Servers = append(c.Servers, c.Servers[0]) }, "listener": func(c *Config) { c.Listener.Address = "0.0.0.0:8080" }, "origin": func(c *Config) { c.Listener.AllowedOrigins = []string{"https://example.com:443"} }, "policy": func(c *Config) { c.Policy.Allow = []string{"bad*rule"} }, "audit": func(c *Config) { c.Audit.Path = "" }, "aggregate tools": func(c *Config) { c.Limits.MaxAggregateTools = 0 }, "aggregate bytes": func(c *Config) { c.Limits.MaxAggregateResponseBytes = 0 }, "refresh interval": func(c *Config) { c.Limits.CatalogRefreshInterval = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid()
			mutate(&c)
			err := c.Validate()
			if name == "valid" && err != nil {
				t.Fatal(err)
			}
			if name != "valid" && err == nil {
				t.Fatal("want error")
			}
		})
	}
}
func TestLoadRejectsUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("listener:\n  address: 127.0.0.1:8080\nunknown: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("want unknown key error")
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	b := []byte("listener:\n  address: 127.0.0.1:8080\n---\nunknown: true\n")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("want multiple-document error")
	}
}

func TestValidateRejectsNegativeTimeout(t *testing.T) {
	c := valid()
	c.Limits.RequestTimeout = -time.Second
	if err := c.Validate(); err == nil {
		t.Fatal("want timeout error")
	}
}

func TestLoadValid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	b := []byte("listener:\n  address: 127.0.0.1:8080\naudit:\n  path: audit.jsonl\nlimits:\n  max_pages_per_downstream: 1\n  max_tools_per_downstream: 1\n  max_downstream_catalog_bytes: 1024\n  max_aggregate_tools: 1\n  max_aggregate_response_bytes: 1024\n  catalog_refresh_interval: 1m\n  request_timeout: 1s\ndownstreams:\n  - id: one\n    url: http://127.0.0.1:9000/mcp\n")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
}
