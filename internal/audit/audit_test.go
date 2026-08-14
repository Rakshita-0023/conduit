package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAppendsAndSyncs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		t.Fatalf("audit=%q err=%v", b, err)
	}
}

func TestOpenRejectsDirectory(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("want error")
	}
}

func TestOpenRejectsPermissiveExistingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(p, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("want permission error")
	}
}
