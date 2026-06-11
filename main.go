// gha-nomad-dispatcher receives GitHub workflow_job webhooks and
// submits Nomad jobs that spawn ephemeral runner containers.
//
// v0.2 scope: multi-tenant. One dispatcher serves many owners (orgs
// and personal user accounts). Each owner is its own GitHub App with
// its own webhook secret, selected by the /webhook/{owner} path; the
// dispatcher mints registration tokens itself (org-scoped for orgs,
// repo-scoped for user accounts) and submits into the owner's Nomad
// namespace with the owner's labels.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nkg/gha-nomad-dispatcher/internal/config"
	"github.com/nkg/gha-nomad-dispatcher/internal/github"
	"github.com/nkg/gha-nomad-dispatcher/internal/nomad"
	"github.com/nkg/gha-nomad-dispatcher/internal/webhook"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	srv := &server{
		cfg:   cfg,
		mint:  github.NewMinter(cfg.Tenants),
		nomad: nomad.New(cfg.NomadAddr, cfg.NomadToken),
		log:   logger,
	}

	mux := http.NewServeMux()
	// Per-owner path: the path segment selects which owner's webhook
	// secret validates the delivery — chosen before the body is parsed,
	// since each owner's GitHub App signs with its own secret.
	mux.HandleFunc("POST /webhook/{owner}", srv.handleWebhook)
	mux.HandleFunc("GET /healthz", srv.handleHealth)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr, "owners", len(cfg.Owners))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown requested")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	logger.Info("bye")
}

type server struct {
	cfg   config.Config
	mint  *github.Minter
	nomad *nomad.Client
	log   *slog.Logger
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleWebhook ingests a single GitHub webhook delivery for the owner
// named in the path, validates its signature against that owner's
// secret, and (if it's a queued workflow_job) submits a Nomad job to
// spawn an ephemeral runner.
//
// 202 on dispatch; 204 for deliveries we ignore; 4xx for malformed or
// misrouted deliveries; 5xx for downstream failures.
func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ownerParam := strings.ToLower(r.PathValue("owner"))
	delivery := r.Header.Get(webhook.HeaderDelivery)
	log := s.log.With("delivery", delivery, "owner", ownerParam)

	owner, ok := s.cfg.Owners[ownerParam]
	if !ok {
		log.Warn("unknown owner in path")
		http.Error(w, "unknown owner", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		log.Warn("body read failed", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get(webhook.HeaderSignature)
	if !webhook.ValidateSignature(sig, body, owner.WebhookSecret) {
		log.Warn("signature invalid")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get(webhook.HeaderEvent)
	if event != "workflow_job" {
		log.Debug("ignoring event", "event", event)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ev, err := webhook.ParseWorkflowJob(body)
	if err != nil {
		log.Warn("payload parse failed", "err", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// Defence in depth: the validated body must belong to the owner the
	// path (and thus the secret) claimed. A mismatch means a delivery
	// was routed to the wrong owner path — refuse rather than dispatch
	// it under the wrong tenant.
	if strings.ToLower(ev.OwnerLogin()) != ownerParam {
		log.Warn("payload owner does not match path", "payload_owner", ev.OwnerLogin())
		http.Error(w, "owner mismatch", http.StatusBadRequest)
		return
	}

	if !ev.IsQueued() {
		log.Debug("ignoring non-queued action", "action", ev.Action)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := s.dispatch(r.Context(), log, owner, ev); err != nil {
		log.Error("dispatch failed", "err", err)
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// dispatch mints a registration token for the owner, renders the Nomad
// job HCL, and submits it. Org owners get an org-level runner; user
// owners get a repo-scoped one (they have no account-level runner pool).
func (s *server) dispatch(ctx context.Context, log *slog.Logger, owner *config.Owner, ev *webhook.WorkflowJob) error {
	log = log.With("repo", ev.Repository.FullName, "job_id", ev.WorkflowJob.ID)

	tok, err := s.mint.RegistrationToken(ctx, owner.Login, ev.RepoName(), owner.RepoScoped)
	if err != nil {
		return fmt.Errorf("mint token: %w", err)
	}

	runnerURL := "https://github.com/" + ev.Repository.Owner.Login
	if owner.RepoScoped {
		runnerURL = "https://github.com/" + ev.Repository.FullName
	}

	hcl, err := nomad.Render(nomad.RunnerJobInputs{
		JobID:        fmt.Sprintf("gha-runner-%s-%d", owner.Login, ev.WorkflowJob.ID),
		Namespace:    owner.NomadNamespace,
		RunnerURL:    runnerURL,
		RunnerToken:  tok,
		RunnerLabels: owner.RunnerLabels,
		RunnerImage:  owner.RunnerImage,
		CPU:          s.cfg.DefaultCPU,
		Memory:       s.cfg.DefaultMemory,
	})
	if err != nil {
		return fmt.Errorf("render job: %w", err)
	}

	evalID, err := s.nomad.SubmitJob(ctx, hcl)
	if err != nil {
		return fmt.Errorf("submit job: %w", err)
	}
	log.Info("runner dispatched", "eval_id", evalID, "namespace", owner.NomadNamespace, "repo_scoped", owner.RepoScoped)
	return nil
}
