package labels

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		csv  string
		want []string // expected members, lowercased
	}{
		{"simple", "self-hosted,linux,x64", []string{"self-hosted", "linux", "x64"}},
		{"whitespace trimmed", " self-hosted , linux ", []string{"self-hosted", "linux"}},
		{"case folded", "Self-Hosted,LINUX,X64", []string{"self-hosted", "linux", "x64"}},
		{"duplicates collapse", "linux,linux,LINUX", []string{"linux"}},
		{"empty elements dropped", "linux,,x64,   ,", []string{"linux", "x64"}},
		{"empty string", "", nil},
		{"only separators", ", , ,", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.csv)
			if got.Len() != len(tt.want) {
				t.Fatalf("Parse(%q) has %d labels %v, want %d %v",
					tt.csv, got.Len(), got, len(tt.want), tt.want)
			}
			for _, w := range tt.want {
				if _, ok := got[w]; !ok {
					t.Errorf("Parse(%q) missing %q (got %v)", tt.csv, w, got)
				}
			}
		})
	}
}

func TestCanServe(t *testing.T) {
	// The real fleet's labels, so the cases below are the ones that
	// actually occur rather than invented ones.
	runner := Parse("self-hosted,linux,x64,podman,sproncy")

	tests := []struct {
		name string
		want []string
		ok   bool
	}{
		{
			// The common sproncy job: a strict subset, so both the Nomad
			// pool and the legacy docker pool can claim it.
			name: "proper subset matches",
			want: []string{"self-hosted", "linux", "x64"},
			ok:   true,
		},
		{
			name: "exact set matches",
			want: []string{"self-hosted", "linux", "x64", "podman", "sproncy"},
			ok:   true,
		},
		{
			// The case this package exists for: a legacy-pool job whose
			// `docker` label the Nomad runners do not carry.
			name: "one unmatched label is enough to refuse",
			want: []string{"self-hosted", "linux", "x64", "docker"},
			ok:   false,
		},
		{
			// A GitHub-hosted job. It produces a workflow_job webhook
			// exactly like a self-hosted one, and spawning a runner for
			// it is pure waste.
			name: "github-hosted labels refused",
			want: []string{"ubuntu-latest"},
			ok:   false,
		},
		{
			// GitHub reports the runner's own labels capitalised even
			// though they were registered lowercase.
			name: "case is ignored",
			want: []string{"self-hosted", "Linux", "X64"},
			ok:   true,
		},
		{
			name: "surrounding whitespace is ignored",
			want: []string{" self-hosted ", "linux"},
			ok:   true,
		},
		{
			// Another owner's tenant label. This is what keeps one org's
			// jobs from spawning another org's runners.
			name: "wrong tenant label refused",
			want: []string{"self-hosted", "linux", "x64", "hordialabs"},
			ok:   false,
		},
		{
			// Documented deliberate choice: no information is not
			// evidence of a mismatch, and there is no retry for a job
			// we skip. See CanServe's comment.
			name: "empty request is served",
			want: nil,
			ok:   true,
		},
		{
			name: "blank labels are ignored, not treated as unmatched",
			want: []string{"linux", "  "},
			ok:   true,
		},
		{
			name: "duplicate requested labels do not change the answer",
			want: []string{"linux", "linux", "docker"},
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runner.CanServe(tt.want); got != tt.ok {
				t.Errorf("CanServe(%q) = %v, want %v", tt.want, got, tt.ok)
			}
		})
	}
}

// An empty runner set must refuse every non-empty request. Config
// rejects this at load time, so it should be unreachable in production
// — but "the set was empty so everything matched" is the failure mode
// that would silently restore the behaviour this package removes, and
// it is worth a test that would catch it.
func TestCanServe_EmptyRunnerSetRefuses(t *testing.T) {
	empty := Parse("")
	if empty.Len() != 0 {
		t.Fatalf("precondition: Parse(\"\") should be empty, got %v", empty)
	}
	if empty.CanServe([]string{"self-hosted"}) {
		t.Error("an empty runner label set must not claim to serve [self-hosted]")
	}
	if !empty.CanServe(nil) {
		t.Error("an empty request is served regardless of the runner set")
	}
}

// Guards the direction of the subset test. A runner whose labels are a
// strict SUBSET of the job's must refuse: GitHub requires the runner to
// carry every label the job asked for, not the other way round.
// Inverting the containment check passes every other case in this file.
func TestCanServe_DirectionIsNotSymmetric(t *testing.T) {
	runner := Parse("self-hosted,linux")
	job := []string{"self-hosted", "linux", "x64", "podman"}

	if runner.CanServe(job) {
		t.Error("runner labels are a subset of the job's — must refuse")
	}
	if !Parse("self-hosted,linux,x64,podman").CanServe([]string{"self-hosted", "linux"}) {
		t.Error("runner labels are a superset of the job's — must serve")
	}
}
