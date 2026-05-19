package nomad

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to a Nomad API endpoint. Implemented against the
// raw HTTP API (no hashicorp/nomad/api dep) to keep the binary small
// and avoid pulling in Nomad's large vendored graph.
type Client struct {
	baseURL    string
	aclToken   string
	httpClient *http.Client
}

// New constructs a Client. `baseURL` is the Nomad API root (e.g.
// "http://nomad-server.lab:4646"). `aclToken` may be empty if ACLs
// are disabled.
func New(baseURL, aclToken string) *Client {
	return &Client{
		baseURL:  baseURL,
		aclToken: aclToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SubmitJob submits the given HCL job spec to Nomad. Returns the
// Nomad-assigned evaluation ID on success.
//
// The two-step submit (parse HCL → submit JSON) matches the Nomad
// HTTP API: /v1/jobs/parse renders HCL to JSON, /v1/jobs registers
// it. The JSON shape is Nomad-internal and not stable across major
// versions, so we never hand-craft it.
func (c *Client) SubmitJob(ctx context.Context, hcl string) (string, error) {
	jobJSON, err := c.parseHCL(ctx, hcl)
	if err != nil {
		return "", fmt.Errorf("parse HCL: %w", err)
	}

	reqBody := map[string]any{"Job": jobJSON}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal submit: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/jobs", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.aclToken != "" {
		req.Header.Set("X-Nomad-Token", c.aclToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("nomad request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("nomad submit: status %d: %s", resp.StatusCode, errBody)
	}

	var out struct {
		EvalID string `json:"EvalID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode submit response: %w", err)
	}
	return out.EvalID, nil
}

// parseHCL renders an HCL job spec into Nomad's JSON internal form.
func (c *Client) parseHCL(ctx context.Context, hcl string) (map[string]any, error) {
	reqBody, err := json.Marshal(map[string]any{
		"JobHCL":       hcl,
		"Canonicalize": true,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/jobs/parse", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.aclToken != "" {
		req.Header.Set("X-Nomad-Token", c.aclToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, errBody)
	}

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode parse response: %w", err)
	}
	return out, nil
}
