package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkg/gha-nomad-dispatcher/internal/config"
	"github.com/nkg/gha-nomad-dispatcher/internal/github"
	"github.com/nkg/gha-nomad-dispatcher/internal/nomad"
)

// These tests exercise handleWebhook's routing and validation, all of
// which short-circuit BEFORE dispatch — so they need no working minter
// or Nomad. The dispatch happy-path components (token minting, HCL
// render, job submit) are covered in their own packages.

func testServer(secret string) *server {
	return &server{
		cfg: config.Config{
			Owners: map[string]*config.Owner{
				"sproncy": {
					Login:          "sproncy",
					RepoScoped:     false,
					WebhookSecret:  secret,
					RunnerLabels:   "self-hosted,sproncy",
					RunnerImage:    "img",
					NomadNamespace: "sproncy",
				},
			},
		},
		mint:  github.NewMinter(map[string]*github.Tenant{}),
		nomad: nomad.New("http://127.0.0.1:1", ""),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func post(t *testing.T, srv *server, ownerPath, event, sig, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/{owner}", srv.handleWebhook)
	req := httptest.NewRequest(http.MethodPost, "/webhook/"+ownerPath, strings.NewReader(body))
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

const queuedSproncy = `{"action":"queued","workflow_job":{"id":1,"status":"queued"},` +
	`"repository":{"full_name":"sproncy/app","name":"app","owner":{"login":"sproncy","type":"Organization"}}}`

func TestHandleWebhook_UnknownOwner(t *testing.T) {
	srv := testServer("s3cr3t")
	body := queuedSproncy
	rec := post(t, srv, "nobody", "workflow_job", sign("s3cr3t", []byte(body)), body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleWebhook_BadSignature(t *testing.T) {
	srv := testServer("s3cr3t")
	body := queuedSproncy
	rec := post(t, srv, "sproncy", "workflow_job", sign("WRONG", []byte(body)), body)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestHandleWebhook_OwnerMismatch(t *testing.T) {
	srv := testServer("s3cr3t")
	// Validly signed with sproncy's secret, but the payload claims a
	// different owner — must be refused, not dispatched under sproncy.
	body := `{"action":"queued","workflow_job":{"id":1,"status":"queued"},` +
		`"repository":{"full_name":"hordialabs/app","name":"app","owner":{"login":"hordialabs","type":"Organization"}}}`
	rec := post(t, srv, "sproncy", "workflow_job", sign("s3cr3t", []byte(body)), body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleWebhook_IgnoresNonWorkflowJob(t *testing.T) {
	srv := testServer("s3cr3t")
	body := queuedSproncy
	rec := post(t, srv, "sproncy", "push", sign("s3cr3t", []byte(body)), body)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestHandleWebhook_IgnoresNonQueued(t *testing.T) {
	srv := testServer("s3cr3t")
	body := `{"action":"completed","workflow_job":{"id":1,"status":"completed"},` +
		`"repository":{"full_name":"sproncy/app","name":"app","owner":{"login":"sproncy","type":"Organization"}}}`
	rec := post(t, srv, "sproncy", "workflow_job", sign("s3cr3t", []byte(body)), body)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
