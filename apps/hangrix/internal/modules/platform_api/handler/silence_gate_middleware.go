package handler

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	silencedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
)

// SilenceGateMiddleware is a chi-compatible middleware that gates every
// /api/v1 call on the repo silence state. It runs after BearerAuth
// (which stores the session in the request context) and returns a
// 423 Locked JSON envelope when the repo is silenced and the session
// has no active override.
//
// Human browser sessions (no session token in context) pass through
// untouched — the gate only fires for authenticated agent sessions.
func SilenceGateMiddleware(gate silencedomain.SilenceGate) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := GetSession(r)
			if sess == nil || sess.RepoID == nil {
				// No agent session — likely a human browser request
				// or a session that hasn't been bound to a repo yet.
				next.ServeHTTP(w, r)
				return
			}

			err := gate.CheckSession(r.Context(), sess.ID, *sess.RepoID)
			if err != nil {
				var silenced *silencedomain.ErrRepoSilenced
				if errors.As(err, &silenced) {
					retryMsg := map[string]any{
						"error": silenced.Error(),
						"code":  "repo_silenced",
					}
					if silenced.ExpectedExitAt != nil {
						retryAfter := time.Until(*silenced.ExpectedExitAt).Seconds()
						if retryAfter > 0 {
							w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter))
						}
						retryMsg["expected_exit_at"] = silenced.ExpectedExitAt.Format(time.RFC3339)
					}
					WriteJSON(w, http.StatusLocked, retryMsg)
					return
				}
				// Non-silence errors (DB failures) are internal errors.
				WriteError(w, http.StatusInternalServerError, "internal error checking silence state")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
