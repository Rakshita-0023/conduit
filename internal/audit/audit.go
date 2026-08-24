package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type Log struct {
	mu     sync.Mutex
	f      *os.File
	failed bool
}

type Event struct {
	Event              string `json:"event"`
	Timestamp          string `json:"timestamp"`
	CallID             string `json:"call_id,omitempty"`
	PublicTool         string `json:"public_tool,omitempty"`
	ServerID           string `json:"server_id,omitempty"`
	DownstreamToolName string `json:"downstream_tool_name,omitempty"`
	RegistryGeneration uint64 `json:"registry_generation,omitempty"`
	PolicyDigest       string `json:"policy_digest,omitempty"`
	Outcome            string `json:"outcome,omitempty"`
	DurationMS         int64  `json:"duration_ms,omitempty"`
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
	return l.Append(Event{Event: "audit_ready"})
}

func (l *Log) Append(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed || l.f == nil {
		return fmt.Errorf("audit unavailable")
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	b = append(b, '\n')
	n, err := l.f.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = l.f.Sync()
	}
	if err != nil {
		l.failed = true
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func (l *Log) Available() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.failed && l.f != nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	l.failed = true
	return err
}
