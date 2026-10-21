package cb_test

import (
	"sync"
	"testing"
	"time"

	"llm-gateway/internal/cb"
)

func TestCB_AllowWhenClosed(t *testing.T) {
	breaker := cb.NewCB(3, time.Second)
	if !breaker.Allow() {
		t.Fatal("expected Allow()=true on fresh circuit breaker")
	}
}

func TestCB_TripOnThreshold(t *testing.T) {
	breaker := cb.NewCB(3, time.Second)

	breaker.RecordFailure()
	breaker.RecordFailure()
	if !breaker.Allow() {
		t.Fatal("should still be CLOSED after 2 failures (threshold=3)")
	}

	breaker.RecordFailure() // 3rd failure trips it
	if breaker.Allow() {
		t.Fatal("should be OPEN after 3 failures")
	}
}

func TestCB_BlockWhenOpen(t *testing.T) {
	breaker := cb.NewCB(1, 10*time.Second)
	breaker.RecordFailure() // trip immediately (threshold=1)

	for i := 0; i < 5; i++ {
		if breaker.Allow() {
			t.Fatalf("Allow() should return false when OPEN (attempt %d)", i+1)
		}
	}
}

func TestCB_HalfOpenAfterCooldown(t *testing.T) {
	breaker := cb.NewCB(1, 5*time.Millisecond)
	breaker.RecordFailure()

	if breaker.Allow() {
		t.Fatal("should be blocked immediately after trip")
	}

	time.Sleep(10 * time.Millisecond) // outlast cooldown

	if !breaker.Allow() {
		t.Fatal("should allow one probe request after cooldown (HALF_OPEN)")
	}
}

func TestCB_SuccessFromHalfOpenResetsToClose(t *testing.T) {
	breaker := cb.NewCB(1, 5*time.Millisecond)
	breaker.RecordFailure()
	time.Sleep(10 * time.Millisecond)

	breaker.Allow()         // transitions to HALF_OPEN
	breaker.RecordSuccess() // should reset to CLOSED

	// Now all subsequent requests should be allowed freely
	for i := 0; i < 5; i++ {
		if !breaker.Allow() {
			t.Fatalf("should be CLOSED after success, Allow() failed on attempt %d", i+1)
		}
	}
}

func TestCB_FailureInHalfOpenReturnsToOpen(t *testing.T) {
	breaker := cb.NewCB(1, 5*time.Millisecond)
	breaker.RecordFailure()
	time.Sleep(10 * time.Millisecond)

	breaker.Allow()         // transitions to HALF_OPEN
	breaker.RecordFailure() // back to OPEN

	if breaker.Allow() {
		t.Fatal("should be OPEN again after failure in HALF_OPEN")
	}
}

func TestCB_SuccessResetsFailureCount(t *testing.T) {
	breaker := cb.NewCB(3, time.Second)

	breaker.RecordFailure()
	breaker.RecordFailure()
	breaker.RecordSuccess() // reset count

	// should need 3 more failures to trip again
	breaker.RecordFailure()
	breaker.RecordFailure()
	if !breaker.Allow() {
		t.Fatal("should still be CLOSED — success reset the counter")
	}

	breaker.RecordFailure()
	if breaker.Allow() {
		t.Fatal("should be OPEN after 3 failures post-reset")
	}
}

// TestCB_ConcurrentAccess does not assert a specific outcome —
// run with -race to verify no data races under concurrent load.
func TestCB_ConcurrentAccess(t *testing.T) {
	breaker := cb.NewCB(5, 10*time.Millisecond)
	var wg sync.WaitGroup

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			breaker.Allow()
			breaker.RecordFailure()
			breaker.RecordSuccess()
		}()
	}
	wg.Wait()
}

func TestCB_HalfOpenOnlyAllowsOneProbe(t *testing.T) {
	breaker := cb.NewCB(1, 5*time.Millisecond)
	breaker.RecordFailure()
	time.Sleep(10 * time.Millisecond)

	// First call after cooldown — should allow (probe)
	if !breaker.Allow() {
		t.Fatal("first Allow() after cooldown should return true")
	}
	// Second concurrent call — probe already in flight, should block
	if breaker.Allow() {
		t.Fatal("second Allow() in HALF_OPEN should return false — probe already in flight")
	}
}

func BenchmarkAllow(b *testing.B) {
	breaker := cb.NewCB(5, 10*time.Second)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		breaker.Allow()
	}
}

func BenchmarkAllow_Parallel(b *testing.B) {
	breaker := cb.NewCB(5, 10*time.Second)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			breaker.Allow()
		}
	})
}
