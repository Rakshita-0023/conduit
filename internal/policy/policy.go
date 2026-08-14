package policy

import (
	"fmt"
	"github.com/conduit-mcp/conduit/internal/config"
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
func match(rule, name string) bool {
	return rule == name || strings.HasSuffix(rule, ".*") && strings.HasPrefix(name, strings.TrimSuffix(rule, "*"))
}
