package config

import (
	"strings"
	"testing"
)

// Each test sets the required env vars via t.Setenv so the test
// table stays declarative and parallel-safe.

func TestLoad_AllRequired(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "secret")
	t.Setenv("TOKEN_SERVER_URL", "http://token-server:8080")
	t.Setenv("NOMAD_ADDR", "http://nomad:4646")
	t.Setenv("RUNNER_IMAGE", "ghcr.io/example/runner:latest")
	t.Setenv("RUNNER_LABELS", "self-hosted,linux")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WebhookSecret != "secret" {
		t.Errorf("WebhookSecret = %q", cfg.WebhookSecret)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("default ListenAddr = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.NomadNamespace != "default" {
		t.Errorf("default NomadNamespace = %q, want default", cfg.NomadNamespace)
	}
	if cfg.RunnerCPU != 2000 {
		t.Errorf("default RunnerCPU = %d, want 2000", cfg.RunnerCPU)
	}
	if cfg.RunnerMemory != 2048 {
		t.Errorf("default RunnerMemory = %d, want 2048", cfg.RunnerMemory)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	// Deliberately set only one of the required env vars; Load
	// should refuse and list the missing keys in the error.
	t.Setenv("GITHUB_WEBHOOK_SECRET", "secret")
	// (others unset)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when required env vars missing")
	}
	for _, key := range []string{"TOKEN_SERVER_URL", "NOMAD_ADDR", "RUNNER_IMAGE", "RUNNER_LABELS"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention missing key %s: %v", key, err)
		}
	}
}

func TestLoad_ResourceOverrides(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "secret")
	t.Setenv("TOKEN_SERVER_URL", "http://t:80")
	t.Setenv("NOMAD_ADDR", "http://n:4646")
	t.Setenv("RUNNER_IMAGE", "img")
	t.Setenv("RUNNER_LABELS", "label")
	t.Setenv("RUNNER_CPU_MHZ", "4000")
	t.Setenv("RUNNER_MEMORY_MB", "8192")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunnerCPU != 4000 || cfg.RunnerMemory != 8192 {
		t.Errorf("overrides not applied: cpu=%d mem=%d", cfg.RunnerCPU, cfg.RunnerMemory)
	}
}
