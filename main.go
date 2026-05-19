// gha-nomad-dispatcher receives GitHub workflow_job webhooks and
// submits Nomad jobs that spawn ephemeral runner containers.
//
// v0.1 scope: single-tenant. One GitHub webhook secret, one
// token-server, one Nomad cluster, one runner image. Per-org / per-
// repo routing comes in a follow-up.
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
	"syscall"
	"time"

	"github.com/nkg/gha-nomad-dispatcher/internal/config"
	"github.com/nkg/gha-nomad-dispatcher/internal/nomad"
	"github.com/nkg/gha-nomad-dispatcher/internal/tokenserver"
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
		cfg:    cfg,
		tokens: tokenserver.New(cfg.TokenServerURL),
		nomad:  nomad.New(cfg.NomadAddr, cfg.NomadToken),
		log:    logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", srv.handleWebhook)
	mux.HandleFunc("GET /healthz", srv.handleHealth)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM. Webhook deliveries
	// in-flight are given 30s to finish before the process exits.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr)
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
	cfg    config.Config
	tokens *tokenserver.Client
	nomad  *nomad.Client
	log    *slog.Logger
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleWebhook ingests a single GitHub webhook delivery, validates
// its signature, and (if it's a queued workflow_job) submits a
// Nomad job to spawn an ephemeral runner for it.
//
// Returns 202 Accepted on successful dispatch; 204 No Content for
// events we deliberately ignore (non-queued actions, other event
// types); 4xx for malformed deliveries; 5xx for downstream failures.
func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	delivery := r.Header.Get(webhook.HeaderDelivery)
	log := s.log.With("delivery", delivery)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		log.Warn("body read failed", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get(webhook.HeaderSignature)
	if !webhook.ValidateSignature(sig, body, s.cfg.WebhookSecret) {
		log.Warn("signature invalid")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get(webhook.HeaderEvent)
	if event != "workflow_job" {
		// Other event types are valid deliveries but not interesting
		// to the dispatcher — ack and move on.
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

	if !ev.IsQueued() {
		log.Debug("ignoring non-queued action", "action", ev.Action)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := s.dispatch(r.Context(), log, ev); err != nil {
		log.Error("dispatch failed", "err", err)
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// dispatch handles the happy path: mint a registration token, render
// the Nomad job HCL, submit it. Returns nil on success.
func (s *server) dispatch(ctx context.Context, log *slog.Logger, ev *webhook.WorkflowJob) error {
	org := ev.Repository.Owner.Login
	repo := ev.Repository.Name
	log = log.With("repo", ev.Repository.FullName, "job_id", ev.WorkflowJob.ID)

	tok, err := s.tokens.MintRegistrationToken(ctx, org, repo)
	if err != nil {
		return fmt.Errorf("mint token: %w", err)
	}

	hcl, err := nomad.Render(nomad.RunnerJobInputs{
		JobID:        fmt.Sprintf("gha-runner-%d", ev.WorkflowJob.ID),
		Namespace:    s.cfg.NomadNamespace,
		RunnerURL:    fmt.Sprintf("https://github.com/%s", ev.Repository.FullName),
		RunnerToken:  tok.Token,
		RunnerLabels: s.cfg.RunnerLabels,
		RunnerImage:  s.cfg.RunnerImage,
		CPU:          s.cfg.RunnerCPU,
		Memory:       s.cfg.RunnerMemory,
	})
	if err != nil {
		return fmt.Errorf("render job: %w", err)
	}

	evalID, err := s.nomad.SubmitJob(ctx, hcl)
	if err != nil {
		return fmt.Errorf("submit job: %w", err)
	}
	log.Info("runner dispatched", "eval_id", evalID)
	return nil
}
