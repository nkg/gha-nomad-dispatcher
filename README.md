# gha-nomad-dispatcher

Receives GitHub `workflow_job` webhooks, validates them, and submits
[Nomad](https://www.nomadproject.io/) jobs that spawn ephemeral
podman containers running the GitHub Actions runner. Effectively
"ARC without Kubernetes" for a Nomad-backed self-hosted runner
platform.

Designed for the
[nkg/terraform-proxmox-fleet](https://github.com/nkg/terraform-proxmox-fleet)
+ [nkg/distrobuilder-proxmox-lxc-images](https://github.com/nkg/distrobuilder-proxmox-lxc-images)
stack: the dispatcher lives in a small unprivileged LXC; Nomad runs
across the fleet; runner workloads land as podman containers on the
Nomad client LXCs.

## v0.1 scope (current)

- **Single-tenant.** One GitHub webhook secret, one token-server, one Nomad cluster, one runner image.
- **`workflow_job: queued` only.** Other actions / event types are acknowledged and ignored — Nomad handles the runner-job lifecycle on its own.
- **Repo-scoped runners.** Registers against `https://github.com/{owner}/{repo}`. Org-scoped support comes later.
- **No Nomad ACLs required** (but supported via `NOMAD_TOKEN`).

Multi-tenant routing, org-scoped runners, per-org runner images,
and metrics land in follow-up PRs.

## Architecture

```
       GitHub
         │  workflow_job.queued
         ▼
    /webhook (HMAC-validated)
         │
   gha-nomad-dispatcher  ─►  token-server LXC ─► GitHub App API
         │                       (mints runner registration token)
         ▼
    Nomad API  ─►  Nomad client LXCs
                       │
                       ▼  podman driver
              ephemeral runner container
              (--ephemeral --once, registers with GitHub, runs the job, exits)
```

## Configuration

Everything via env vars:

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_WEBHOOK_SECRET` | yes | — | Shared secret for `X-Hub-Signature-256` validation |
| `TOKEN_SERVER_URL` | yes | — | Base URL of the token-server, e.g. `http://token-server.lab:8080` |
| `NOMAD_ADDR` | yes | — | Nomad API endpoint, e.g. `http://nomad-server.lab:4646` |
| `NOMAD_TOKEN` | no | (empty) | Nomad ACL token (if Nomad ACLs enabled) |
| `NOMAD_NAMESPACE` | no | `default` | Nomad namespace for spawned runner jobs |
| `RUNNER_IMAGE` | yes | — | OCI image to spawn, e.g. `ghcr.io/myorg/actions-runner:latest` |
| `RUNNER_LABELS` | yes | — | Comma-separated runner labels, e.g. `self-hosted,linux,x64,podman` |
| `RUNNER_CPU_MHZ` | no | `2000` | Nomad CPU resource (MHz) per runner |
| `RUNNER_MEMORY_MB` | no | `2048` | Nomad memory resource (MB) per runner |
| `LISTEN_ADDR` | no | `:8080` | HTTP listen address |

## Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/webhook` | GitHub webhook ingestion. Returns `202` on dispatch, `204` on ignored event, `401` on signature failure, `400` on malformed payload, `500` on downstream failure. |
| `GET` | `/healthz` | Liveness probe. |

## Build + run

```bash
mise install            # Go 1.24 + golangci-lint + lefthook
go build -o gha-nomad-dispatcher .
```

```bash
export GITHUB_WEBHOOK_SECRET=...
export TOKEN_SERVER_URL=http://token-server.lab:8080
export NOMAD_ADDR=http://nomad-server.lab:4646
export RUNNER_IMAGE=ghcr.io/myorg/actions-runner:latest
export RUNNER_LABELS=self-hosted,linux,x64,podman
./gha-nomad-dispatcher
```

## Tests

```bash
go test ./...               # all packages
go test -race ./...         # race detector (same as CI)
go test -cover ./...        # coverage summary
```

CI runs `go vet`, `go test -race`, `go build`, and `golangci-lint`
on every PR + push to main.

## Design notes

### No `hashicorp/nomad/api` dependency

The dispatcher talks to Nomad via its raw HTTP API (`/v1/jobs/parse`
→ `/v1/jobs`) rather than vendoring `github.com/hashicorp/nomad/api`.
The Nomad client surface we need is tiny (parse HCL, submit job,
read evaluation ID), and the upstream package pulls in a heavy graph
of vendored deps that bloats the binary considerably for almost no
gain. The raw HTTP approach keeps the binary under 10 MB
statically-linked.

### Job spec is HCL, embedded at build time

`internal/nomad/runner.nomad.hcl` is the canonical job template,
embedded with `//go:embed`. Substitution is plain string replacement
(`@@FIELD@@` placeholders) — every input is a known fixed identifier
with no escaping needs. If the template grows conditionals or loops,
swap to `text/template` at that point.

### Token-server is on-trust

The dispatcher talks to the token-server over plain HTTP on the
same VLAN inside the fleet firewall. No mTLS today. Adding mTLS is
straightforward when the threat model warrants it; for the
single-LAN deployments this targets, the firewall does the
authentication work.

### `--ephemeral --once`

The runner image is expected to run with `RUNNER_EPHEMERAL=true`
and exit after a single job. The Nomad job is `type = "batch"` with
no restart / reschedule — one runner container per workflow_job, no
recycling, no per-host state accumulation. This matches the
"GitHub-hosted-like" execution model.

## Roadmap

- **v0.2** — Multi-tenant routing. Map `repository.owner.login` → tenant config (image, labels, Nomad namespace, token-server tenant).
- **v0.3** — Org-scoped runner registration support (`https://github.com/{owner}` URLs + org-level registration tokens from the token-server).
- **v0.4** — Prometheus metrics endpoint (`/metrics`).
- **v0.5** — Per-tenant resource overrides (let some orgs run larger / smaller runners by label match).

## License

MIT.
