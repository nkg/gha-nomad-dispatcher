// Package config loads dispatcher configuration from a JSON file.
//
// v0.2 is multi-tenant: one dispatcher serves many owners (orgs and
// personal user accounts), each with its own GitHub App, webhook
// secret, runner labels, and Nomad namespace. A flat env-var surface
// no longer fits that shape, so config now reads a JSON file
// (CONFIG_PATH, default /etc/gha-dispatcher/config.json) rendered by
// the deploying composition from SOPS-decrypted material.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nkg/gha-nomad-dispatcher/internal/github"
)

// DefaultConfigPath is used when CONFIG_PATH is unset.
const DefaultConfigPath = "/etc/gha-dispatcher/config.json"

// Owner is the resolved runtime configuration for one tenant. Built
// from a fileOwner plus the global defaults.
type Owner struct {
	Login string // lowercased

	// RepoScoped is true for personal user accounts, which have no
	// account-level runner pool and must register runners per-repo.
	RepoScoped bool

	// WebhookSecret validates this owner's webhook deliveries. Each
	// owner is its own GitHub App with its own secret, selected by the
	// /webhook/{owner} path before the body is parsed.
	WebhookSecret string

	// RunnerLabels announced by spawned runners (comma-separated).
	RunnerLabels string

	// RunnerImage to spawn — the owner override if set, else the global
	// default.
	RunnerImage string

	// NomadNamespace to submit this owner's runner jobs into.
	NomadNamespace string

	// Tenant carries the GitHub App credentials used to mint tokens.
	Tenant *github.Tenant
}

// Config is the resolved runtime configuration.
type Config struct {
	ListenAddr    string
	NomadAddr     string
	NomadToken    string
	DefaultCPU    int // MHz
	DefaultMemory int // MB

	// Owners is keyed by lowercased login.
	Owners map[string]*Owner

	// Tenants is the login→tenant map handed to github.NewMinter.
	Tenants map[string]*github.Tenant
}

// ── on-disk schema ──────────────────────────────────────────────────

type fileConfig struct {
	ListenAddr string       `json:"listen_addr"`
	NomadAddr  string       `json:"nomad_addr"`
	NomadToken string       `json:"nomad_token"`
	Defaults   fileDefaults `json:"defaults"`
	Owners     []fileOwner  `json:"owners"`
}

type fileDefaults struct {
	RunnerImage    string `json:"runner_image"`
	RunnerCPUMHz   int    `json:"runner_cpu_mhz"`
	RunnerMemoryMB int    `json:"runner_memory_mb"`
}

type fileOwner struct {
	Login          string `json:"login"`
	Type           string `json:"type"` // "organization" | "user"
	AppID          string `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	PrivateKeyPath string `json:"private_key_path"`
	WebhookSecret  string `json:"webhook_secret"`
	RunnerLabels   string `json:"runner_labels"`
	NomadNamespace string `json:"nomad_namespace"`
	RunnerImage    string `json:"runner_image"` // optional per-owner override
}

// Load reads and validates the config file named by CONFIG_PATH
// (default DefaultConfigPath).
func Load() (Config, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = DefaultConfigPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	return parse(data)
}

func parse(data []byte) (Config, error) {
	var fc fileConfig
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fc); err != nil {
		return Config{}, fmt.Errorf("parsing config JSON: %w", err)
	}

	cfg := Config{
		ListenAddr:    orDefault(fc.ListenAddr, ":8080"),
		NomadAddr:     fc.NomadAddr,
		NomadToken:    fc.NomadToken,
		DefaultCPU:    orDefaultInt(fc.Defaults.RunnerCPUMHz, 2000),
		DefaultMemory: orDefaultInt(fc.Defaults.RunnerMemoryMB, 2048),
		Owners:        map[string]*Owner{},
		Tenants:       map[string]*github.Tenant{},
	}

	if cfg.NomadAddr == "" {
		return Config{}, fmt.Errorf("nomad_addr is required")
	}
	if fc.Defaults.RunnerImage == "" {
		return Config{}, fmt.Errorf("defaults.runner_image is required")
	}
	if len(fc.Owners) == 0 {
		return Config{}, fmt.Errorf("at least one owner must be configured")
	}

	for i, fo := range fc.Owners {
		owner, tenant, err := resolveOwner(fo, fc.Defaults.RunnerImage)
		if err != nil {
			return Config{}, fmt.Errorf("owners[%d] (%q): %w", i, fo.Login, err)
		}
		if _, dup := cfg.Owners[owner.Login]; dup {
			return Config{}, fmt.Errorf("duplicate owner %q", owner.Login)
		}
		cfg.Owners[owner.Login] = owner
		cfg.Tenants[owner.Login] = tenant
	}
	return cfg, nil
}

func resolveOwner(fo fileOwner, defaultImage string) (*Owner, *github.Tenant, error) {
	login := strings.ToLower(strings.TrimSpace(fo.Login))
	if login == "" {
		return nil, nil, fmt.Errorf("login is required")
	}

	var repoScoped bool
	switch strings.ToLower(strings.TrimSpace(fo.Type)) {
	case "organization", "org":
		repoScoped = false
	case "user":
		repoScoped = true
	default:
		return nil, nil, fmt.Errorf("type must be \"organization\" or \"user\", got %q", fo.Type)
	}

	if strings.TrimSpace(fo.AppID) == "" {
		return nil, nil, fmt.Errorf("app_id is required")
	}
	if fo.InstallationID <= 0 {
		return nil, nil, fmt.Errorf("installation_id is required")
	}
	if fo.WebhookSecret == "" {
		return nil, nil, fmt.Errorf("webhook_secret is required")
	}
	if fo.RunnerLabels == "" {
		return nil, nil, fmt.Errorf("runner_labels is required")
	}
	if fo.PrivateKeyPath == "" {
		return nil, nil, fmt.Errorf("private_key_path is required")
	}

	keyData, err := os.ReadFile(fo.PrivateKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading private_key_path: %w", err)
	}
	key, err := github.ParsePrivateKeyPEM(keyData)
	if err != nil {
		return nil, nil, fmt.Errorf("private key: %w", err)
	}

	namespace := strings.TrimSpace(fo.NomadNamespace)
	if namespace == "" {
		namespace = "default"
	}
	image := strings.TrimSpace(fo.RunnerImage)
	if image == "" {
		image = defaultImage
	}

	tenant := &github.Tenant{
		Login:          login,
		AppID:          strings.TrimSpace(fo.AppID),
		InstallationID: fo.InstallationID,
		PrivateKey:     key,
	}
	owner := &Owner{
		Login:          login,
		RepoScoped:     repoScoped,
		WebhookSecret:  fo.WebhookSecret,
		RunnerLabels:   fo.RunnerLabels,
		RunnerImage:    image,
		NomadNamespace: namespace,
		Tenant:         tenant,
	}
	return owner, tenant, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func orDefaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
