package audit

import (
	"fmt"
	"os"
	"sync"
)

type Log struct {
	mu sync.Mutex
	f  *os.File
}

func Open(path string) (*Log, error) {
	if info, e := os.Stat(path); e == nil && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("audit file permissions must not grant group or other access")
	} else if e != nil && !os.IsNotExist(e) {
		return nil, fmt.Errorf("stat audit: %w", e)
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return nil, fmt.Errorf("open audit: %w", e)
	}
	l := &Log{f: f}
	if e = l.AppendReady(); e != nil {
		f.Close()
		return nil, e
	}
	return l, nil
}
func (l *Log) AppendReady() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, e := l.f.WriteString("{\"event\":\"audit_ready\"}\n"); e != nil {
		return fmt.Errorf("append audit readiness: %w", e)
	}
	if e := l.f.Sync(); e != nil {
		return fmt.Errorf("sync audit readiness: %w", e)
	}
	return nil
}
func (l *Log) Close() error { l.mu.Lock(); defer l.mu.Unlock(); return l.f.Close() }
