// Package labels answers one question: could a runner this dispatcher
// is about to spawn actually claim the job that caused it to be
// spawned?
//
// GitHub assigns a queued job to any runner whose label set is a
// SUPERSET of the job's `runs-on` labels. The dispatcher spawns one
// runner per queued workflow_job, so without this check it spawns
// runners for jobs they can never claim: a job asking for
// `[self-hosted, linux, x64, docker]` on a fleet whose runners announce
// `[self-hosted, linux, x64, podman, sproncy]` gets a runner that sits
// at "Listening for Jobs" until its idle timeout, having consumed a
// scheduling slot for nothing. Jobs targeting GitHub-hosted runners
// (`runs-on: ubuntu-latest`) are the same shape and just as common.
package labels

import "strings"

// Set is a normalised (lowercased, de-duplicated, whitespace-trimmed)
// collection of runner labels.
type Set map[string]struct{}

// Parse reads the comma-separated form used by config's
// `runner_labels` and by the runner image's RUNNER_LABELS env var.
// Empty and whitespace-only elements are dropped rather than becoming
// an empty-string label that can never match.
func Parse(csv string) Set {
	s := Set{}
	for _, raw := range strings.Split(csv, ",") {
		if l := normalise(raw); l != "" {
			s[l] = struct{}{}
		}
	}
	return s
}

// CanServe reports whether a runner carrying this label set could claim
// a job requesting `want`, using GitHub's own superset rule.
//
// An EMPTY `want` returns true, which is the deliberate choice of the
// two available mistakes. A queued workflow_job always carries at least
// one label in practice, so an empty list means the payload was not
// what we assumed — and the dispatcher has no retry: it fires once, on
// the `queued` delivery, and nothing re-examines a job that stays
// queued. Wrongly skipping therefore strands that job until a human
// notices, while wrongly dispatching costs one runner slot for the idle
// timeout. Waste is recoverable; stranding is not.
func (s Set) CanServe(want []string) bool {
	for _, raw := range want {
		l := normalise(raw)
		if l == "" {
			// Same reasoning as an empty `want`: a label we cannot
			// interpret is not evidence that the runner cannot serve it.
			continue
		}
		if _, ok := s[l]; !ok {
			return false
		}
	}
	return true
}

// Len is the number of distinct labels, so callers can reject a
// configured label string that parsed to nothing.
func (s Set) Len() int { return len(s) }

// normalise folds case and trims surrounding whitespace. Case matters
// because GitHub is inconsistent about it in its own payloads: a
// runner registers `linux,x64` and the API reports them back as
// `Linux,X64`, while `workflow_job.labels` echoes whatever the workflow
// author typed. GitHub's matching ignores case, so ours must too.
func normalise(l string) string {
	return strings.ToLower(strings.TrimSpace(l))
}
