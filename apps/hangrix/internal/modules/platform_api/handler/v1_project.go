package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func v1ReadProject(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := GetActor(r)
		if p == nil {
			WriteError(w, http.StatusUnauthorized, "missing actor")
			return
		}
		projectID, ok := parseProjectID(w, r)
		if !ok {
			return
		}
		result, err := api.ReadProject(r.Context(), p, projectID)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteOK(w, result)
	}
}

func v1LinkProjectRepo(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := GetActor(r)
		if p == nil {
			WriteError(w, http.StatusUnauthorized, "missing actor")
			return
		}
		projectID, ok := parseProjectID(w, r)
		if !ok {
			return
		}
		var req struct {
			RepoID  int64  `json:"repo_id"`
			Purpose string `json:"purpose"`
			Role    string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		result, err := api.LinkProjectRepo(r.Context(), p, projectID, req.RepoID, req.Purpose, req.Role)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteOK(w, result)
	}
}

func v1LinkProjectIssue(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := GetActor(r)
		if p == nil {
			WriteError(w, http.StatusUnauthorized, "missing actor")
			return
		}
		projectID, ok := parseProjectID(w, r)
		if !ok {
			return
		}
		var req struct {
			RepoID      int64  `json:"repo_id"`
			IssueNumber int64  `json:"issue_number"`
			Kind        string `json:"kind"`
			Summary     string `json:"summary"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		result, err := api.LinkProjectIssue(r.Context(), p, projectID, req.RepoID, req.IssueNumber, req.Kind, req.Summary)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteOK(w, result)
	}
}

func v1CreateProjectRepoProposal(api AgentAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := GetActor(r)
		if p == nil {
			WriteError(w, http.StatusUnauthorized, "missing actor")
			return
		}
		projectID, ok := parseProjectID(w, r)
		if !ok {
			return
		}
		var req struct {
			OwnerName      string `json:"owner_name"`
			RepoName       string `json:"repo_name"`
			Description    string `json:"description"`
			Reason         string `json:"reason"`
			ModuleBoundary string `json:"module_boundary"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		result, err := api.CreateProjectRepoProposal(r.Context(), p, projectID, req.OwnerName, req.RepoName, req.Description, req.Reason, req.ModuleBoundary)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteOK(w, result)
	}
}

func parseProjectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "projectID"), 10, 64)
	if err != nil || projectID <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid project_id")
		return 0, false
	}
	return projectID, true
}
