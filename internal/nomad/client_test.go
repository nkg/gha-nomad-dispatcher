package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedRequest captures what the client actually sent, so the tests
// can assert on the wire contract rather than only on the return value.
type recordedRequest struct {
	path        string
	method      string
	contentType string
	nomadToken  string
	body        map[string]any
}

// nomadStub stands in for the Nomad HTTP API. Handlers are keyed by
// path so a test can fail one leg of the two-step submit while leaving
// the other working.
type nomadStub struct {
	mu       sync.Mutex
	requests []recordedRequest

	parseStatus int
	parseBody   string
	jobsStatus  int
	jobsBody    string

	// block, when non-nil, holds the handler until closed or the
	// request context is cancelled.
	block chan struct{}
}

func newStub() *nomadStub {
	return &nomadStub{
		parseStatus: http.StatusOK,
		parseBody:   `{"ID":"gha-runner-1","Name":"gha-runner-1"}`,
		jobsStatus:  http.StatusOK,
		jobsBody:    `{"EvalID":"eval-abc123","Index":42}`,
	}
}

func (s *nomadStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)

		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{
			path:        r.URL.Path,
			method:      r.Method,
			contentType: r.Header.Get("Content-Type"),
			nomadToken:  r.Header.Get("X-Nomad-Token"),
			body:        parsed,
		})
		s.mu.Unlock()

		if s.block != nil {
			select {
			case <-s.block:
			case <-r.Context().Done():
				return
			}
		}

		switch r.URL.Path {
		case "/v1/jobs/parse":
			w.WriteHeader(s.parseStatus)
			_, _ = w.Write([]byte(s.parseBody))
		case "/v1/jobs":
			w.WriteHeader(s.jobsStatus)
			_, _ = w.Write([]byte(s.jobsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *nomadStub) recorded() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

const testHCL = `job "gha-runner-1" { type = "batch" }`

// ── happy path ──────────────────────────────────────────────────────

func TestSubmitJobReturnsEvalID(t *testing.T) {
	stub := newStub()
	srv := stub.server(t)

	evalID, err := New(srv.URL, "").SubmitJob(context.Background(), testHCL)
	if err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}
	if evalID != "eval-abc123" {
		t.Errorf("evalID = %q, want %q", evalID, "eval-abc123")
	}
}

// The two-step submit is the whole reason this client exists: HCL is
// rendered to Nomad's internal JSON by the server, because that shape
// isn't stable across major versions and must never be hand-crafted.
func TestSubmitJobParsesHCLBeforeRegistering(t *testing.T) {
	stub := newStub()
	srv := stub.server(t)

	if _, err := New(srv.URL, "").SubmitJob(context.Background(), testHCL); err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}

	got := stub.recorded()
	if len(got) != 2 {
		t.Fatalf("made %d requests, want 2 (parse then register)", len(got))
	}
	if got[0].path != "/v1/jobs/parse" || got[1].path != "/v1/jobs" {
		t.Fatalf("request order = %q then %q, want /v1/jobs/parse then /v1/jobs",
			got[0].path, got[1].path)
	}

	for i, r := range got {
		if r.method != http.MethodPost {
			t.Errorf("request %d method = %s, want POST", i, r.method)
		}
		if r.contentType != "application/json" {
			t.Errorf("request %d Content-Type = %q, want application/json", i, r.contentType)
		}
	}

	// The parse call must send the HCL verbatim and ask Nomad to
	// canonicalize, so defaults are filled in server-side.
	if got[0].body["JobHCL"] != testHCL {
		t.Errorf("JobHCL = %v, want the HCL we passed", got[0].body["JobHCL"])
	}
	if got[0].body["Canonicalize"] != true {
		t.Errorf("Canonicalize = %v, want true", got[0].body["Canonicalize"])
	}

	// The register call must wrap the parsed JSON under "Job".
	job, ok := got[1].body["Job"].(map[string]any)
	if !ok {
		t.Fatalf("register body has no Job object: %v", got[1].body)
	}
	if job["ID"] != "gha-runner-1" {
		t.Errorf("Job.ID = %v, want the parsed job's ID", job["ID"])
	}
}

// ── ACL token ───────────────────────────────────────────────────────

func TestSubmitJobSendsACLTokenWhenSet(t *testing.T) {
	stub := newStub()
	srv := stub.server(t)

	if _, err := New(srv.URL, "secret-acl-token").SubmitJob(context.Background(), testHCL); err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}

	for _, r := range stub.recorded() {
		if r.nomadToken != "secret-acl-token" {
			t.Errorf("%s X-Nomad-Token = %q, want the configured token", r.path, r.nomadToken)
		}
	}
}

// Nomad rejects an empty X-Nomad-Token differently from an absent one,
// so an ACL-disabled cluster must see no header at all.
func TestSubmitJobOmitsACLHeaderWhenEmpty(t *testing.T) {
	stub := newStub()
	srv := stub.server(t)

	if _, err := New(srv.URL, "").SubmitJob(context.Background(), testHCL); err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}

	for _, r := range stub.recorded() {
		if r.nomadToken != "" {
			t.Errorf("%s sent X-Nomad-Token = %q, want no header", r.path, r.nomadToken)
		}
	}
}

// ── failure modes ───────────────────────────────────────────────────

func TestSubmitJobSurfacesParseFailure(t *testing.T) {
	stub := newStub()
	stub.parseStatus = http.StatusBadRequest
	stub.parseBody = "unexpected token at line 3"
	srv := stub.server(t)

	_, err := New(srv.URL, "").SubmitJob(context.Background(), testHCL)
	if err == nil {
		t.Fatal("SubmitJob() error = nil, want a parse error")
	}
	// The operator needs both the status and Nomad's message to know
	// whether the template or the cluster is at fault.
	for _, want := range []string{"parse HCL", "400", "unexpected token at line 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}

	if n := len(stub.recorded()); n != 1 {
		t.Errorf("made %d requests, want 1 — must not register after a failed parse", n)
	}
}

func TestSubmitJobSurfacesRegisterFailure(t *testing.T) {
	stub := newStub()
	stub.jobsStatus = http.StatusForbidden
	stub.jobsBody = "Permission denied"
	srv := stub.server(t)

	_, err := New(srv.URL, "").SubmitJob(context.Background(), testHCL)
	if err == nil {
		t.Fatal("SubmitJob() error = nil, want a register error")
	}
	for _, want := range []string{"nomad submit", "403", "Permission denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestSubmitJobRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*nomadStub)
		wantErr string
	}{
		{
			name:    "parse returns non-JSON",
			mutate:  func(s *nomadStub) { s.parseBody = "not json at all" },
			wantErr: "decode parse response",
		},
		{
			name:    "register returns non-JSON",
			mutate:  func(s *nomadStub) { s.jobsBody = "<html>502</html>" },
			wantErr: "decode submit response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newStub()
			tt.mutate(stub)
			srv := stub.server(t)

			_, err := New(srv.URL, "").SubmitJob(context.Background(), testHCL)
			if err == nil {
				t.Fatalf("SubmitJob() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// A 200 with no EvalID isn't an error at the HTTP layer, but it means
// the caller has nothing to log or track the dispatch by.
func TestSubmitJobReturnsEmptyEvalIDWhenNomadOmitsIt(t *testing.T) {
	stub := newStub()
	stub.jobsBody = `{"Index":42}`
	srv := stub.server(t)

	evalID, err := New(srv.URL, "").SubmitJob(context.Background(), testHCL)
	if err != nil {
		t.Fatalf("SubmitJob() error = %v", err)
	}
	if evalID != "" {
		t.Errorf("evalID = %q, want empty", evalID)
	}
}

func TestSubmitJobFailsOnUnreachableNomad(t *testing.T) {
	// Port 1 on loopback: connection refused immediately.
	_, err := New("http://127.0.0.1:1", "").SubmitJob(context.Background(), testHCL)
	if err == nil {
		t.Fatal("SubmitJob() error = nil, want a connection error")
	}
	if !strings.Contains(err.Error(), "parse HCL") {
		t.Errorf("error = %q, want it to identify the failing step", err)
	}
}

// ── context ─────────────────────────────────────────────────────────

// The dispatcher runs SubmitJob on a background goroutine under a
// timeout, so cancellation has to reach the request rather than
// waiting out the client's own 30s deadline.
func TestSubmitJobHonoursContextCancellation(t *testing.T) {
	stub := newStub()
	stub.block = make(chan struct{})
	defer close(stub.block)
	srv := stub.server(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := New(srv.URL, "").SubmitJob(ctx, testHCL)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("SubmitJob() error = nil, want a cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v to observe cancellation; the client's 30s timeout won instead", elapsed)
	}
}

// ── constructor ─────────────────────────────────────────────────────

func TestNewSetsATimeout(t *testing.T) {
	c := New("http://nomad.example:4646", "tok")
	if c.baseURL != "http://nomad.example:4646" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.aclToken != "tok" {
		t.Errorf("aclToken = %q", c.aclToken)
	}
	// An unbounded client would let a wedged Nomad hold a dispatch
	// goroutine open indefinitely.
	if c.httpClient.Timeout == 0 {
		t.Error("httpClient has no timeout")
	}
}
