package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/conduit-mcp/conduit/internal/config"
	"sort"
	"strings"
)

type Compiled struct{ allow, deny []string }

func Compile(p config.Policy) (Compiled, error) {
	for _, r := range append(append([]string{}, p.Allow...), p.Deny...) {
		if r == "" || strings.ContainsAny(r, " \t\n") {
			return Compiled{}, fmt.Errorf("invalid rule")
		}
	}
	return Compiled{p.Allow, p.Deny}, nil
}
func (p Compiled) Allowed(name string) bool {
	for _, r := range p.deny {
		if match(r, name) {
			return false
		}
	}
	for _, r := range p.allow {
		if match(r, name) {
			return true
		}
	}
	return false
}
func (p Compiled) Digest() string {
	allow := append([]string(nil), p.allow...)
	deny := append([]string(nil), p.deny...)
	sort.Strings(allow)
	sort.Strings(deny)
	h := sha256.New()
	for _, group := range [][]string{allow, deny} {
		for _, rule := range group {
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(rule))
		}
		_, _ = h.Write([]byte{1})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
func match(rule, name string) bool {
	return rule == name || strings.HasSuffix(rule, ".*") && strings.HasPrefix(name, strings.TrimSuffix(rule, "*"))
}
