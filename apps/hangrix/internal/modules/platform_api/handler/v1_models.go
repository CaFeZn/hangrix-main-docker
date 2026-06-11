package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
)

// v1GetModel returns a factory that handles GET /api/v1/models/{name}.
// It returns the ModelSpec for the given model name, authenticated by
// the bearer token and gated by IssueGate / SilenceGate middleware.
func v1GetModel(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		name = strings.TrimSpace(name)
		if name == "" {
			httpx.WriteError(w, http.StatusBadRequest, "model name is required")
			return
		}

		p := requireActor(w, r)
		if p == nil {
			return // requireActor already wrote the error
		}

		result, err := api.GetModel(r.Context(), p, name)
		if err != nil {
			if errors.Is(err, apidomain.ErrModelNotFound) {
				httpx.WriteError(w, http.StatusNotFound, "model not found")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}

		httpx.WriteJSON(w, http.StatusOK, result)
	}
}
