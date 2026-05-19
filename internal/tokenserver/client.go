// Package tokenserver wraps the HTTP API exposed by the token-server
// LXC. The dispatcher only needs to mint runner-registration tokens,
// so this client surface is intentionally tiny.
package tokenserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Client talks to one token-server instance.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New constructs a Client. `baseURL` is the token-server's root, e.g.
// "http://token-server.lab:8080". The token-server is treated as
// trusted: we authenticate it by being on the same VLAN behind the
// fleet's firewall, not via mTLS (today).
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// RegistrationToken response from the token-server. The token is
// single-use and short-lived (~1 hour). The runner agent consumes it
// during `config.sh --token <token>`.
type RegistrationToken struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"` // RFC3339
}

// MintRegistrationToken asks the token-server for a one-shot runner
// registration token scoped to the given repo. The token-server
// handles the GitHub App auth + the actual GitHub API call;
// dispatcher only sees the resulting token.
func (c *Client) MintRegistrationToken(ctx context.Context, org, repo string) (*RegistrationToken, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	u.Path = "/runner-registration-token"
	q := u.Query()
	q.Set("org", org)
	q.Set("repo", repo)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token-server request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token-server: status %d", resp.StatusCode)
	}

	var out RegistrationToken
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &out, nil
}
