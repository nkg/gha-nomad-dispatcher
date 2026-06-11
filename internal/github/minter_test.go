package github

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"crypto/rand"
)

// fakeGitHub stands in for api.github.com. It mints a static installation
// token then a static registration token, recording how many times each
// endpoint was hit and the exact registration path used.
type fakeGitHub struct {
	installCalls atomic.Int64
	regCalls     atomic.Int64
	lastRegPath  atomic.Value // string

	// failRegOnce, when true, makes the first registration call return
	// 401 to exercise the auth-retry path.
	failRegOnce atomic.Bool
}

func (f *fakeGitHub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		f.installCalls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_install_token",
			"expires_at": "2999-01-01T00:00:00Z",
		})
	})
	regHandler := func(w http.ResponseWriter, r *http.Request) {
		f.regCalls.Add(1)
		f.lastRegPath.Store(r.URL.Path)
		if f.failRegOnce.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "ghr_registration_token"})
	}
	mux.HandleFunc("/orgs/{org}/actions/runners/registration-token", regHandler)
	mux.HandleFunc("/repos/{owner}/{repo}/actions/runners/registration-token", regHandler)
	return mux
}

func newTestMinter(t *testing.T, srvURL string, logins ...string) *Minter {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tenants := map[string]*Tenant{}
	for i, login := range logins {
		tenants[login] = &Tenant{
			Login:          login,
			AppID:          fmt.Sprintf("app-%d", i),
			InstallationID: int64(100 + i),
			PrivateKey:     key,
		}
	}
	m := NewMinter(tenants)
	m.baseURL = srvURL
	return m
}

func TestRegistrationToken_OrgScoped(t *testing.T) {
	fake := &fakeGitHub{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := newTestMinter(t, srv.URL, "sproncy")
	tok, err := m.RegistrationToken(context.Background(), "sproncy", "anything", false)
	if err != nil {
		t.Fatalf("RegistrationToken: %v", err)
	}
	if tok != "ghr_registration_token" {
		t.Errorf("token = %q", tok)
	}
	if got := fake.lastRegPath.Load().(string); got != "/orgs/sproncy/actions/runners/registration-token" {
		t.Errorf("registration path = %q, want org-scoped", got)
	}
}

func TestRegistrationToken_RepoScopedForUser(t *testing.T) {
	fake := &fakeGitHub{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := newTestMinter(t, srv.URL, "nkg")
	tok, err := m.RegistrationToken(context.Background(), "nkg", "private-thing", true)
	if err != nil {
		t.Fatalf("RegistrationToken: %v", err)
	}
	if tok != "ghr_registration_token" {
		t.Errorf("token = %q", tok)
	}
	if got := fake.lastRegPath.Load().(string); got != "/repos/nkg/private-thing/actions/runners/registration-token" {
		t.Errorf("registration path = %q, want repo-scoped", got)
	}
}

func TestRegistrationToken_RepoScopedRequiresRepo(t *testing.T) {
	fake := &fakeGitHub{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := newTestMinter(t, srv.URL, "nkg")
	_, err := m.RegistrationToken(context.Background(), "nkg", "", true)
	if err == nil || !strings.Contains(err.Error(), "repository name") {
		t.Fatalf("want repo-name error, got %v", err)
	}
}

func TestRegistrationToken_UnknownOwner(t *testing.T) {
	fake := &fakeGitHub{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := newTestMinter(t, srv.URL, "sproncy")
	_, err := m.RegistrationToken(context.Background(), "hordialabs", "", false)
	if err == nil || !strings.Contains(err.Error(), "no tenant") {
		t.Fatalf("want no-tenant error, got %v", err)
	}
}

func TestInstallationTokenCached(t *testing.T) {
	fake := &fakeGitHub{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := newTestMinter(t, srv.URL, "sproncy")
	for i := 0; i < 3; i++ {
		if _, err := m.RegistrationToken(context.Background(), "sproncy", "", false); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := fake.installCalls.Load(); n != 1 {
		t.Errorf("installation token minted %d times, want 1 (cached)", n)
	}
	if n := fake.regCalls.Load(); n != 3 {
		t.Errorf("registration calls = %d, want 3", n)
	}
}

func TestRegistrationToken_AuthRetry(t *testing.T) {
	fake := &fakeGitHub{}
	fake.failRegOnce.Store(true)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := newTestMinter(t, srv.URL, "sproncy")
	tok, err := m.RegistrationToken(context.Background(), "sproncy", "", false)
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if tok != "ghr_registration_token" {
		t.Errorf("token = %q", tok)
	}
	// First 401, then a re-mint of the installation token, then 201.
	if n := fake.regCalls.Load(); n != 2 {
		t.Errorf("registration calls = %d, want 2 (one failed + one retry)", n)
	}
	if n := fake.installCalls.Load(); n != 2 {
		t.Errorf("installation calls = %d, want 2 (initial + post-invalidate)", n)
	}
}
