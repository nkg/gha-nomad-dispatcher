package tokenserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMintRegistrationToken(t *testing.T) {
	const wantOrg = "acme"
	const wantToken = "ATOKEN_12345abcde"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/token" {
			t.Errorf("path = %s, want /token", r.URL.Path)
		}
		if got := r.URL.Query().Get("org"); got != wantOrg {
			t.Errorf("org param = %q, want %q", got, wantOrg)
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, wantToken)
	}))
	defer srv.Close()

	c := New(srv.URL)
	got, err := c.MintRegistrationToken(context.Background(), wantOrg)
	if err != nil {
		t.Fatalf("MintRegistrationToken: %v", err)
	}
	if got.Token != wantToken {
		t.Errorf("Token = %q, want %q", got.Token, wantToken)
	}
}

func TestMintRegistrationToken_TrimsWhitespace(t *testing.T) {
	// gha-token-server returns text/plain — make sure trailing newlines
	// or whitespace don't end up in the token we hand to the runner agent.
	const inner = "TOKEN_xyz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "  %s\n\n", inner)
	}))
	defer srv.Close()

	c := New(srv.URL)
	got, err := c.MintRegistrationToken(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != inner {
		t.Errorf("Token = %q, want %q (no leading/trailing whitespace)", got.Token, inner)
	}
}

func TestMintRegistrationToken_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "kaboom")
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.MintRegistrationToken(context.Background(), "acme")
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("error should mention status + body: %v", err)
	}
}

func TestMintRegistrationToken_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 200 OK but no body — token-server bug; we should refuse to
		// hand an empty token to the runner agent.
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.MintRegistrationToken(context.Background(), "acme")
	if err == nil {
		t.Fatal("expected error on empty body")
	}
}

func TestMintRegistrationToken_NotFound(t *testing.T) {
	// Token-server returns 404 if the org doesn't have a configured
	// tenant. Dispatcher should surface that rather than retry blindly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "no tenant configured for org \"unknown-org\"")
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.MintRegistrationToken(context.Background(), "unknown-org")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404: %v", err)
	}
}
