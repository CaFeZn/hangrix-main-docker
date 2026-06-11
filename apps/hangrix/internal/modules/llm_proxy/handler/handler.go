// Package handler implements the OpenAI-Response-API-compatible HTTP
// proxy mounted at /api/llm/v1/responses.
//
// Architecture:
//
//   - This file owns the HTTP + wire-format boundary: bearer-token auth,
//     body bounding, model→provider routing, key decryption, parsing the
//     inbound Responses-API JSON into a typed upstream.Request,
//     dispatching through upstream.Provider.Respond, marshalling the
//     typed Response back as Responses-API JSON, and writing the usage
//     log row.
//
//   - Per-vendor logic (URL shaping, request/response translation,
//     reasoning effort mapping, usage extraction) lives behind
//     upstream.Provider in the sibling upstream package. The handler
//     dispatches via the Registry; adding a new vendor is one new
//     Provider implementation, no edits here.
//
// Auth model:
//
//   - No session cookies, no CSRF, no RequireAuth: every request
//     carries an `Authorization: Bearer hgxs_<prefix>_<secret>` session
//     token. The bearerAuth middleware resolves it via
//     runner/domain.SessionTokenValidator and stores the agent_session
//     on the request context. Failures map to 401 (missing/malformed
//     header) or 403 (token invalid / inactive).
//
//   - The session token is the in-container agent's identity. It is NOT
//     bound to a specific provider — the proxy resolves the upstream from
//     the request body's `model` field via the model/group definitions
//     (Lookup.ResolveModel).
//
// Scope:
//
//   - Only POST /v1/responses is supported. Other paths (/v1/embeddings,
//     /v1/audio/*, /v1/files/*) need their own typed adapters; they
//     return 404 until then.
//
//   - Streaming responses are not supported (`stream:true` → 501). A
//     typed Response can't represent a partial token stream; SSE will
//     be re-introduced when there's a real consumer.
//
// Every request — success or failure — writes one row to llm_usage_log
// via llm_provider/domain.Lookup.RecordUsage. Logging failures never
// break a working upstream call (best-effort, swallowed).
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	llmdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy/upstream"
	silencedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// maxRequestBody bounds the buffered request body so a hostile caller
// cannot OOM the server by streaming a multi-gigabyte JSON object.
// 4 MiB comfortably fits a long conversation plus a few function-tool
// schemas; anything larger is rejected with 413.
const maxRequestBody = 4 << 20

// upstreamTimeout is the per-request timeout for upstream calls. With
// streaming removed, one timeout covers everything.
const upstreamTimeout = 5 * time.Minute

type ctxKey int

const (
	ctxKeySession ctxKey = iota
)

// Handler implements server.RouteProvider for the proxy.
type Handler struct {
	lookup      llmdomain.Lookup
	validator   runnerdomain.SessionTokenValidator
	silenceGate silencedomain.SilenceGate
	registry    *upstream.Registry
	box         *cryptobox.Box
	client      *http.Client
}

type HandlerDeps struct {
	Lookup      llmdomain.Lookup
	Validator   runnerdomain.SessionTokenValidator
	SilenceGate silencedomain.SilenceGate
	Registry    *upstream.Registry
	Config      *config.Config
}

func NewHandler(deps *HandlerDeps) *Handler {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(fmt.Errorf("llm_proxy cryptobox: %w", err))
	}
	return &Handler{
		lookup:      deps.Lookup,
		validator:   deps.Validator,
		silenceGate: deps.SilenceGate,
		registry:    deps.Registry,
		box:         box,
		client:      &http.Client{Timeout: upstreamTimeout},
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/llm/v1", func(r chi.Router) {
		r.Use(h.bearerAuth)
		r.Post("/responses", h.respond)
	})
}

// bearerAuth resolves the Authorization header into the calling
// agent_session and stores it on the request context. 401 on
// missing/malformed header, 403 on token invalid/inactive.
func (h *Handler) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(raw, prefix) {
			httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
		if token == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		sess, err := h.validator.ValidateSessionToken(r.Context(), token)
		if err != nil {
			switch {
			case errors.Is(err, runnerdomain.ErrInvalidSessionToken):
				httpx.WriteError(w, http.StatusForbidden, "invalid session token")
			case errors.Is(err, runnerdomain.ErrSessionTokenInactive):
				httpx.WriteError(w, http.StatusForbidden, "session token revoked or session terminated")
			default:
				httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		// Silence gate: even inference calls must be blocked when the
		// repo is silenced — this is a defence-in-depth measure so that
		// a silenced agent cannot continue working even if the control
		// frame is delayed. Returns 423 Locked with a Retry-After header
		// based on the expected exit time.
		if h.silenceGate != nil && sess.RepoID != nil {
			if serr := h.silenceGate.CheckSession(r.Context(), sess.ID, *sess.RepoID); serr != nil {
				var silenced *silencedomain.ErrRepoSilenced
				if errors.As(serr, &silenced) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusLocked)
					msg := map[string]any{"error": silenced.Error(), "code": "repo_silenced"}
					if silenced.ExpectedExitAt != nil {
						retryAfter := time.Until(*silenced.ExpectedExitAt).Seconds()
						if retryAfter > 0 {
							w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter))
						}
						msg["expected_exit_at"] = silenced.ExpectedExitAt.Format(time.RFC3339)
					}
					_ = json.NewEncoder(w).Encode(msg)
					return
				}
				httpx.WriteError(w, http.StatusInternalServerError, "internal error checking silence state")
				return
			}
		}

		// No issue-state gate here: an LLM inference call performs no
		// issue/repo mutation, so a closed or merged issue must not block
		// it. An agent mid-completion when its issue merges would otherwise
		// hard-fail with a 403. Terminal-state enforcement belongs on the
		// write surfaces (platform API tools, git push), not on inference.
		ctx := context.WithValue(r.Context(), ctxKeySession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// respond is the entry point for every authenticated request. Linear
// flow: validate, parse, resolve provider by model, dispatch, marshal,
// log usage.
func (h *Handler) respond(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sess, ok := r.Context().Value(ctxKeySession).(*runnerdomain.AgentSession)
	if !ok || sess == nil {
		httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}

	// (1) Buffer and parse the body into a typed Request.
	body, err := readBoundedBody(r.Body)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	upReq, stream, err := upstream.ParseResponsesAPIRequest(body)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if stream {
		httpx.WriteError(w, http.StatusNotImplemented, "streaming not supported by this proxy")
		h.recordUsage(r.Context(), sess, 0, upReq.Model, upstream.Usage{}, http.StatusNotImplemented, "stream not supported", r.URL.Path, time.Since(start), string(body), "")
		return
	}
	if upReq.Model == "" {
		httpx.WriteError(w, http.StatusBadRequest, "model is required")
		return
	}

	// (2) Resolve model → ordered candidate list from its model/group definition.
	resolution, err := h.lookup.ResolveModel(r.Context(), upReq.Model)
	if err != nil {
		switch {
		case errors.Is(err, llmdomain.ErrNoModelMatch):
			msg := fmt.Sprintf("no provider serves model %q", upReq.Model)
			httpx.WriteError(w, http.StatusNotFound, msg)
		case errors.Is(err, llmdomain.ErrGroupAllUnavailable):
			httpx.WriteError(w, http.StatusServiceUnavailable, err.Error())
		default:
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// (3) Iterate candidates, fail over on transient errors.
	var lastErr error
	for i, cand := range resolution.Candidates {
		adapter, ok := h.registry.Lookup(cand.Provider.Type)
		if !ok {
			msg := fmt.Sprintf("unsupported provider type: %s", cand.Provider.Type)
			h.recordUsage(r.Context(), sess, cand.Provider.ID, cand.Model, upstream.Usage{}, http.StatusNotImplemented, msg, r.URL.Path, time.Since(start), string(body), "")
			lastErr = fmt.Errorf("%s", msg)
			continue
		}

		apiKey, err := h.box.Decrypt(cand.Provider.ApiKey)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to decrypt provider key")
			return
		}

		// Fill connection params for this candidate.
		upReq.Model = cand.Model
		upReq.APIKey = apiKey
		upReq.BaseURL = cand.Provider.BaseURL
		upReq.Client = h.client

		upResp, dispatchErr := adapter.Respond(r.Context(), upReq)

		if dispatchErr == nil {
			// Success: report to state machine and write response.
			_ = h.lookup.ReportAttempt(r.Context(), cand.MemberID, llmdomain.AttemptOutcome{Success: true})

			outBody, err := upstream.MarshalResponsesAPIResponse(upResp)
			if err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "failed to encode response")
				h.recordUsage(r.Context(), sess, cand.Provider.ID, cand.Model, upResp.Usage, http.StatusInternalServerError, err.Error(), r.URL.Path, time.Since(start), string(body), "")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(outBody); err != nil {
				h.recordUsage(r.Context(), sess, cand.Provider.ID, cand.Model, upResp.Usage, http.StatusOK, err.Error(), r.URL.Path, time.Since(start), string(body), string(outBody))
				return
			}
			h.recordUsage(r.Context(), sess, cand.Provider.ID, cand.Model, upResp.Usage,
				http.StatusOK, "", r.URL.Path, time.Since(start), string(body), string(outBody))
			return
		}

		// Failure: classify the error and report to state machine.
		cls := upstream.ClassifyDispatchError(dispatchErr)
		outcome := llmdomain.AttemptOutcome{
			Success:    false,
			StatusCode: cls.StatusCode,
			Message:    dispatchErr.Error(),
		}
		_ = h.lookup.ReportAttempt(r.Context(), cand.MemberID, outcome)

		// Record a usage row for this failed attempt.
		errMsg := dispatchErr.Error()
		if resolution.Kind == llmdomain.RouteKindGroup {
			errMsg = fmt.Sprintf("[group %s#priority=%d attempt] %s", resolution.GroupName, i, dispatchErr.Error())
		}
		h.recordUsage(r.Context(), sess, cand.Provider.ID, cand.Model, upstream.Usage{},
			int32(cls.StatusCode), errMsg, r.URL.Path, time.Since(start), string(body), "")

		lastErr = dispatchErr

		if !cls.FailOver {
			// Non-retryable error (4xx client error): stop immediately.
			status, msg := dispatchStatusFor(dispatchErr)
			httpx.WriteError(w, status, msg)
			return
		}
		// Retryable: continue to next candidate.
	}

	// All candidates exhausted.
	if resolution.Kind == llmdomain.RouteKindGroup {
		httpx.WriteError(w, http.StatusBadGateway,
			fmt.Sprintf("model group %q exhausted after %d attempt(s): %v", resolution.GroupName, len(resolution.Candidates), lastErr))
	} else {
		status, msg := dispatchStatusFor(lastErr)
		if status == 0 {
			status = http.StatusBadGateway
		}
		httpx.WriteError(w, status, msg)
	}
}

// dispatchStatusFor maps adapter-level errors onto HTTP statuses + a
// user-facing message. UpstreamError surfaces the upstream's own
// status; sentinel errors map to 501/500; everything else is 502.
func dispatchStatusFor(err error) (int, string) {
	var ue *upstream.UpstreamError
	if errors.As(err, &ue) {
		return ue.StatusCode, ue.Message
	}
	switch {
	case errors.Is(err, upstream.ErrStreamingUnsupported):
		return http.StatusNotImplemented, err.Error()
	case errors.Is(err, upstream.ErrBaseURLRequired):
		return http.StatusInternalServerError, err.Error()
	default:
		return http.StatusBadGateway, err.Error()
	}
}

// recordUsage writes one usage row. We log + swallow on failure so a
// transient DB hiccup never breaks a working API call. Synchronous
// (not in a goroutine) so the row is visible by the time the response
// returns; the call is tiny and not on the hot path.
func (h *Handler) recordUsage(
	ctx context.Context,
	sess *runnerdomain.AgentSession,
	providerID int64,
	model string,
	u upstream.Usage,
	status int32,
	errMessage string,
	path string,
	latency time.Duration,
	requestBody string,
	responseBody string,
) {
	rec := &llmdomain.UsageRecord{
		ProviderID:       providerID,
		Model:            model,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		LatencyMS:        int32(latency.Milliseconds()),
		StatusCode:       status,
		ErrorMessage:     errMessage,
		RequestPath:      path,
		RequestBody:      requestBody,
		ResponseBody:     responseBody,
	}
	if sess != nil {
		rec.SessionID = &sess.ID
	}
	_ = h.lookup.RecordUsage(ctx, rec)
}

// ---- body helpers ----

var errBodyTooLarge = errors.New("request body exceeds limit")

func readBoundedBody(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	buf, err := io.ReadAll(io.LimitReader(rc, maxRequestBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > maxRequestBody {
		return nil, errBodyTooLarge
	}
	return buf, nil
}

// writeError emits a compact JSON error. Matches the shape used by the
// admin handler so frontend code doesn't need a second renderer.
