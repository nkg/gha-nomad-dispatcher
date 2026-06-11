// Package github mints GitHub Actions runner registration tokens
// directly, folding in the GitHub App auth that previously lived in
// the standalone gha-token-server LXC.
//
// The flow per owner: sign a short-lived JWT with the owner's App
// private key → exchange it for an installation access token (cached
// ~1h, refreshed under singleflight) → exchange that for a single-use
// runner registration token. The registration endpoint is chosen by
// owner type: organisations get an org-level token; personal user
// accounts have no account-level runner pool, so they get a repo-
// scoped token instead.
package github

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

const githubAPIBase = "https://api.github.com"

// Tenant holds the credentials for one owner's GitHub App installation.
//
// AppID is a string because GitHub Apps created on personal accounts
// after 2024-10-08 (and some org Apps in the redesigned settings UI)
// only expose a string Client ID like "Iv23liqTIFEtdIu6Vn1r" rather
// than a numeric App ID. GitHub's JWT `iss` claim accepts either form,
// so it's passed through verbatim — which is exactly what makes the
// nkg personal account work alongside the org tenants.
type Tenant struct {
	Login          string // lowercased owner login
	AppID          string // numeric App ID or string Client ID — JWT issuer, verbatim
	InstallationID int64
	PrivateKey     *rsa.PrivateKey
}

// Minter mints registration tokens for a fixed set of owners. Safe for
// concurrent use.
type Minter struct {
	tenants map[string]*Tenant // lowercased login → tenant
	http    *http.Client
	cache   *tokenCache
	baseURL string
}

// NewMinter builds a Minter over the given tenants (keyed by lowercased
// login).
func NewMinter(tenants map[string]*Tenant) *Minter {
	return &Minter{
		tenants: tenants,
		http:    &http.Client{Timeout: 30 * time.Second},
		cache:   newTokenCache(),
		baseURL: githubAPIBase,
	}
}

// RegistrationToken mints a single-use runner registration token for
// the owner identified by login. When repoScoped is true the token is
// scoped to owner/repo (required for user-owned accounts); otherwise
// it is an org-level token and repo is ignored.
//
// On an auth failure (401/403) the cached installation token is
// invalidated and the exchange is retried once with a fresh one — the
// token may have been revoked out-of-band (e.g. App key rotation).
func (m *Minter) RegistrationToken(ctx context.Context, login, repo string, repoScoped bool) (string, error) {
	login = strings.ToLower(login)
	tenant, ok := m.tenants[login]
	if !ok {
		return "", fmt.Errorf("no tenant configured for owner %q", login)
	}

	installToken, err := m.installationToken(ctx, tenant)
	if err != nil {
		return "", fmt.Errorf("installation token: %w", err)
	}

	tok, err := m.runnerToken(ctx, installToken, login, repo, repoScoped)
	if err != nil {
		if isAuthError(err) {
			slog.Warn("registration token failed with auth error; refreshing installation token",
				"owner", login, "err", err)
			m.cache.invalidate(login)
			installToken, err = m.installationToken(ctx, tenant)
			if err != nil {
				return "", fmt.Errorf("installation token (retry): %w", err)
			}
			tok, err = m.runnerToken(ctx, installToken, login, repo, repoScoped)
		}
		if err != nil {
			return "", fmt.Errorf("registration token: %w", err)
		}
	}
	return tok, nil
}

// installationToken returns a cached or freshly-minted installation
// access token for the tenant. Cache misses for the same owner coalesce
// through singleflight; different owners refresh in parallel.
func (m *Minter) installationToken(ctx context.Context, tenant *Tenant) (string, error) {
	if tok, ok := m.cache.get(tenant.Login); ok {
		return tok, nil
	}

	v, err, _ := m.cache.flight.Do(tenant.Login, func() (any, error) {
		// Re-check under singleflight: a concurrent caller may have
		// refreshed between our miss and entering Do.
		if tok, ok := m.cache.get(tenant.Login); ok {
			return tok, nil
		}

		jwtToken, err := generateJWT(tenant)
		if err != nil {
			return "", err
		}

		url := fmt.Sprintf("%s/app/installations/%d/access_tokens", m.baseURL, tenant.InstallationID)
		resp, body, err := m.do(ctx, http.MethodPost, url, "Bearer "+jwtToken)
		if err != nil {
			return "", fmt.Errorf("requesting installation token: %w", err)
		}
		if resp.StatusCode != http.StatusCreated {
			return "", fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Token     string    `json:"token"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return "", fmt.Errorf("parsing installation token response: %w", err)
		}

		m.cache.set(tenant.Login, result.Token, result.ExpiresAt)
		slog.Info("obtained installation token",
			"owner", tenant.Login,
			"installation_id", tenant.InstallationID,
			"expires_at", result.ExpiresAt.Format(time.RFC3339))
		return result.Token, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// runnerToken exchanges an installation token for a runner registration
// token. The endpoint is owner-type dependent: repo-scoped for user
// accounts (which have no account-level runner pool), org-scoped
// otherwise.
func (m *Minter) runnerToken(ctx context.Context, installToken, login, repo string, repoScoped bool) (string, error) {
	var url string
	if repoScoped {
		if repo == "" {
			return "", fmt.Errorf("repo-scoped registration requires a repository name")
		}
		url = fmt.Sprintf("%s/repos/%s/%s/actions/runners/registration-token", m.baseURL, login, repo)
	} else {
		url = fmt.Sprintf("%s/orgs/%s/actions/runners/registration-token", m.baseURL, login)
	}

	resp, body, err := m.do(ctx, http.MethodPost, url, "Bearer "+installToken)
	if err != nil {
		return "", fmt.Errorf("requesting registration token: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return "", &authError{statusCode: resp.StatusCode, body: string(body)}
		}
		return "", fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing registration token response: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("GitHub returned an empty registration token")
	}
	return result.Token, nil
}

// do executes a GitHub API request with a single retry on transport
// errors or 5xx. Returns the response (body already drained) and the
// body bytes.
func (m *Minter) do(ctx context.Context, method, url, authorization string) (*http.Response, []byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Authorization", authorization)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "gha-nomad-dispatcher")

		resp, err := m.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("executing request: %w", err)
			slog.Warn("github request failed, will retry", "url", url, "attempt", attempt+1, "err", err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("reading response body: %w", err)
		}

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("github returned %d: %s", resp.StatusCode, string(body))
			slog.Warn("github 5xx, will retry", "url", url, "status", resp.StatusCode, "attempt", attempt+1)
			continue
		}
		return resp, body, nil
	}
	return nil, nil, lastErr
}

// generateJWT creates a short-lived RS256 JWT for the tenant's App,
// signed with that tenant's private key.
func generateJWT(tenant *Tenant) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		// Backdate iat 60s to tolerate minor clock skew between us and
		// GitHub; 10m expiry is GitHub's documented maximum.
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		Issuer:    tenant.AppID,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(tenant.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}
	return signed, nil
}

// ParsePrivateKeyPEM parses an RSA private key in PKCS#1 or PKCS#8 PEM
// form — the two formats GitHub hands out for App keys.
func ParsePrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	pkcs8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing private key (tried PKCS1 and PKCS8): %w", err)
	}
	key, ok := pkcs8.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PKCS8 key is not RSA")
	}
	return key, nil
}

// ── auth error ──────────────────────────────────────────────────────

type authError struct {
	statusCode int
	body       string
}

func (e *authError) Error() string {
	return fmt.Sprintf("GitHub API returned %d: %s", e.statusCode, e.body)
}

func isAuthError(err error) bool {
	var ae *authError
	return errors.As(err, &ae)
}

// ── installation-token cache ────────────────────────────────────────

type tokenCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	flight  singleflight.Group
}

type cacheEntry struct {
	token     string
	expiresAt time.Time
}

func newTokenCache() *tokenCache {
	return &tokenCache{entries: map[string]cacheEntry{}}
}

// get returns a cached token if present and not within 5 minutes of
// expiry.
func (c *tokenCache) get(login string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[login]
	if ok && e.token != "" && time.Now().Before(e.expiresAt.Add(-5*time.Minute)) {
		return e.token, true
	}
	return "", false
}

func (c *tokenCache) set(login, token string, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[login] = cacheEntry{token: token, expiresAt: expiresAt}
}

func (c *tokenCache) invalidate(login string) {
	c.mu.Lock()
	delete(c.entries, login)
	c.mu.Unlock()
	slog.Info("invalidated cached installation token", "owner", login)
}
