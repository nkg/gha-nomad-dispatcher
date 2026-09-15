package nomad

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	out, err := Render(RunnerJobInputs{
		JobID:        "gha-runner-12345",
		Namespace:    "default",
		RunnerURL:    "https://github.com/owner/repo",
		RunnerToken:  "abc123token",
		RunnerLabels: "self-hosted,linux,podman",
		RunnerImage:  "ghcr.io/example/runner:latest",
		CPU:          2000,
		Memory:       2048,
		IdleTimeout:  900,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Spot-check each substitution landed.
	checks := []string{
		`job "gha-runner-12345"`,
		`namespace   = "default"`,
		`RUNNER_URL       = "https://github.com/owner/repo"`,
		`RUNNER_TOKEN     = "abc123token"`,
		`RUNNER_LABELS    = "self-hosted,linux,podman"`,
		`image      = "ghcr.io/example/runner:latest"`,
		`cpu    = 2000`,
		`memory = 2048`,
		`RUNNER_IDLE_TIMEOUT = "900"`,
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, out)
		}
	}

	// Sanity: no leftover placeholders.
	if strings.Contains(out, "@@") {
		t.Errorf("unsubstituted placeholder remains in output:\n%s", out)
	}
}

func TestRender_MissingJobID(t *testing.T) {
	_, err := Render(RunnerJobInputs{Namespace: "default"})
	if err == nil {
		t.Errorf("expected error when JobID is empty")
	}
}

// The zero value must render as "0" rather than an empty string or a
// stray placeholder: the runner image treats 0 as "wait forever", which
// is the pre-existing behaviour, and an empty value would be ambiguous
// to anyone reading a submitted job.
func TestRenderIdleTimeoutZero(t *testing.T) {
	out, err := Render(RunnerJobInputs{
		JobID:        "gha-runner-1",
		Namespace:    "default",
		RunnerURL:    "https://github.com/o/r",
		RunnerToken:  "t",
		RunnerLabels: "self-hosted",
		RunnerImage:  "img",
		CPU:          1000,
		Memory:       2048,
		IdleTimeout:  0,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, `RUNNER_IDLE_TIMEOUT = "0"`) {
		t.Errorf("zero IdleTimeout should render as \"0\"\n--- got ---\n%s", out)
	}
	if strings.Contains(out, "@@") {
		t.Errorf("unsubstituted placeholder remains\n--- got ---\n%s", out)
	}
}
