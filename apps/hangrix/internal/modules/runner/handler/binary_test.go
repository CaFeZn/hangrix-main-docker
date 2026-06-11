package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// newAgentHandlerTestServer spins up an httptest.Server hosting only
// AgentHandler's routes. baseURL controls cfg.Server.URL so tests can
// flip between the explicit-config path and the request-host fallback.
func newAgentHandlerTestServer(t *testing.T, auth domain.AgentValidator, baseURL string) *httptest.Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.Server.URL = baseURL
	cfg.Runner.DefaultAgentImage = "ghcr.io/test/agent:latest"
	h := NewAgentHandler(&AgentHandlerDeps{AgentValidator: auth, Config: cfg})
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return httptest.NewServer(r)
}

func getJSON(t *testing.T, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, body
}

func TestBootstrap_MissingBearer_Returns401(t *testing.T) {
	srv := newAgentHandlerTestServer(t, &stubAgentValidator{}, "https://platform.test")
	defer srv.Close()

	resp, _ := getJSON(t, srv.URL+"/api/runner/bootstrap", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestBootstrap_BadToken_Returns401(t *testing.T) {
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	srv := newAgentHandlerTestServer(t, auth, "https://platform.test")
	defer srv.Close()

	resp, _ := getJSON(t, srv.URL+"/api/runner/bootstrap", "hgxr_wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestBootstrap_InactiveToken_Returns403(t *testing.T) {
	auth := &stubAgentValidator{err: domain.ErrTokenInactive}
	srv := newAgentHandlerTestServer(t, auth, "https://platform.test")
	defer srv.Close()

	resp, _ := getJSON(t, srv.URL+"/api/runner/bootstrap", "hgxr_dead")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestBootstrap_OK_Returns200_WithJSONShape(t *testing.T) {
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	srv := newAgentHandlerTestServer(t, auth, "https://platform.test")
	defer srv.Close()

	resp, body := getJSON(t, srv.URL+"/api/runner/bootstrap", "hgxr_correct")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var bp bootstrapJSON
	if err := json.Unmarshal(body, &bp); err != nil {
		t.Fatalf("decode bootstrap: %v\nbody: %s", err, body)
	}
	if bp.BaseURL != "https://platform.test" {
		t.Errorf("BaseURL = %q, want https://platform.test", bp.BaseURL)
	}
	if bp.DefaultAgentImage != "ghcr.io/test/agent:latest" {
		t.Errorf("DefaultAgentImage = %q", bp.DefaultAgentImage)
	}
	if bp.PollWaitSec <= 0 {
		t.Errorf("PollWaitSec = %d, want > 0", bp.PollWaitSec)
	}
	if bp.HeartbeatSec <= 0 {
		t.Errorf("HeartbeatSec = %d, want > 0", bp.HeartbeatSec)
	}
}

// TestBootstrap_FallsBackToRequestHost pins the devcontainer / `go run`
// happy path: when cfg.Server.URL is empty, BaseURL must be derived
// from the inbound request rather than hard-coding localhost. Without
// this, every agent container the runner spawns would see
// HANGRIX_PLATFORM_BASE_URL=http://localhost:8080 and fail to reach
// the platform from inside the container network.
func TestBootstrap_FallsBackToRequestHost(t *testing.T) {
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	srv := newAgentHandlerTestServer(t, auth, "")
	defer srv.Close()

	resp, body := getJSON(t, srv.URL+"/api/runner/bootstrap", "hgxr_correct")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var bp bootstrapJSON
	if err := json.Unmarshal(body, &bp); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	// srv.URL is what we dialed, so the handler should echo back
	// scheme://Host derived from r.Host — which equals srv.URL minus
	// the scheme/host distinction. httptest uses http:// so we compare
	// against the full URL.
	if !strings.EqualFold(bp.BaseURL, srv.URL) {
		t.Errorf("BaseURL = %q, want %q (derived from request Host)", bp.BaseURL, srv.URL)
	}
}
