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

## v0.2 scope (current)

- **Multi-tenant.** One dispatcher serves many owners (orgs and
  personal user accounts). Each owner is its own GitHub App with its
  own webhook secret, labels, and Nomad namespace.
- **Self-contained token minting.** The dispatcher signs the App JWT,
  exchanges it for an installation token (cached ~1h, refreshed under
  singleflight), and mints the runner registration token itself — no
  separate token-server. The minting logic was folded in from
  [`nkg/gha-token-server`](https://github.com/nkg/gha-token-server).
- **Org- *and* user-account aware.** Organisations register an
  org-level runner (`https://github.com/{owner}`); personal user
  accounts have no account-level runner pool, so they register a
  repo-scoped runner (`https://github.com/{owner}/{repo}`). The branch
  is driven by `repository.owner.type` in the payload.
- **`workflow_job: queued` only.** Other actions / event types are
  acknowledged and ignored — Nomad handles the runner-job lifecycle.
- **No Nomad ACLs required** (but supported via `nomad_token`).

## Architecture

```
       GitHub (App per owner)
         │  workflow_job.queued  →  POST /webhook/{owner}
         ▼
   gha-nomad-dispatcher
     1. select owner by path → validate HMAC with that owner's secret
     2. parse; cross-check payload owner == path owner
     3. mint registration token (org- or repo-scoped) via GitHub App API
     4. submit Nomad job into the owner's namespace with the owner's labels
         │
         ▼
    Nomad API  ─►  Nomad client LXCs
                       │
                       ▼  podman driver
              ephemeral runner container
              (--ephemeral --once, registers with GitHub, runs the job, exits)
```

Each owner's GitHub App points its webhook at `/webhook/{owner}` so
the secret is selected by route *before* the body is trusted — every
App signs with its own secret.

## Configuration

Configuration is a JSON file named by `CONFIG_PATH`
(default `/etc/gha-dispatcher/config.json`). The deploying composition
renders it from SOPS-decrypted material (webhook secrets live in the
file; private keys are referenced by path and mounted alongside).

```jsonc
{
  "listen_addr": ":8080",                 // optional, default ":8080"
  "nomad_addr": "http://nomad.lab:4646",  // required
  "nomad_token": "",                       // optional, Nomad ACL token
  "defaults": {
    "runner_image": "ghcr.io/nkg/oci-actions-runner:v0.1.0", // required
    "runner_cpu_mhz": 2000,                // optional, default 2000
    "runner_memory_mb": 2048               // optional, default 2048
  },
  "owners": [
    {
      "login": "sproncy",                  // org tenant
      "type": "organization",
      "app_id": "2879772",                 // numeric App ID or string Client ID
      "installation_id": 110552520,
      "private_key_path": "/etc/gha-dispatcher/keys/sproncy.pem",
      "webhook_secret": "…",
      "runner_labels": "self-hosted,linux,x64,podman,sproncy",
      "nomad_namespace": "sproncy"         // optional, default "default"
      // "runner_image": "…"               // optional per-owner override
    },
    {
      "login": "nkg",                      // personal user account
      "type": "user",                      // → repo-scoped registration
      "app_id": "Iv23li…",                 // personal Apps expose a string Client ID
      "installation_id": 99887766,
      "private_key_path": "/etc/gha-dispatcher/keys/nkg.pem",
      "webhook_secret": "…",
      "runner_labels": "self-hosted,linux,x64,podman,nkg"
    }
  ]
}
```

Per-owner `type` must be `"organization"` or `"user"`. The user's App
needs repo **Administration: write** to mint repo-scoped registration
tokens; orgs need org self-hosted-runner write.

## Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/webhook/{owner}` | GitHub webhook ingestion for one owner. `202` on dispatch, `204` on ignored event/action, `401` on signature failure, `400` on malformed payload or owner mismatch, `404` on unknown owner, `500` on downstream failure. |
| `GET` | `/healthz` | Liveness probe. |

## Build + run

### Local (Go)

```bash
mise install            # Go 1.26 + golangci-lint + lefthook
go build -o gha-nomad-dispatcher .
CONFIG_PATH=./config.json ./gha-nomad-dispatcher
```

### Container

```bash
docker run --rm \
  -p 8080:8080 \
  -v /etc/gha-dispatcher:/etc/gha-dispatcher:ro \
  ghcr.io/nkg/gha-nomad-dispatcher:v0.2.0
```

Mount the config file and the referenced private keys under
`/etc/gha-dispatcher/`. After a tag is pushed, the release workflow
publishes multi-arch images (`linux/amd64` + `linux/arm64`).

## Tests

```bash
go test ./...               # all packages
go test -race ./...         # race detector (same as CI)
go test -cover ./...        # coverage summary
```

CI runs `go vet`, `go test -race`, `go build`, and `golangci-lint`
on every PR + push to main.

## Design notes

### Token minting folded in (no token-server)

Earlier the dispatcher called a separate `gha-token-server` LXC over
HTTP to mint registration tokens. v0.2 folds that logic into
`internal/github`: one fewer LXC, one fewer secrets-distribution hop,
and the App private keys live in exactly one place. `gha-token-server`
remains a standalone service for non-Nomad consumers; it is no longer
on this dispatcher's path.

### Owner type decides registration scope

GitHub only exposes account-level self-hosted runner pools for
organisations (and enterprises). Personal user accounts don't have
one, so a user-owned repo's runner must register at repo scope. The
dispatcher reads `repository.owner.type` and picks the GitHub API
endpoint accordingly: `POST /orgs/{owner}/…/registration-token` vs
`POST /repos/{owner}/{repo}/…/registration-token`.

### No `hashicorp/nomad/api` dependency

The dispatcher talks to Nomad via its raw HTTP API (`/v1/jobs/parse`
→ `/v1/jobs`) rather than vendoring `github.com/hashicorp/nomad/api`.
The client surface we need is tiny and the upstream package pulls in a
heavy vendored graph; the raw HTTP approach keeps the binary small.

### Job spec is HCL, embedded at build time

`internal/nomad/runner.nomad.hcl` is the canonical job template,
embedded with `//go:embed`. Substitution is plain string replacement
(`@@FIELD@@` placeholders). If the template grows conditionals or
loops, swap to `text/template`.

### `--ephemeral --once`

The runner image runs with `RUNNER_EPHEMERAL=true` and exits after a
single job. The Nomad job is `type = "batch"` with no restart /
reschedule — one runner container per workflow_job, no recycling, no
per-host state accumulation.

## Roadmap

- **v0.3** — Prometheus metrics endpoint (`/metrics`), mirroring the
  per-owner counters the token-server exposed.
- **v0.4** — Per-owner resource overrides (CPU/memory by label match).
- **Cross-tailnet runners** — composition concern: user/org tenants
  whose private infra lives on a separate Tailscale tailnet.

## License

MIT.
