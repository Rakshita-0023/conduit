package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/conduit-mcp/conduit/internal/config"
	"github.com/conduit-mcp/conduit/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const protocolVersion = "2026-07-28"

type meter struct {
	max int64
	n   atomic.Int64
}

// boundedTransport is the narrow boundary around the SDK HTTP client. It
// prevents redirects and legacy requests from reaching downstreams, and bounds
// every response before the SDK reads it.
type boundedTransport struct {
	base    http.RoundTripper
	max     int64
	m       *meter
	headers map[string]string
	ctx     context.Context
}

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(t.ctx)
	for key, value := range t.headers {
		if isMCPRoutingHeader(key) {
			return nil, fmt.Errorf("configured MCP routing headers are not permitted")
		}
		r.Header.Set(key, value)
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("MCP-Protocol-Version") != protocolVersion {
			return nil, fmt.Errorf("non-2026 MCP request blocked")
		}
		method := r.Header.Get("Mcp-Method")
		if method != "server/discover" && method != "tools/list" {
			return nil, fmt.Errorf("unsupported downstream MCP request blocked")
		}
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	var total *meter
	if r.Header.Get("Mcp-Method") == "tools/list" {
		total = t.m
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, max: t.max, total: total}
	if r.Header.Get("Mcp-Method") != "tools/list" {
		return resp, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	resp.Body.Close()
	if err := validateToolHeaders(body); err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func isMCPRoutingHeader(key string) bool {
	key = strings.ToLower(key)
	return key == "mcp-protocol-version" || key == "mcp-method" || key == "mcp-name" || strings.HasPrefix(key, "mcp-param-")
}

type boundedBody struct {
	io.ReadCloser
	max, used int64
	total     *meter
}

func (b *boundedBody) Read(p []byte) (int, error) {
	remaining := b.max - b.used
	if remaining < 0 {
		return 0, fmt.Errorf("downstream response exceeds byte limit")
	}
	if int64(len(p)) > remaining+1 {
		p = p[:remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.used += int64(n)
	if b.total != nil && n > 0 && b.total.n.Add(int64(n)) > b.total.max {
		return n, fmt.Errorf("catalog response exceeds byte limit")
	}
	if b.used > b.max {
		return n, fmt.Errorf("downstream response exceeds byte limit")
	}
	return n, err
}

type headerSchemaProperty struct {
	Type       string                          `json:"type"`
	XMCPHeader json.RawMessage                 `json:"x-mcp-header,omitempty"`
	Properties map[string]headerSchemaProperty `json:"properties,omitempty"`
}

// validateToolHeaders mirrors only the SDK's pre-list filtering rule. The SDK
// otherwise silently drops invalid x-mcp-header tools, which would make a
// partial catalog indistinguishable from a complete one.
func validateToolHeaders(body []byte) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Result) == 0 {
		return nil // Let the SDK report malformed JSON-RPC responses.
	}
	var result struct {
		Tools []struct {
			InputSchema any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil // Let the SDK report malformed tools/list results.
	}
	for _, tool := range result.Tools {
		var schema headerSchemaProperty
		data, err := json.Marshal(tool.InputSchema)
		if err != nil || json.Unmarshal(data, &schema) != nil {
			continue // This matches the SDK's no-annotations behavior.
		}
		if err := validateHeaderProperties(schema.Properties, map[string]bool{}); err != nil {
			return fmt.Errorf("invalid downstream tool header annotation")
		}
	}
	return nil
}

func validateHeaderProperties(properties map[string]headerSchemaProperty, seen map[string]bool) error {
	for _, property := range properties {
		if property.XMCPHeader != nil {
			if property.Type != "string" && property.Type != "integer" && property.Type != "boolean" {
				return errors.New("invalid annotation type")
			}
			var header string
			if json.Unmarshal(property.XMCPHeader, &header) != nil || header == "" || !isHeaderToken(header) {
				return errors.New("invalid annotation header")
			}
			key := strings.ToLower(header)
			if seen[key] {
				return errors.New("duplicate annotation header")
			}
			seen[key] = true
		}
		if err := validateHeaderProperties(property.Properties, seen); err != nil {
			return err
		}
	}
	return nil
}

func isHeaderToken(value string) bool {
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	return true
}

func Refresh(ctx context.Context, s config.Downstream, lim config.Limits) (registry.Catalog, error) {
	m := &meter{max: lim.MaxDownstreamCatalogBytes}
	hc := &http.Client{
		Timeout:   lim.RequestTimeout,
		Transport: boundedTransport{base: http.DefaultTransport, max: lim.MaxDownstreamCatalogBytes, m: m, headers: s.Headers, ctx: ctx},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("downstream redirects are not permitted")
		},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "conduit", Version: "0.1.0"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if e != nil {
		return registry.Catalog{}, fmt.Errorf("connect downstream %q: %w", s.ID, e)
	}
	defer session.Close()
	if init := session.InitializeResult(); init == nil || init.ProtocolVersion != protocolVersion {
		return registry.Catalog{}, fmt.Errorf("downstream %q does not support MCP 2026-07-28", s.ID)
	}
	var all []*mcp.Tool
	seen := map[string]bool{}
	cursor := ""
	for page := 1; ; page++ {
		if page > lim.MaxPagesPerDownstream {
			return registry.Catalog{}, fmt.Errorf("catalog page limit exceeded")
		}
		res, e := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if e != nil {
			return registry.Catalog{}, fmt.Errorf("list tools: %w", e)
		}
		for _, t := range res.Tools {
			if t == nil || t.Name == "" || t.InputSchema == nil {
				return registry.Catalog{}, fmt.Errorf("malformed tool definition")
			}
			all = append(all, t)
			if len(all) > lim.MaxToolsPerDownstream {
				return registry.Catalog{}, fmt.Errorf("catalog tool limit exceeded")
			}
		}
		if res.NextCursor == "" {
			break
		}
		if seen[res.NextCursor] {
			return registry.Catalog{}, fmt.Errorf("repeated opaque cursor")
		}
		seen[res.NextCursor] = true
		cursor = res.NextCursor
	}
	return registry.Catalog{ServerID: s.ID, Tools: all}, nil
}
