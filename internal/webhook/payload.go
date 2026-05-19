package webhook

import (
	"encoding/json"
	"fmt"
)

// WorkflowJob is the subset of GitHub's `workflow_job` webhook payload
// that we care about. The upstream schema is much larger; we only
// pull the fields the dispatcher actually uses, so a schema bump
// upstream doesn't break parsing of unrelated additions.
//
// Reference: https://docs.github.com/en/webhooks/webhook-events-and-payloads#workflow_job
type WorkflowJob struct {
	Action string `json:"action"` // "queued" | "in_progress" | "completed" | "waiting"

	WorkflowJob struct {
		ID     int64    `json:"id"`
		RunID  int64    `json:"run_id"`
		Name   string   `json:"name"`
		Labels []string `json:"labels"`
		Status string   `json:"status"`
	} `json:"workflow_job"`

	Repository struct {
		FullName string `json:"full_name"` // "owner/repo"
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	} `json:"repository"`

	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// ParseWorkflowJob unmarshals the webhook body. It does NOT validate
// the signature — callers must do that before parsing.
func ParseWorkflowJob(body []byte) (*WorkflowJob, error) {
	var ev WorkflowJob
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("parse workflow_job: %w", err)
	}
	return &ev, nil
}

// IsQueued returns true if this event represents a job becoming
// available to schedule. Other actions (in_progress, completed,
// waiting) are ignored by the dispatcher — Nomad handles the
// runner-job lifecycle from there.
func (e *WorkflowJob) IsQueued() bool {
	return e.Action == "queued"
}
