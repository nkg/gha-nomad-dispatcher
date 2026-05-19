package nomad

import (
	_ "embed"
	"fmt"
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
}

// Render substitutes the inputs into the embedded template and
// returns the resulting HCL. Templating is done with plain string
// replacement — no template engine — because every field is a known
// fixed identifier that doesn't need escaping logic.
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
		"@@RUNNER_LABELS@@": in.RunnerLabels,
		"@@RUNNER_IMAGE@@":  in.RunnerImage,
		"@@CPU@@":           fmt.Sprintf("%d", in.CPU),
		"@@MEMORY@@":        fmt.Sprintf("%d", in.Memory),
	}
	for k, v := range replacements {
		out = strings.ReplaceAll(out, k, v)
	}
	return out, nil
}
