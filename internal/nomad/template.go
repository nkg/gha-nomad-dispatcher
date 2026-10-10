package nomad

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

// runnerHCL is the Nomad job template for a single ephemeral runner
// container. Embedded at build time so the binary is self-contained.
//
//go:embed runner.nomad.hcl
var runnerHCL string

// RunnerJobInputs are the per-job fields we substitute into the
// template. Kept tiny — anything more elaborate belongs in a real
// HCL renderer (or a separate template per org).
type RunnerJobInputs struct {
	// JobID is the unique Nomad job ID, typically derived from the
	// GitHub workflow_job ID so retries on the same job land on the
	// same Nomad job entry.
	JobID string

	// Namespace is the Nomad namespace to submit into.
	Namespace string

	// RunnerURL is the GitHub URL the runner registers against, e.g.
	// "https://github.com/owner/repo". Org-level runners would use
	// "https://github.com/owner".
	RunnerURL string

	// RunnerToken is the single-use registration token from
	// the token-server.
	RunnerToken string

	// RunnerLabels is the comma-separated label list the runner
	// announces to GitHub.
	RunnerLabels string

	// RunnerImage is the OCI image to spawn (e.g.
	// "ghcr.io/myorg/actions-runner:latest").
	RunnerImage string

	// CPU is the Nomad CPU resource in MHz.
	CPU int

	// Memory is the Nomad memory resource in MB.
	Memory int

	// IdleTimeout is how many seconds a spawned runner waits for its
	// FIRST job before exiting. 0 disables it, which is the old
	// behaviour: wait forever.
	//
	// It exists because this dispatcher spawns a runner per queued
	// workflow_job without checking labels, so a runner routinely
	// outlives its reason for existing — the job it was created for is
	// claimed by someone else, or wants labels it does not advertise.
	// An --ephemeral runner exits after one job but waits indefinitely
	// for its first, so those runners hold an allocation until
	// something else happens to match them. On a multi-tenant cluster
	// that is starvation, not untidiness.
	IdleTimeout int

	// RemoveToken lets a spawned runner deregister itself on shutdown.
	// It is a different credential from RunnerToken — that one is
	// single-use and already spent by config.sh at startup, so it
	// cannot be reused for `config.sh remove`.
	//
	// Optional. Empty means the runner exits without deregistering and
	// leaves an offline registration behind, which is the behaviour
	// before this field existed; an older image that does not read
	// RUNNER_REMOVE_TOKEN does the same whatever is passed.
	RemoveToken string

	// Volumes are extra podman bind mounts ("host:container[:opts]")
	// appended after the always-present podman socket mount. The
	// socket stays first and unconditional because every workflow that
	// builds an image depends on it; operators add to it (an NFS
	// package cache, say), they do not replace it.
	Volumes []string

	// Env is injected alongside the fixed RUNNER_* vars, one attribute
	// per entry, keys sorted so the rendered jobspec is stable across
	// dispatches. Keys are assumed valid HCL identifiers — the config
	// loader enforces that — and values are escaped for an HCL quoted
	// string.
	Env map[string]string
}

// Render substitutes the inputs into the embedded template and
// returns the resulting HCL. Templating is done with plain string
// replacement — no template engine — because every field is a known
// fixed identifier that doesn't need escaping logic. The two list
// fields (Volumes, Env) are the exception: they are rendered to HCL
// fragments first and substituted as a block.
//
// If the template ever grows conditional blocks or loops, swap to
// text/template at that point.
func Render(in RunnerJobInputs) (string, error) {
	if in.JobID == "" {
		return "", fmt.Errorf("RunnerJobInputs.JobID is required")
	}
	out := runnerHCL
	replacements := map[string]string{
		"@@JOB_ID@@":        in.JobID,
		"@@NAMESPACE@@":     in.Namespace,
		"@@RUNNER_URL@@":    in.RunnerURL,
		"@@RUNNER_TOKEN@@":  in.RunnerToken,
		"@@REMOVE_TOKEN@@":  in.RemoveToken,
		"@@RUNNER_LABELS@@": in.RunnerLabels,
		"@@RUNNER_IMAGE@@":  in.RunnerImage,
		"@@CPU@@":           fmt.Sprintf("%d", in.CPU),
		"@@MEMORY@@":        fmt.Sprintf("%d", in.Memory),
		"@@IDLE_TIMEOUT@@":  fmt.Sprintf("%d", in.IdleTimeout),
		"@@EXTRA_VOLUMES@@": renderVolumes(in.Volumes),
		"@@EXTRA_ENV@@":     renderEnv(in.Env),
	}
	for k, v := range replacements {
		out = strings.ReplaceAll(out, k, v)
	}
	return out, nil
}

// renderVolumes produces the list-tail that follows the socket mount
// in the template: each extra as a leading ",\n" plus a quoted entry,
// so an empty list renders as nothing and a non-empty one never leaves
// a trailing comma.
func renderVolumes(vols []string) string {
	var b strings.Builder
	for _, v := range vols {
		b.WriteString(",\n          ")
		b.WriteString(hclQuote(v))
	}
	return b.String()
}

// renderEnv produces one `KEY = "value"` attribute per entry, sorted
// by key. The first line lands where the placeholder sat; subsequent
// lines carry the env block's indentation themselves.
func renderEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+" = "+hclQuote(env[k]))
	}
	return strings.Join(lines, "\n        ")
}

// hclQuote wraps s as an HCL quoted string. Backslash and double quote
// are the two characters that would end or corrupt the literal; a raw
// newline is also escaped because an HCL quoted string cannot span
// lines. Nothing else is touched — in particular `${` is passed
// through, so an operator can still reference Nomad runtime variables
// such as ${NOMAD_ALLOC_DIR} on purpose.
func hclQuote(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
	)
	return `"` + r.Replace(s) + `"`
}
