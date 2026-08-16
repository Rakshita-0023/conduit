package health

import (
	"sync"
	"testing"
)

func TestReadinessRequiresAllInitialAttemptsAndOneHealthy(t *testing.T) {
	s := New([]string{"healthy", "failed"})
	s.SetLive(true)
	s.SetAggregate(1, "ready", 0)
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
	s.SetAggregate(1, "ready", 0)
	s.SetServer("x", "healthy", 1, "")
	if !s.Snapshot().Ready {
		t.Fatal("want ready while live")
	}
	s.SetLive(false)
	if s.Snapshot().Ready {
		t.Fatal("ready while not live")
	}
}

func TestReadinessRequiresUsableAggregate(t *testing.T) {
	s := New([]string{"x"})
	s.SetLive(true)
	s.SetServer("x", "healthy", 1, "")
	for _, state := range []string{"starting", "unavailable", "aggregate_collision", "aggregate_over_limit"} {
		s.SetAggregate(1, state, 0)
		if s.Snapshot().Ready {
			t.Fatalf("ready with aggregate state %q", state)
		}
	}
	s.SetAggregate(1, "ready", 0)
	if !s.Snapshot().Ready {
		t.Fatal("empty ready aggregate should be ready")
	}
}

func TestAggregateGenerationNeverRegresses(t *testing.T) {
	s := New([]string{"a", "b"})
	s.SetLive(true)
	s.SetServer("a", "healthy", 1, "")
	s.SetServer("b", "healthy", 1, "")
	s.SetAggregate(2, "aggregate_collision", 0)
	s.SetAggregate(1, "ready", 2)
	status := s.Snapshot()
	if status.Aggregate.Generation != 2 || status.Aggregate.State != "aggregate_collision" || status.Ready {
		t.Fatalf("status=%+v", status)
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
