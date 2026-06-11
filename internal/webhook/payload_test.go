package webhook

import "testing"

const queuedPayload = `{
  "action": "queued",
  "workflow_job": {
    "id": 12345,
    "run_id": 67890,
    "name": "build",
    "labels": ["self-hosted", "linux"],
    "status": "queued"
  },
  "repository": {
    "full_name": "owner/repo",
    "name": "repo",
    "owner": { "login": "owner", "type": "Organization" }
  },
  "installation": { "id": 999 }
}`

const userOwnedPayload = `{
  "action": "queued",
  "workflow_job": { "id": 222, "status": "queued" },
  "repository": {
    "full_name": "nkg/private-thing",
    "name": "private-thing",
    "owner": { "login": "nkg", "type": "User" }
  },
  "installation": { "id": 1000 }
}`

const completedPayload = `{
  "action": "completed",
  "workflow_job": { "id": 12345 },
  "repository": { "full_name": "owner/repo", "name": "repo", "owner": {"login": "owner"} }
}`

func TestParseWorkflowJob(t *testing.T) {
	ev, err := ParseWorkflowJob([]byte(queuedPayload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Action != "queued" {
		t.Errorf("Action = %q, want queued", ev.Action)
	}
	if ev.WorkflowJob.ID != 12345 {
		t.Errorf("WorkflowJob.ID = %d, want 12345", ev.WorkflowJob.ID)
	}
	if ev.Repository.FullName != "owner/repo" {
		t.Errorf("Repository.FullName = %q", ev.Repository.FullName)
	}
	if ev.Repository.Owner.Login != "owner" {
		t.Errorf("Repository.Owner.Login = %q", ev.Repository.Owner.Login)
	}
	if ev.IsUserOwner() {
		t.Errorf("org-owned payload IsUserOwner() = true")
	}
}

func TestIsUserOwner(t *testing.T) {
	user, err := ParseWorkflowJob([]byte(userOwnedPayload))
	if err != nil {
		t.Fatal(err)
	}
	if !user.IsUserOwner() {
		t.Errorf("user-owned payload IsUserOwner() = false")
	}
	if got := user.OwnerLogin(); got != "nkg" {
		t.Errorf("OwnerLogin() = %q, want nkg", got)
	}
	if got := user.RepoName(); got != "private-thing" {
		t.Errorf("RepoName() = %q, want private-thing", got)
	}
}

func TestParseWorkflowJob_Malformed(t *testing.T) {
	if _, err := ParseWorkflowJob([]byte("not json")); err == nil {
		t.Errorf("expected error on malformed JSON")
	}
}

func TestIsQueued(t *testing.T) {
	queued, err := ParseWorkflowJob([]byte(queuedPayload))
	if err != nil {
		t.Fatal(err)
	}
	if !queued.IsQueued() {
		t.Errorf("queued payload IsQueued() = false")
	}

	completed, err := ParseWorkflowJob([]byte(completedPayload))
	if err != nil {
		t.Fatal(err)
	}
	if completed.IsQueued() {
		t.Errorf("completed payload IsQueued() = true")
	}
}
