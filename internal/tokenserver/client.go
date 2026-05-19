// Package tokenserver wraps the HTTP API exposed by the
// gha-token-server LXC. The dispatcher only needs to mint runner-
// registration tokens, so this client surface is intentionally tiny.
package tokenserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one gha-token-server instance.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New constructs a Client. `baseURL` is the token-server's root,
// e.g. "http://gha-token-server.lab:8080". The server is treated as
// trusted — we rely on shared-VLAN + firewall isolation rather than
// mTLS today.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// RegistrationToken is the response from the token-server. The
// token is single-use and short-lived (~1 hour); the runner agent
// consumes it during `config.sh --token <token>`.
//
// Wrapped in a struct (rather than returning a bare string) so the
// future JSON endpoint on the token-server's roadmap — which adds
// `ExpiresAt` — slots in here without breaking callers.
type RegistrationToken struct {
	Token string
}

// MintRegistrationToken asks the token-server for a one-shot
// registration token for the given org. The token-server handles
// the GitHub App auth + the actual GitHub API call; the dispatcher
// only sees the resulting token.
//
// Wire format follows gha-token-server's v0.1 API:
//
//	GET <baseURL>/token?org=<org>
//	200 OK, Content-Type: text/plain
//	<bare token, no newline guarantees>
//
// Repo-scope (when needed) is configured server-side via the
// token-server's GITHUB_REPO env var, not per-request — so this
// client only passes the org.
func (c *Client) MintRegistrationToken(ctx context.Context, org string) (*RegistrationToken, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	u.Path = "/token"
	q := u.Query()
	q.Set("org", org)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token-server request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token-server: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	token := strings.TrimSpace(string(body))
	if token == "" {
		return nil, fmt.Errorf("token-server returned empty body")
	}
	return &RegistrationToken{Token: token}, nil
}
