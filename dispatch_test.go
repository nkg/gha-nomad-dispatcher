package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nkg/gha-nomad-dispatcher/internal/config"
	"github.com/nkg/gha-nomad-dispatcher/internal/github"
	"github.com/nkg/gha-nomad-dispatcher/internal/nomad"
	"github.com/nkg/gha-nomad-dispatcher/internal/webhook"
)

// These exercise dispatch end to end against a fake GitHub and a fake
// Nomad, which the handler tests in main_test.go deliberately do not:
// there, every case short-circuits before dispatch. What is checked
// here is what actually reaches the jobspec — a credential that is
// minted but never passed through is indistinguishable from one that
// was never minted, and the compiler catches neither.

// fakeGitHubAPI serves the two token endpoints with distinguishable
// values, so a test cannot pass by getting the right-looking token from
// the wrong endpoint.
func fakeGitHubAPI() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_install",
			"expires_at": "2999-01-01T00:00:00Z",
		})
	})
	mux.HandleFunc("/orgs/{org}/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "REGISTRATION_TOKEN"})
	})
	mux.HandleFunc("/orgs/{org}/actions/runners/remove-token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "REMOVAL_TOKEN"})
	})
	return httptest.NewServer(mux)
}

// fakeNomad captures the HCL handed to /v1/jobs/parse, which is where
// the rendered jobspec first leaves the process.
type fakeNomad struct {
	lastHCL atomic.Value // string
}

func (f *fakeNomad) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs/parse", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			JobHCL string `json:"JobHCL"`
		}
		_ = json.Unmarshal(body, &req)
		f.lastHCL.Store(req.JobHCL)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ID": "parsed-job"})
	})
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"EvalID": "eval-1"})
	})
	return httptest.NewServer(mux)
}

func (f *fakeNomad) hcl() string {
	v, _ := f.lastHCL.Load().(string)
	return v
}

// dispatchOnce wires a server against both fakes and dispatches one
// queued job, returning the HCL Nomad was asked to parse.
func dispatchOnce(t *testing.T) string {
	t.Helper()

	gh := fakeGitHubAPI()
	t.Cleanup(gh.Close)
	fn := &fakeNomad{}
	nomadSrv := fn.server()
	t.Cleanup(nomadSrv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tenant := &github.Tenant{
		Login:          "sproncy",
		AppID:          "1",
		InstallationID: 110552520,
		PrivateKey:     key,
	}
	owner := &config.Owner{
		Login:          "sproncy",
		RepoScoped:     false,
		WebhookSecret:  "s",
		RunnerLabels:   "self-hosted,linux,x64,podman,sproncy",
		RunnerImage:    "ghcr.io/nkg/oci-actions-runner:v0.1.9",
		NomadNamespace: "sproncy",
		Tenant:         tenant,
	}
	cfg := config.Config{
		Owners:             map[string]*config.Owner{"sproncy": owner},
		DefaultCPU:         1000,
		DefaultMemory:      3072,
		DefaultIdleTimeout: 900,
	}

	srv := newServer(
		cfg,
		github.NewMinterWithBaseURL(map[string]*github.Tenant{"sproncy": tenant}, gh.URL),
		nomad.New(nomadSrv.URL, ""),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	ev := &webhook.WorkflowJob{}
	ev.Action = "queued"
	ev.WorkflowJob.ID = 12345
	ev.WorkflowJob.Labels = []string{"self-hosted", "linux", "x64"}
	ev.Repository.FullName = "sproncy/app"
	ev.Repository.Name = "app"
	ev.Repository.Owner.Login = "sproncy"
	ev.Repository.Owner.Type = "Organization"

	if err := srv.dispatch(context.Background(), srv.log, owner, ev); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	hcl := fn.hcl()
	if hcl == "" {
		t.Fatal("Nomad was never asked to parse a jobspec")
	}
	return hcl
}

// The point of the whole change: the spawned runner must receive a
// removal token, and it must be the one from the remove-token endpoint
// rather than the registration token. Passing the registration token
// through would look correct in the jobspec and fail at `config.sh
// remove` — it is single-use and already spent by config.sh at startup.
func TestDispatch_PassesRemovalTokenToTheRunner(t *testing.T) {
	hcl := dispatchOnce(t)

	if !strings.Contains(hcl, `RUNNER_REMOVE_TOKEN = "REMOVAL_TOKEN"`) {
		t.Errorf("jobspec does not carry the removal token\n--- got ---\n%s", hcl)
	}
	if strings.Contains(hcl, `RUNNER_REMOVE_TOKEN = "REGISTRATION_TOKEN"`) {
		t.Error("the registration token was passed as the removal token; `config.sh remove` would fail")
	}
	if !strings.Contains(hcl, `RUNNER_TOKEN     = "REGISTRATION_TOKEN"`) {
		t.Errorf("jobspec does not carry the registration token\n--- got ---\n%s", hcl)
	}
}

// Everything else about the dispatch keeps working, so a regression in
// the token wiring cannot hide behind a broken jobspec.
func TestDispatch_RendersTheRestOfTheJobspec(t *testing.T) {
	hcl := dispatchOnce(t)

	for _, want := range []string{
		`job "gha-runner-sproncy-12345"`,
		`namespace   = "sproncy"`,
		`RUNNER_URL       = "https://github.com/sproncy"`,
		`RUNNER_LABELS    = "self-hosted,linux,x64,podman,sproncy"`,
		`RUNNER_IDLE_TIMEOUT = "900"`,
		`image      = "ghcr.io/nkg/oci-actions-runner:v0.1.9"`,
		`memory = 3072`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("jobspec missing %q\n--- got ---\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, "@@") {
		t.Errorf("unsubstituted placeholder reached Nomad:\n%s", hcl)
	}
}

// Minting a removal token is best-effort. An owner whose removal
// endpoint fails must still get a runner — refusing to dispatch would
// trade a working job for a tidy runner list — and the job must carry
// an empty removal token rather than the placeholder or the
// registration token.
func TestDispatch_SurvivesRemovalTokenFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_install",
			"expires_at": "2999-01-01T00:00:00Z",
		})
	})
	mux.HandleFunc("/orgs/{org}/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "REGISTRATION_TOKEN"})
	})
	mux.HandleFunc("/orgs/{org}/actions/runners/remove-token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})
	gh := httptest.NewServer(mux)
	defer gh.Close()

	fn := &fakeNomad{}
	nomadSrv := fn.server()
	defer nomadSrv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tenant := &github.Tenant{Login: "sproncy", AppID: "1", InstallationID: 1, PrivateKey: key}
	owner := &config.Owner{
		Login: "sproncy", WebhookSecret: "s",
		RunnerLabels: "self-hosted", RunnerImage: "img",
		NomadNamespace: "sproncy", Tenant: tenant,
	}
	srv := newServer(
		config.Config{Owners: map[string]*config.Owner{"sproncy": owner}, DefaultCPU: 1000, DefaultMemory: 2048},
		github.NewMinterWithBaseURL(map[string]*github.Tenant{"sproncy": tenant}, gh.URL),
		nomad.New(nomadSrv.URL, ""),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	ev := &webhook.WorkflowJob{}
	ev.Action = "queued"
	ev.WorkflowJob.ID = 999
	ev.Repository.FullName = "sproncy/app"
	ev.Repository.Name = "app"
	ev.Repository.Owner.Login = "sproncy"
	ev.Repository.Owner.Type = "Organization"

	if err := srv.dispatch(context.Background(), srv.log, owner, ev); err != nil {
		t.Fatalf("dispatch must succeed without a removal token, got: %v", err)
	}
	hcl := fn.hcl()
	if !strings.Contains(hcl, `RUNNER_REMOVE_TOKEN = ""`) {
		t.Errorf("want an empty removal token\n--- got ---\n%s", hcl)
	}
	if strings.Contains(hcl, "@@") {
		t.Errorf("unsubstituted placeholder reached Nomad:\n%s", hcl)
	}
}
