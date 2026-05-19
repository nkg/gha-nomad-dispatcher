// Package config loads dispatcher configuration from environment
// variables. All knobs live here so the rest of the binary stays
// configuration-source-agnostic.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config is the resolved runtime configuration. Built by Load() from
// env vars; consumers receive an immutable value.
type Config struct {
	// HTTP listen address (e.g. ":8080").
	ListenAddr string

	// Shared secret used to validate GitHub webhook signatures.
	// MUST match the secret configured in the GitHub App / webhook.
	// Required.
	WebhookSecret string

	// Base URL of the token-server LXC, e.g.
	// "http://token-server.lab:8080". Required.
	TokenServerURL string

	// Nomad cluster API endpoint, e.g. "http://nomad-server.lab:4646".
	// Required.
	NomadAddr string

	// Nomad ACL token. Optional — leave empty if Nomad ACLs are off.
	NomadToken string

	// Nomad namespace to submit runner jobs into. Defaults to "default".
	NomadNamespace string

	// OCI image for ephemeral runner containers, e.g.
	// "ghcr.io/myorg/actions-runner:latest". Required.
	RunnerImage string

	// Default labels attached to spawned runners. Comma-separated,
	// e.g. "self-hosted,linux,x64,podman". Required.
	RunnerLabels string

	// CPU + memory per ephemeral runner job. Conservative defaults
	// suit lightweight CI; bump for ML / heavy builds.
	RunnerCPU    int // MHz
	RunnerMemory int // MB
}

// Load reads config from environment variables and returns a
// validated Config. Missing required values produce a clear error
// listing every missing key.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:     envOrDefault("LISTEN_ADDR", ":8080"),
		WebhookSecret:  os.Getenv("GITHUB_WEBHOOK_SECRET"),
		TokenServerURL: os.Getenv("TOKEN_SERVER_URL"),
		NomadAddr:      os.Getenv("NOMAD_ADDR"),
		NomadToken:     os.Getenv("NOMAD_TOKEN"),
		NomadNamespace: envOrDefault("NOMAD_NAMESPACE", "default"),
		RunnerImage:    os.Getenv("RUNNER_IMAGE"),
		RunnerLabels:   os.Getenv("RUNNER_LABELS"),
	}

	var err error
	cfg.RunnerCPU, err = envInt("RUNNER_CPU_MHZ", 2000)
	if err != nil {
		return Config{}, err
	}
	cfg.RunnerMemory, err = envInt("RUNNER_MEMORY_MB", 2048)
	if err != nil {
		return Config{}, err
	}

	var missing []string
	if cfg.WebhookSecret == "" {
		missing = append(missing, "GITHUB_WEBHOOK_SECRET")
	}
	if cfg.TokenServerURL == "" {
		missing = append(missing, "TOKEN_SERVER_URL")
	}
	if cfg.NomadAddr == "" {
		missing = append(missing, "NOMAD_ADDR")
	}
	if cfg.RunnerImage == "" {
		missing = append(missing, "RUNNER_IMAGE")
	}
	if cfg.RunnerLabels == "" {
		missing = append(missing, "RUNNER_LABELS")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %v", missing)
	}
	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
