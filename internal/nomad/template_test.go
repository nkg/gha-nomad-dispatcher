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
