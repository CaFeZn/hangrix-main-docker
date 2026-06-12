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
	ID        int64
	ProjectID int64
	RepoID    int64
	OwnerName string
	RepoName  string
	Purpose   string
	Role      string
	CreatedBy int64
	CreatedAt time.Time
}

type ProjectIssueLink struct {
	ID          int64
	ProjectID   int64
	RepoID      int64
	IssueID     int64
	OwnerName   string
	RepoName    string
	IssueNumber int64
	IssueTitle  string
	IssueState  string
	Kind        string
	Summary     string
	CreatedBy   int64
	CreatedAt   time.Time
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
	ID             int64
	ProjectID      int64
	SourceRepoID   *int64
	SourceIssueID  *int64
	OwnerName      string
	RepoName       string
	Description    string
	Reason         string
	ModuleBoundary string
	Status         RepoProposalStatus
	TargetRepoID   *int64
	CreatedBy      int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
