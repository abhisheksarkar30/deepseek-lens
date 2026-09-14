package sink

import (
	"context"
	"sync"
	"testing"
	"time"
)

func newCall() *CapturedCall {
	return &CapturedCall{StartedAt: time.Now(), Method: "POST", Path: "/v1/chat/completions"}
}

func TestSubmitEmptySink(t *testing.T) {
	s := New(16)
	if ok := s.Submit(newCall()); !ok {
		t.Fatal("Submit on empty sink returned false, want true")
	}
	accepted, dropped := s.Stats()
	if accepted != 1 || dropped != 0 {
		t.Fatalf("Stats() = (%d, %d), want (1, 0)", accepted, dropped)
	}
}

func TestSubmitFillsToCapacityWithoutConsumer(t *testing.T) {
	const capacity = 64
	s := New(capacity)
	for i := 0; i < capacity; i++ {
		if ok := s.Submit(newCall()); !ok {
			t.Fatalf("Submit #%d returned false, want true (consumer stopped, still under capacity)", i)
		}
	}
	accepted, dropped := s.Stats()
	if accepted != capacity || dropped != 0 {
		t.Fatalf("Stats() = (%d, %d), want (%d, 0)", accepted, dropped, capacity)
	}
}

func TestSubmitDropsOverflowWithoutConsumer(t *testing.T) {
	const capacity = 64
	const overflow = 500
	s := New(capacity)

	start := time.Now()
	for i := 0; i < capacity+overflow; i++ {
		ok := s.Submit(newCall())
		want := i < capacity
		if ok != want {
			t.Fatalf("Submit #%d returned %v, want %v", i, ok, want)
		}
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("submitting %d calls to a stopped consumer took %v, want < 1s", capacity+overflow, elapsed)
	}

	accepted, dropped := s.Stats()
	if accepted != capacity {
		t.Fatalf("accepted = %d, want %d", accepted, capacity)
	}
	if dropped != overflow {
		t.Fatalf("dropped = %d, want %d", dropped, overflow)
	}
}

func TestStatsUnderConcurrentSubmit(t *testing.T) {
	const goroutines = 100
	const perGoroutine = 50
	s := New(200) // smaller than total submissions, so some drop

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				s.Submit(newCall())
			}
		}()
	}
	wg.Wait()

	accepted, dropped := s.Stats()
	total := accepted + dropped
	want := uint64(goroutines * perGoroutine)
	if total != want {
		t.Fatalf("accepted+dropped = %d, want %d", total, want)
	}
}

func TestSubmitNeverBlocks(t *testing.T) {
	const capacity = 64
	s := New(capacity)

	done := make(chan struct{})
	go func() {
		for i := 0; i < capacity*10; i++ {
			s.Submit(newCall())
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Submit loop did not complete within 2s — Submit appears to be blocking")
	}
}

func TestCloseTerminatesDrainRange(t *testing.T) {
	s := New(16)
	for i := 0; i < 5; i++ {
		s.Submit(newCall())
	}

	ch := s.Drain(context.Background())
	s.Close()

	got := 0
	done := make(chan struct{})
	go func() {
		for range ch {
			got++
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("range over Drain's channel did not terminate after Close")
	}
	if got != 5 {
		t.Fatalf("drained %d calls, want 5", got)
	}
}

func TestConcurrentSubmitAndConsume(t *testing.T) {
	const goroutines = 100
	const perGoroutine = 200
	s := New(256)

	consumed := 0
	consumerDone := make(chan struct{})
	go func() {
		for range s.Drain(context.Background()) {
			consumed++
		}
		close(consumerDone)
	}()

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				s.Submit(newCall())
			}
		}()
	}
	wg.Wait()
	s.Close()

	select {
	case <-consumerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not finish after Close")
	}

	accepted, dropped := s.Stats()
	if uint64(consumed) != accepted {
		t.Fatalf("consumed %d, accepted %d — should match exactly", consumed, accepted)
	}
	if accepted+dropped != uint64(goroutines*perGoroutine) {
		t.Fatalf("accepted+dropped = %d, want %d", accepted+dropped, goroutines*perGoroutine)
	}
}

// TestSubmitAllocs verifies Submit itself does not add allocations beyond
// what producing the ID string requires — no extra goroutines, slices, or
// maps sneak in on the send path.
func TestSubmitAllocs(t *testing.T) {
	s := New(1) // no consumer; most calls drop, which is fine for this check
	call := newCall()

	allocs := testing.AllocsPerRun(1000, func() {
		s.Submit(call)
	})
	if allocs > 1 {
		t.Fatalf("Submit allocates %v times per call, want <= 1 (just the ID string)", allocs)
	}
}
