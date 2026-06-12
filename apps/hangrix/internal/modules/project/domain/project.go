package domain

import (
	"context"
	"errors"
	"time"
)

type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

func (v Visibility) Valid() bool { return v == VisibilityPublic || v == VisibilityPrivate }

type OwnerKind string

const (
	OwnerKindUser OwnerKind = "user"
	OwnerKindOrg  OwnerKind = "org"
)

func (k OwnerKind) Valid() bool { return k == OwnerKindUser || k == OwnerKindOrg }

type Project struct {
	ID               int64
	OwnerKind        OwnerKind
	OwnerID          int64
	OwnerName        string
	Name             string
	Description      string
	Visibility       Visibility
	Architecture     string
	ModuleBoundaries string
	CreatedBy        int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ProjectRepo struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	RepoID    int64     `json:"repo_id"`
	OwnerName string    `json:"owner_name"`
	RepoName  string    `json:"repo_name"`
	Purpose   string    `json:"purpose"`
	Role      string    `json:"role"`
	CreatedBy int64     `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type ProjectIssueLink struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"project_id"`
	RepoID      int64     `json:"repo_id"`
	IssueID     int64     `json:"issue_id"`
	OwnerName   string    `json:"owner_name"`
	RepoName    string    `json:"repo_name"`
	IssueNumber int64     `json:"issue_number"`
	IssueTitle  string    `json:"issue_title"`
	IssueState  string    `json:"issue_state"`
	Kind        string    `json:"kind"`
	Summary     string    `json:"summary"`
	CreatedBy   int64     `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type RepoProposalStatus string

const (
	RepoProposalPending     RepoProposalStatus = "pending"
	RepoProposalApproved    RepoProposalStatus = "approved"
	RepoProposalRejected    RepoProposalStatus = "rejected"
	RepoProposalProvisioned RepoProposalStatus = "provisioned"
)

func (s RepoProposalStatus) Valid() bool {
	switch s {
	case RepoProposalPending, RepoProposalApproved, RepoProposalRejected, RepoProposalProvisioned:
		return true
	default:
		return false
	}
}

type RepoProposal struct {
	ID             int64              `json:"id"`
	ProjectID      int64              `json:"project_id"`
	SourceRepoID   *int64             `json:"source_repo_id,omitempty"`
	SourceIssueID  *int64             `json:"source_issue_id,omitempty"`
	OwnerName      string             `json:"owner_name"`
	RepoName       string             `json:"repo_name"`
	Description    string             `json:"description"`
	Reason         string             `json:"reason"`
	ModuleBoundary string             `json:"module_boundary"`
	Status         RepoProposalStatus `json:"status"`
	TargetRepoID   *int64             `json:"target_repo_id,omitempty"`
	CreatedBy      int64              `json:"created_by"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

var (
	ErrProjectNotFound = errors.New("project not found")
	ErrProjectConflict = errors.New("project already exists")
	ErrLinkConflict    = errors.New("project link already exists")
)

type Store interface {
	Create(ctx context.Context, p *Project) (*Project, error)
	GetByID(ctx context.Context, id int64) (*Project, error)
	GetByOwnerAndName(ctx context.Context, ownerKind OwnerKind, ownerID int64, name string) (*Project, error)
	ListReadable(ctx context.Context, userID int64, admin bool, offset, limit int32) ([]*Project, int64, error)
	Update(ctx context.Context, p *Project) (*Project, error)

	AddRepo(ctx context.Context, projectID, repoID, createdBy int64, purpose, role string) (*ProjectRepo, error)
	ListRepos(ctx context.Context, projectID int64) ([]*ProjectRepo, error)
	RemoveRepo(ctx context.Context, projectID, repoID int64) error

	LinkIssue(ctx context.Context, projectID, repoID, issueID, createdBy int64, kind, summary string) (*ProjectIssueLink, error)
	ListIssueLinks(ctx context.Context, projectID int64) ([]*ProjectIssueLink, error)

	CreateRepoProposal(ctx context.Context, p *RepoProposal) (*RepoProposal, error)
	ListRepoProposals(ctx context.Context, projectID int64) ([]*RepoProposal, error)
	UpdateRepoProposalStatus(ctx context.Context, projectID, proposalID int64, status RepoProposalStatus, targetRepoID *int64) (*RepoProposal, error)
}
