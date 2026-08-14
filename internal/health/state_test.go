package health

import (
	"sync"
	"testing"
)

func TestReadinessRequiresAllInitialAttemptsAndOneHealthy(t *testing.T) {
	s := New([]string{"healthy", "failed"})
	s.SetLive(true)
	s.SetServer("healthy", "healthy", 2, "")
	if s.Snapshot().Ready {
		t.Fatal("ready before every initial attempt")
	}
	s.SetServer("failed", "degraded", 0, "sanitized")
	if !s.Snapshot().Ready {
		t.Fatal("want ready after partial failure")
	}
	s.SetAudit(false)
	if s.Snapshot().Ready {
		t.Fatal("audit failure must make unready")
	}
}

func TestNotReadyWhenNotLive(t *testing.T) {
	s := New([]string{"x"})
	s.SetLive(true)
	s.SetServer("x", "healthy", 1, "")
	if !s.Snapshot().Ready {
		t.Fatal("want ready while live")
	}
	s.SetLive(false)
	if s.Snapshot().Ready {
		t.Fatal("ready while not live")
	}
}
func TestConcurrentSnapshots(t *testing.T) {
	s := New([]string{"a"})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.SetServer("a", "healthy", j, "")
				_ = s.Snapshot()
			}
		}()
	}
	wg.Wait()
}
