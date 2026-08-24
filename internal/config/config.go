package config

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listener Listener     `yaml:"listener"`
	Audit    Audit        `yaml:"audit"`
	Policy   Policy       `yaml:"policy"`
	Limits   Limits       `yaml:"limits"`
	Servers  []Downstream `yaml:"downstreams"`
}
type Listener struct {
	Address        string   `yaml:"address"`
	AllowedOrigins []string `yaml:"allowed_origins"`
}
type Audit struct {
	Path string `yaml:"path"`
}
type Policy struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}
type Limits struct {
	MaxPagesPerDownstream     int           `yaml:"max_pages_per_downstream"`
	MaxToolsPerDownstream     int           `yaml:"max_tools_per_downstream"`
	MaxDownstreamCatalogBytes int64         `yaml:"max_downstream_catalog_bytes"`
	MaxAggregateTools         int           `yaml:"max_aggregate_tools"`
	MaxAggregateResponseBytes int64         `yaml:"max_aggregate_response_bytes"`
	MaxToolResponseBytes      int64         `yaml:"max_tool_response_bytes"`
	CatalogRefreshInterval    time.Duration `yaml:"catalog_refresh_interval"`
	RequestTimeout            time.Duration `yaml:"request_timeout"`
	ToolCallTimeout           time.Duration `yaml:"tool_call_timeout"`
}
type Downstream struct {
	ID      string            `yaml:"id"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("decode config: multiple YAML documents are not supported")
		}
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
func (c *Config) Validate() error {
	if c.Listener.Address == "" {
		c.Listener.Address = "127.0.0.1:8080"
	}
	if !loopbackAddr(c.Listener.Address) {
		return fmt.Errorf("listener.address must be a loopback host:port")
	}
	for _, origin := range c.Listener.AllowedOrigins {
		if err := validOrigin(origin); err != nil {
			return err
		}
	}
	if c.Audit.Path == "" || filepath.Clean(c.Audit.Path) == "." {
		return fmt.Errorf("audit.path is required")
	}
	if c.Limits.MaxPagesPerDownstream <= 0 || c.Limits.MaxToolsPerDownstream <= 0 || c.Limits.MaxDownstreamCatalogBytes <= 0 || c.Limits.MaxAggregateTools <= 0 || c.Limits.MaxAggregateResponseBytes <= 0 || c.Limits.MaxToolResponseBytes <= 0 {
		return fmt.Errorf("catalog limits must be positive")
	}
	if c.Limits.CatalogRefreshInterval <= 0 {
		return fmt.Errorf("catalog_refresh_interval must be positive")
	}
	if c.Limits.RequestTimeout < 0 {
		return fmt.Errorf("request_timeout must not be negative")
	}
	if c.Limits.RequestTimeout == 0 {
		c.Limits.RequestTimeout = 10 * time.Second
	}
	if c.Limits.ToolCallTimeout <= 0 {
		return fmt.Errorf("tool_call_timeout must be positive")
	}
	if len(c.Servers) == 0 {
		return fmt.Errorf("at least one downstream is required")
	}
	seen := map[string]bool{}
	for _, s := range c.Servers {
		if s.ID == "" || seen[s.ID] {
			return fmt.Errorf("downstream IDs must be non-empty and unique")
		}
		seen[s.ID] = true
		u, e := url.Parse(s.URL)
		if e != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil {
			return fmt.Errorf("downstream %q has invalid URL", s.ID)
		}
	}
	for _, rule := range append(append([]string{}, c.Policy.Allow...), c.Policy.Deny...) {
		if !validRule(rule) {
			return fmt.Errorf("invalid policy rule %q", rule)
		}
	}
	return nil
}
func loopbackAddr(s string) bool {
	h, _, e := net.SplitHostPort(s)
	if e != nil {
		return false
	}
	return h == "127.0.0.1" || h == "::1"
}
func validOrigin(s string) error {
	if s == "null" {
		return fmt.Errorf("Origin null is invalid")
	}
	u, e := url.Parse(s)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("invalid allowed origin %q", s)
	}
	h := u.Hostname()
	if h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return fmt.Errorf("allowed origin must be loopback: %q", s)
	}
	return nil
}
func validRule(s string) bool {
	return s != "" && !strings.ContainsAny(s, " \t\n") && (strings.HasSuffix(s, ".*") || !strings.Contains(s, "*"))
}
