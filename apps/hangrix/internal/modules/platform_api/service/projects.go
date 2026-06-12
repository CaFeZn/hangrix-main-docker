package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
	projectdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/project/domain"
)

type apiProjectDetail struct {
	Project       *projectdomain.Project            `json:"project"`
	Repos         []*projectdomain.ProjectRepo      `json:"repos"`
	IssueLinks    []*projectdomain.ProjectIssueLink `json:"issue_links"`
	RepoProposals []*projectdomain.RepoProposal     `json:"repo_proposals"`
}

func (s *APIService) ReadProject(ctx context.Context, p *apidomain.Actor, projectID int64) (any, error) {
	if s.r.deps.Projects == nil {
		return nil, errors.New("project store unavailable")
	}
	if projectID <= 0 {
		return nil, errors.New("project_id is required")
	}
	if err := s.ensureCurrentRepoLinked(ctx, p, projectID); err != nil {
		return nil, err
	}
	return s.projectDetail(ctx, projectID)
}

func (s *APIService) LinkProjectRepo(ctx context.Context, p *apidomain.Actor, projectID, repoID int64, purpose, role string) (any, error) {
	if s.r.deps.Projects == nil {
		return nil, errors.New("project store unavailable")
	}
	if projectID <= 0 || repoID <= 0 {
		return nil, errors.New("project_id and repo_id are required")
	}
	if err := s.ensureCurrentRepoLinked(ctx, p, projectID); err != nil {
		return nil, err
	}
	return s.r.deps.Projects.AddRepo(ctx, projectID, repoID, 0, strings.TrimSpace(purpose), strings.TrimSpace(role))
}

func (s *APIService) LinkProjectIssue(ctx context.Context, p *apidomain.Actor, projectID, repoID, issueNumber int64, kind, summary string) (any, error) {
	if s.r.deps.Projects == nil {
		return nil, errors.New("project store unavailable")
	}
	if projectID <= 0 || repoID <= 0 || issueNumber <= 0 {
		return nil, errors.New("project_id, repo_id, and issue_number are required")
	}
	if err := s.ensureCurrentRepoLinked(ctx, p, projectID); err != nil {
		return nil, err
	}
	iss, err := s.r.deps.Issues.GetByNumber(ctx, repoID, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("load target issue: %w", err)
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "implementation"
	}
	return s.r.deps.Projects.LinkIssue(ctx, projectID, repoID, iss.ID, 0, kind, strings.TrimSpace(summary))
}

func (s *APIService) CreateProjectRepoProposal(ctx context.Context, p *apidomain.Actor, projectID int64, ownerName, repoName, description, reason, moduleBoundary string) (any, error) {
	if s.r.deps.Projects == nil {
		return nil, errors.New("project store unavailable")
	}
	if projectID <= 0 || strings.TrimSpace(repoName) == "" {
		return nil, errors.New("project_id and repo_name are required")
	}
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := s.ensureCurrentRepoLinked(ctx, p, projectID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(ownerName) == "" {
		ownerName = scope.repo.OwnerName
	}
	return s.r.deps.Projects.CreateRepoProposal(ctx, &projectdomain.RepoProposal{
		ProjectID:      projectID,
		SourceRepoID:   &scope.repo.ID,
		SourceIssueID:  &scope.issue.ID,
		OwnerName:      strings.TrimSpace(ownerName),
		RepoName:       strings.TrimSpace(repoName),
		Description:    strings.TrimSpace(description),
		Reason:         strings.TrimSpace(reason),
		ModuleBoundary: strings.TrimSpace(moduleBoundary),
		Status:         projectdomain.RepoProposalPending,
	})
}

func (s *APIService) ensureCurrentRepoLinked(ctx context.Context, p *apidomain.Actor, projectID int64) error {
	scope, err := s.mustLoadScope(ctx, p)
	if err != nil {
		return err
	}
	repos, err := s.r.deps.Projects.ListRepos(ctx, projectID)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if repo.RepoID == scope.repo.ID {
			return nil
		}
	}
	return errors.New("current repo is not linked to this project")
}

func (s *APIService) projectDetail(ctx context.Context, projectID int64) (*apiProjectDetail, error) {
	p, err := s.r.deps.Projects.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	repos, err := s.r.deps.Projects.ListRepos(ctx, projectID)
	if err != nil {
		return nil, err
	}
	links, err := s.r.deps.Projects.ListIssueLinks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	proposals, err := s.r.deps.Projects.ListRepoProposals(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &apiProjectDetail{Project: p, Repos: repos, IssueLinks: links, RepoProposals: proposals}, nil
}
