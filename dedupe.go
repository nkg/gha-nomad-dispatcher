package main

import "sync"

// dedupe remembers which workflow_job IDs have already been dispatched,
// so a redelivered webhook doesn't spawn a second runner for one job.
//
// GitHub redelivers a webhook when the endpoint doesn't answer inside
// its delivery timeout — which the dispatcher can exceed on a cold
// token cache, since minting walks two GitHub API calls before Nomad
// is even contacted. Without this guard the redelivery submits a
// second Nomad job; GitHub then hands the second runner a different
// job or none at all, and the fleet burns an allocation on an idle
// container.
//
// Bounded by capacity: once full, the oldest entry is evicted. A job
// that is still queued after `capacity` further dispatches is
// vanishingly unlikely, and the failure mode of an eviction (one
// redundant runner) is exactly the behaviour before this existed.
type dedupe struct {
	mu       sync.Mutex
	capacity int
	seen     map[int64]struct{}
	order    []int64 // insertion order, for FIFO eviction
}

func newDedupe(capacity int) *dedupe {
	return &dedupe{capacity: capacity, seen: make(map[int64]struct{})}
}

// claim records id and reports whether the caller is the first to do
// so. A false return means another delivery already owns this job and
// the caller should not dispatch.
func (d *dedupe) claim(id int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, dup := d.seen[id]; dup {
		return false
	}
	d.seen[id] = struct{}{}
	d.order = append(d.order, id)

	if len(d.order) > d.capacity {
		delete(d.seen, d.order[0])
		d.order = d.order[1:]
	}
	return true
}

// release forgets id so a later redelivery is allowed to retry.
// Called when a dispatch fails — GitHub's redelivery is the retry
// mechanism, and holding the claim would suppress it.
func (d *dedupe) release(id int64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seen[id]; !ok {
		return
	}
	delete(d.seen, id)
	for i, v := range d.order {
		if v == id {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
}
