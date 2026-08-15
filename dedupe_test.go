package main

import (
	"sync"
	"testing"
)

func TestDedupeClaimIsExclusive(t *testing.T) {
	d := newDedupe(16)

	if !d.claim(42) {
		t.Fatal("first claim(42) = false, want true")
	}
	if d.claim(42) {
		t.Error("second claim(42) = true, want false")
	}
	if !d.claim(43) {
		t.Error("claim(43) = false, want true — a different id must be independent")
	}
}

func TestDedupeReleaseAllowsRetry(t *testing.T) {
	d := newDedupe(16)

	d.claim(1)
	d.release(1)
	if !d.claim(1) {
		t.Error("claim after release = false, want true — a failed dispatch must be retryable")
	}
}

func TestDedupeReleaseOfUnknownIDIsSafe(t *testing.T) {
	d := newDedupe(16)
	d.release(999) // must not panic or corrupt state
	if !d.claim(999) {
		t.Error("claim(999) = false, want true")
	}
}

// The dedupe must not grow without bound: past capacity the oldest
// claims are evicted, which is the deliberate trade (a redundant
// runner in a pathological case, versus unbounded memory).
func TestDedupeEvictsOldestPastCapacity(t *testing.T) {
	const capacity = 4
	d := newDedupe(capacity)

	for id := int64(1); id <= 6; id++ {
		d.claim(id)
	}

	if got := len(d.seen); got != capacity {
		t.Errorf("len(seen) = %d, want %d", got, capacity)
	}
	if got := len(d.order); got != capacity {
		t.Errorf("len(order) = %d, want %d", got, capacity)
	}
	// 1 and 2 were evicted, so they are claimable again.
	if !d.claim(1) {
		t.Error("claim(1) = false, want true — should have been evicted")
	}
	// 6 is still the most recent and must remain claimed.
	if d.claim(6) {
		t.Error("claim(6) = true, want false — most recent entry must be retained")
	}
}

func TestDedupeConcurrentClaimsElectOneWinner(t *testing.T) {
	d := newDedupe(64)

	const goroutines = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if d.claim(7) {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("concurrent claims on one id produced %d winners, want exactly 1", wins)
	}
}
