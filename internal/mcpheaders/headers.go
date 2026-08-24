// Package mcpheaders implements only the private SDK x-mcp-header behavior
// needed to validate a frozen public schema and regenerate downstream headers.
package mcpheaders

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	paramPrefix  = "Mcp-Param-"
	base64Prefix = "=?base64?"
	base64Suffix = "?="
	maxSafeInt   = 1<<53 - 1
)

type property struct {
	Type       string              `json:"type"`
	Annotation json.RawMessage     `json:"x-mcp-header,omitempty"`
	Properties map[string]property `json:"properties,omitempty"`
}

type binding struct {
	path   []string
	header string
}

// ValidateCall checks the applicable standard name and parameter headers.
func ValidateCall(header http.Header, name string, args json.RawMessage, tool *mcp.Tool) error {
	if got := header.Get("Mcp-Name"); got == "" || got != name {
		return fmt.Errorf("Mcp-Name does not match tool name")
	}
	bound, err := bindings(tool)
	if err != nil {
		return err
	}
	var values map[string]json.RawMessage
	if len(args) == 0 {
		args = []byte(`{}`)
	}
	if err := json.Unmarshal(args, &values); err != nil || values == nil {
		return fmt.Errorf("tool arguments must be an object")
	}
	for _, b := range bound {
		key := paramPrefix + b.header
		raw, present := lookup(values, b.path)
		headerValue := header.Get(key)
		if !present || string(raw) == "null" {
			if headerValue != "" {
				return fmt.Errorf("unexpected %s header", key)
			}
			continue
		}
		if headerValue == "" {
			return fmt.Errorf("%s does not match tool argument", key)
		}
		decoded, ok := decode(headerValue)
		if !ok {
			return fmt.Errorf("%s does not match tool argument", key)
		}
		value, ok := primitive(raw)
		if !ok || !value.equal(decoded) {
			return fmt.Errorf("%s does not match tool argument", key)
		}
	}
	return nil
}

// Generate produces exactly the applicable Mcp-Param-* values for args.
func Generate(tool *mcp.Tool, args json.RawMessage) (http.Header, error) {
	result := make(http.Header)
	if tool == nil {
		return nil, fmt.Errorf("missing tool schema")
	}
	var values map[string]json.RawMessage
	if len(args) == 0 {
		args = []byte(`{}`)
	}
	if err := json.Unmarshal(args, &values); err != nil || values == nil {
		return nil, fmt.Errorf("tool arguments must be an object")
	}
	bound, err := bindings(tool)
	if err != nil {
		return nil, err
	}
	for _, b := range bound {
		raw, ok := lookup(values, b.path)
		if !ok || string(raw) == "null" {
			continue
		}
		value, ok := primitive(raw)
		if !ok {
			return nil, fmt.Errorf("header-bound argument %q is not a supported primitive", strings.Join(b.path, "."))
		}
		result.Set(paramPrefix+b.header, encode(value.text))
	}
	return result, nil
}

type primitiveValue struct {
	text    string
	integer *big.Int
}

func (p primitiveValue) equal(header string) bool {
	if p.integer == nil {
		return header == p.text
	}
	other, ok := safeInteger(header)
	return ok && other.Cmp(p.integer) == 0
}

func primitive(raw json.RawMessage) (primitiveValue, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return primitiveValue{}, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return primitiveValue{}, false
	}
	switch value := value.(type) {
	case string:
		return primitiveValue{text: value}, true
	case bool:
		return primitiveValue{text: strconv.FormatBool(value)}, true
	case json.Number:
		integer, ok := safeInteger(value.String())
		if !ok {
			return primitiveValue{}, false
		}
		return primitiveValue{text: integer.String(), integer: integer}, true
	default:
		return primitiveValue{}, false
	}
}

func safeInteger(value string) (*big.Int, bool) {
	rat, ok := new(big.Rat).SetString(value)
	if !ok || !rat.IsInt() {
		return nil, false
	}
	integer := rat.Num()
	maximum := big.NewInt(maxSafeInt)
	if integer.Cmp(maximum) > 0 || integer.Cmp(new(big.Int).Neg(maximum)) < 0 {
		return nil, false
	}
	return new(big.Int).Set(integer), true
}

func decode(value string) (string, bool) {
	if encoded, ok := strings.CutPrefix(value, base64Prefix); ok {
		if encoded, ok = strings.CutSuffix(encoded, base64Suffix); ok {
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return "", false
			}
			return string(decoded), true
		}
	}
	return value, true
}

func bindings(tool *mcp.Tool) ([]binding, error) {
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("encode input schema: %w", err)
	}
	var root property
	if json.Unmarshal(data, &root) != nil {
		return nil, fmt.Errorf("invalid input schema")
	}
	var out []binding
	seen := map[string]bool{}
	var walk func(map[string]property, []string) error
	walk = func(properties map[string]property, prefix []string) error {
		for name, p := range properties {
			path := append(append([]string(nil), prefix...), name)
			if p.Annotation != nil {
				var header string
				if json.Unmarshal(p.Annotation, &header) != nil || header == "" || !validHeader(header) || (p.Type != "string" && p.Type != "integer" && p.Type != "boolean") {
					return fmt.Errorf("invalid x-mcp-header annotation")
				}
				key := strings.ToLower(header)
				if seen[key] {
					return fmt.Errorf("duplicate x-mcp-header annotation")
				}
				seen[key] = true
				out = append(out, binding{path: path, header: header})
			}
			if err := walk(p.Properties, path); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root.Properties, nil); err != nil {
		return nil, err
	}
	return out, nil
}

func validHeader(value string) bool {
	for _, c := range value {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return value != ""
}

func lookup(values map[string]json.RawMessage, path []string) (json.RawMessage, bool) {
	var raw json.RawMessage
	var ok bool
	for i, part := range path {
		if i == 0 {
			raw, ok = values[part]
		} else {
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) != nil {
				return nil, false
			}
			raw, ok = nested[part]
		}
		if !ok {
			return nil, false
		}
	}
	return raw, true
}

func encode(value string) string {
	if value == "" {
		return value
	}
	unsafe := value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t'
	for _, c := range value {
		unsafe = unsafe || c < 0x20 || c > 0x7e
	}
	if strings.HasPrefix(value, base64Prefix) && strings.HasSuffix(value, base64Suffix) {
		unsafe = true
	}
	if !unsafe {
		return value
	}
	return base64Prefix + base64.StdEncoding.EncodeToString([]byte(value)) + base64Suffix
}
