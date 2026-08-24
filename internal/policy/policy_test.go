package policy

import (
	"testing"

	"github.com/Rakshita-0023/conduit/internal/config"
)

func TestAllowedDenyThenAllowThenDefaultDeny(t *testing.T) {
	p, err := Compile(config.Policy{Allow: []string{"github.*", "postgres.query"}, Deny: []string{"github.delete"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"github.read": true, "github.delete": false, "postgres.query": true, "postgres.write": false} {
		if got := p.Allowed(name); got != want {
			t.Errorf("Allowed(%q)=%t want %t", name, got, want)
		}
	}
}

func TestCompileRejectsMalformedPolicy(t *testing.T) {
	if _, err := Compile(config.Policy{Allow: []string{"bad rule"}}); err == nil {
		t.Fatal("want error")
	}
}
