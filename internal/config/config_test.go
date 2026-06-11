package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKey generates an RSA key, writes it as PKCS#1 PEM into dir, and
// returns the path.
func writeKey(t *testing.T, dir string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	p := filepath.Join(dir, "key.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(p, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return p
}

// loadJSON writes body to a temp config file, points CONFIG_PATH at it,
// and calls Load.
func loadJSON(t *testing.T, body string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("CONFIG_PATH", p)
	return Load()
}

func TestLoad_OrgAndUser(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeKey(t, dir)

	body := fmt.Sprintf(`{
      "nomad_addr": "http://nomad:4646",
      "defaults": { "runner_image": "ghcr.io/nkg/oci-actions-runner:v0.1.0" },
      "owners": [
        {
          "login": "Sproncy",
          "type": "organization",
          "app_id": "2879772",
          "installation_id": 110552520,
          "private_key_path": %q,
          "webhook_secret": "sproncy-secret",
          "runner_labels": "self-hosted,linux,x64,podman,sproncy",
          "nomad_namespace": "sproncy"
        },
        {
          "login": "nkg",
          "type": "user",
          "app_id": "Iv23liExample",
          "installation_id": 222,
          "private_key_path": %q,
          "webhook_secret": "nkg-secret",
          "runner_labels": "self-hosted,linux,x64,podman,nkg"
        }
      ]
    }`, keyPath, keyPath)

	cfg, err := loadJSON(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("default ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.DefaultCPU != 2000 || cfg.DefaultMemory != 2048 {
		t.Errorf("defaults cpu=%d mem=%d", cfg.DefaultCPU, cfg.DefaultMemory)
	}

	// Login is lowercased into the map key.
	sproncy, ok := cfg.Owners["sproncy"]
	if !ok {
		t.Fatal("sproncy owner missing")
	}
	if sproncy.RepoScoped {
		t.Error("org owner should not be repo-scoped")
	}
	if sproncy.NomadNamespace != "sproncy" {
		t.Errorf("namespace = %q", sproncy.NomadNamespace)
	}
	if sproncy.RunnerImage != "ghcr.io/nkg/oci-actions-runner:v0.1.0" {
		t.Errorf("image not defaulted: %q", sproncy.RunnerImage)
	}

	nkg, ok := cfg.Owners["nkg"]
	if !ok {
		t.Fatal("nkg owner missing")
	}
	if !nkg.RepoScoped {
		t.Error("user owner should be repo-scoped")
	}
	if nkg.NomadNamespace != "default" {
		t.Errorf("nkg namespace = %q, want default", nkg.NomadNamespace)
	}
	if nkg.Tenant == nil || nkg.Tenant.AppID != "Iv23liExample" {
		t.Errorf("tenant not wired for nkg: %+v", nkg.Tenant)
	}
	if len(cfg.Tenants) != 2 {
		t.Errorf("Tenants len = %d, want 2", len(cfg.Tenants))
	}
}

func TestLoad_RejectsUnknownType(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeKey(t, dir)
	body := fmt.Sprintf(`{
      "nomad_addr": "http://n:4646",
      "defaults": { "runner_image": "img" },
      "owners": [{ "login": "x", "type": "enterprise", "app_id": "1",
        "installation_id": 1, "private_key_path": %q,
        "webhook_secret": "s", "runner_labels": "l" }]
    }`, keyPath)
	_, err := loadJSON(t, body)
	if err == nil || !strings.Contains(err.Error(), "organization") {
		t.Fatalf("want type error, got %v", err)
	}
}

func TestLoad_MissingRequiredOwnerFields(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeKey(t, dir)
	body := fmt.Sprintf(`{
      "nomad_addr": "http://n:4646",
      "defaults": { "runner_image": "img" },
      "owners": [{ "login": "x", "type": "user", "app_id": "1",
        "installation_id": 1, "private_key_path": %q,
        "webhook_secret": "", "runner_labels": "l" }]
    }`, keyPath)
	_, err := loadJSON(t, body)
	if err == nil || !strings.Contains(err.Error(), "webhook_secret") {
		t.Fatalf("want webhook_secret error, got %v", err)
	}
}

func TestLoad_MissingNomadAddr(t *testing.T) {
	body := `{ "defaults": { "runner_image": "img" }, "owners": [] }`
	_, err := loadJSON(t, body)
	if err == nil || !strings.Contains(err.Error(), "nomad_addr") {
		t.Fatalf("want nomad_addr error, got %v", err)
	}
}

func TestLoad_DuplicateOwner(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeKey(t, dir)
	owner := fmt.Sprintf(`{ "login": "dup", "type": "user", "app_id": "1",
      "installation_id": 1, "private_key_path": %q,
      "webhook_secret": "s", "runner_labels": "l" }`, keyPath)
	body := fmt.Sprintf(`{ "nomad_addr": "http://n:4646",
      "defaults": { "runner_image": "img" }, "owners": [%s, %s] }`, owner, owner)
	_, err := loadJSON(t, body)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestLoad_RejectsUnknownField(t *testing.T) {
	body := `{ "nomad_addr": "http://n:4646", "bogus": true,
      "defaults": { "runner_image": "img" }, "owners": [] }`
	_, err := loadJSON(t, body)
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}
