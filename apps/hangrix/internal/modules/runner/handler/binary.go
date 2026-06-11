// binary.go owns the chi-routed HTTP surface that survived the
// Connect cutover. Most of the runner protocol rides
// hangrix.runner.v1 over Connect now (see runner_connect.go); these
// stay on plain HTTP for two distinct reasons.
//
// Raw-byte routes — Connect would force a base64 round-trip:
//
//	GET /install/runner.sh         (anonymous — curl|sh entrypoint)
//	GET /install/{asset}           (anonymous — install script
//	                                downloads from here)
//	GET /api/runner/binaries/{name} (agent-token — runner self-update)
//
// Self-rescue route — auto-update must keep working after a Connect
// protocol bump that would otherwise brick old runners:
//
//	GET /api/runner/bootstrap      (agent-token — binary catalogue +
//	                                base URL + cadence params)
//
// Bootstrap stays JSON-shaped on purpose. A runner whose Connect
// wire version is too stale to talk to the current server must still
// be able to fetch the catalogue, download the new binary, and swap
// itself out. Tying Bootstrap to Connect would kill that path.
//
// The cross-handler helpers (history-replay text rendering,
// role_config snapshot decoders, triggerActorFromRun) used to live
// here when this file owned the legacy JSON wire shapes; they moved
// to helpers.go alongside the type definitions they need.
package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/binaries"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// AgentHandler keeps its pre-Connect name (rather than something more
// accurate like BinaryHandler) so ioc bindings + the install.go
// methods don't have to be touched. Its surface is now just binary
// downloads + install — the runner protocol moved entirely to
// Connect.
type AgentHandler struct {
	agentValidator domain.AgentValidator
	cfg            *config.Config
}

type AgentHandlerDeps struct {
	AgentValidator domain.AgentValidator
	Config         *config.Config
}

func NewAgentHandler(deps *AgentHandlerDeps) *AgentHandler {
	return &AgentHandler{
		agentValidator: deps.AgentValidator,
		cfg:            deps.Config,
	}
}

func (h *AgentHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/runner", func(r chi.Router) {
		r.Use(h.requireAgentToken)
		// Binary download for the runner's self-update path. Stays
		// HTTP (raw bytes); the URL is advertised by
		// BootstrapPayload.binaries[*].url and the runner downloads
		// via *http.Client (see runner/internal/client/client.go).
		r.Get("/binaries/{name}", h.serveBinary)
		// Bootstrap fetch — see file header for why this stays JSON.
		r.Get("/bootstrap", h.serveBootstrap)
	})

	// Public install path. Both routes are unauthenticated: the
	// install script is the curl|sh entrypoint that does not yet have
	// an agent token, and the binary itself is a public release
	// artefact — possessing it without an enroll token still gets the
	// operator nowhere.
	r.Get("/install/runner.sh", h.serveInstallScript)
	r.Get("/install/{asset}", h.serveInstallBinary)
}

// requireAgentToken validates the bearer hgxr_ token on serveBinary.
// It does NOT inject the resolved runner onto the request context —
// serveBinary doesn't need to know which runner is asking, only that
// the token is valid. Skip the ctx dance entirely so we don't carry
// dead state around.
func (h *AgentHandler) requireAgentToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, err := bearerToken(r)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if _, err := h.agentValidator.ValidateAgentToken(r.Context(), tok); err != nil {
			switch {
			case errors.Is(err, domain.ErrInvalidToken):
				httpx.WriteError(w, http.StatusUnauthorized, "invalid token")
			case errors.Is(err, domain.ErrTokenInactive):
				httpx.WriteError(w, http.StatusForbidden, "token inactive")
			default:
				httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("missing authorization header")
	}
	const pfx = "Bearer "
	if !strings.HasPrefix(h, pfx) {
		return "", errors.New("authorization must be Bearer")
	}
	tok := strings.TrimSpace(h[len(pfx):])
	if tok == "" {
		return "", errors.New("empty bearer token")
	}
	return tok, nil
}

// publicBase decides what URL the in-container agent and the install
// script should talk to. In order of preference:
//
//  1. config.Server.URL explicitly set by the operator (production).
//  2. Reconstructed from the inbound request (devcontainer happy
//     path).
//
// Used by serveInstallScript (install.go) to template the curl|sh
// entrypoint. runner_connect.go has its own publicBaseFromConfig that
// degrades to localhost when no inbound *http.Request is reachable
// from the RPC context.
func (h *AgentHandler) publicBase(r *http.Request) string {
	if b := strings.TrimSpace(h.cfg.Server.URL); b != "" {
		return strings.TrimRight(b, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	return scheme + "://" + host
}

// bootstrapJSON is the wire shape served by GET /api/runner/bootstrap.
// Mirrors apps/hangrix-runner/internal/client.BootstrapPayload — the
// runner client decodes into that struct. Keep the field tags in
// lockstep across the two files; a stale runner with the old struct
// is exactly the case this endpoint exists to serve.
type bootstrapJSON struct {
	Binaries          map[string]bootstrapBinaryJSON `json:"binaries"`
	BaseURL           string                         `json:"base_url"`
	DefaultAgentImage string                         `json:"default_agent_image,omitempty"`
	PollWaitSec       int                            `json:"poll_wait_sec"`
	HeartbeatSec      int                            `json:"heartbeat_sec"`
}

type bootstrapBinaryJSON struct {
	URL    string `json:"url"`
	Name   string `json:"name"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// serveBootstrap returns the same payload Enroll embeds, fetched fresh
// with the long-lived agent token. Plain JSON on purpose so an old
// runner whose Connect wire is stale can still drive its self-update
// loop (Bootstrap → DownloadBinary → swap). See file header.
//
// pollWait / heartbeat numbers come from runner_connect.go's shared
// constants (same package) so the Connect Enroll path and this HTTP
// path can't drift.
func (h *AgentHandler) serveBootstrap(w http.ResponseWriter, r *http.Request) {
	bins := make(map[string]bootstrapBinaryJSON, len(binaries.All()))
	for _, b := range binaries.All() {
		bins[b.AssetName] = bootstrapBinaryJSON{
			URL:    "/api/runner/binaries/" + b.AssetName,
			Name:   b.Name,
			GOOS:   b.GOOS,
			GOARCH: b.GOARCH,
			SHA256: b.SHA256,
			Size:   b.Size,
		}
	}
	payload := bootstrapJSON{
		Binaries:          bins,
		BaseURL:           h.publicBase(r),
		DefaultAgentImage: h.cfg.Runner.DefaultAgentImage,
		PollWaitSec:       int(pollWait / time.Second),
		HeartbeatSec:      20,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// serveBinary streams an embedded runner binary. Path param `name` is
// the AssetName (`hangrix-runner_<goos>_<goarch>`); the same endpoint
// answers for every variant the build embedded. Bearer-authed so a
// public mirror can't be used as a free CDN for the artefact.
func (h *AgentHandler) serveBinary(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	info, err := binaries.GetByAssetName(name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "binary not embedded")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Hangrix-SHA256", info.SHA256)
	http.ServeContent(w, r, info.AssetName, time.Time{}, bytes.NewReader(info.Bytes))
}
