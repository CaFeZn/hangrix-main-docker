package infra

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/project/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type PostgresStore struct {
	pool *pgxpool.Pool
}

type PostgresStoreDeps struct {
	Pool *pgxpool.Pool
}

func NewPostgresStore(deps *PostgresStoreDeps) *PostgresStore {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("project migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_project", "."); err != nil {
		panic(fmt.Errorf("apply project migrations: %w", err))
	}
	return &PostgresStore{pool: deps.Pool}
}

func (s *PostgresStore) Create(ctx context.Context, p *domain.Project) (*domain.Project, error) {
	var row pgx.Row
	switch p.OwnerKind {
	case domain.OwnerKindUser:
		row = s.pool.QueryRow(ctx, `
			INSERT INTO projects (owner_user_id, name, description, visibility, architecture, module_boundaries, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, 0))
			RETURNING id`, p.OwnerID, p.Name, p.Description, string(p.Visibility), p.Architecture, p.ModuleBoundaries, p.CreatedBy)
	case domain.OwnerKindOrg:
		row = s.pool.QueryRow(ctx, `
			INSERT INTO projects (owner_org_id, name, description, visibility, architecture, module_boundaries, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, 0))
			RETURNING id`, p.OwnerID, p.Name, p.Description, string(p.Visibility), p.Architecture, p.ModuleBoundaries, p.CreatedBy)
	default:
		return nil, errors.New("invalid owner kind")
	}
	var id int64
	if err := row.Scan(&id); err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrProjectConflict
		}
		return nil, err
	}
	return s.GetByID(ctx, id)
}

func (s *PostgresStore) GetByID(ctx context.Context, id int64) (*domain.Project, error) {
	return scanProject(s.pool.QueryRow(ctx, projectSelect()+` WHERE p.id = $1`, id))
}

func (s *PostgresStore) GetByOwnerAndName(ctx context.Context, ownerKind domain.OwnerKind, ownerID int64, name string) (*domain.Project, error) {
	switch ownerKind {
	case domain.OwnerKindUser:
		return scanProject(s.pool.QueryRow(ctx, projectSelect()+` WHERE p.owner_user_id = $1 AND p.name = $2`, ownerID, name))
	case domain.OwnerKindOrg:
		return scanProject(s.pool.QueryRow(ctx, projectSelect()+` WHERE p.owner_org_id = $1 AND p.name = $2`, ownerID, name))
	default:
		return nil, domain.ErrProjectNotFound
	}
}

func (s *PostgresStore) ListReadable(ctx context.Context, userID int64, admin bool, offset, limit int32) ([]*domain.Project, int64, error) {
	where := `
		WHERE $1::bool
		   OR p.visibility = 'public'
		   OR p.owner_user_id = $2
		   OR EXISTS (
		     SELECT 1 FROM organization_members om
		     WHERE om.org_id = p.owner_org_id AND om.user_id = $2
		   )`
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM projects p `+where, admin, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, projectSelect()+where+` ORDER BY p.updated_at DESC, p.id DESC LIMIT $3 OFFSET $4`, admin, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*domain.Project{}
	for rows.Next() {
		p, err := scanProjectRows(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (s *PostgresStore) Update(ctx context.Context, p *domain.Project) (*domain.Project, error) {
	if _, err := s.pool.Exec(ctx, `
		UPDATE projects
		SET description = $2, visibility = $3, architecture = $4, module_boundaries = $5, updated_at = now()
		WHERE id = $1`, p.ID, p.Description, string(p.Visibility), p.Architecture, p.ModuleBoundaries); err != nil {
		return nil, err
	}
	return s.GetByID(ctx, p.ID)
}

func (s *PostgresStore) AddRepo(ctx context.Context, projectID, repoID, createdBy int64, purpose, role string) (*domain.ProjectRepo, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO project_repos (project_id, repo_id, purpose, role, created_by)
		VALUES ($1, $2, $3, $4, NULLIF($5, 0))
		RETURNING id`, projectID, repoID, purpose, role, createdBy).Scan(&id)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrLinkConflict
		}
		return nil, err
	}
	return s.getProjectRepo(ctx, projectID, repoID)
}

func (s *PostgresStore) ListRepos(ctx context.Context, projectID int64) ([]*domain.ProjectRepo, error) {
	rows, err := s.pool.Query(ctx, projectRepoSelect()+` WHERE pr.project_id = $1 ORDER BY pr.created_at ASC, pr.id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.ProjectRepo{}
	for rows.Next() {
		item, err := scanProjectRepoRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) RemoveRepo(ctx context.Context, projectID, repoID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM project_repos WHERE project_id = $1 AND repo_id = $2`, projectID, repoID)
	return err
}

func (s *PostgresStore) LinkIssue(ctx context.Context, projectID, repoID, issueID, createdBy int64, kind, summary string) (*domain.ProjectIssueLink, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO project_issue_links (project_id, repo_id, issue_id, kind, summary, created_by)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0))
		RETURNING id`, projectID, repoID, issueID, kind, summary, createdBy).Scan(&id)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, domain.ErrLinkConflict
		}
		return nil, err
	}
	return s.getIssueLink(ctx, projectID, issueID)
}

func (s *PostgresStore) ListIssueLinks(ctx context.Context, projectID int64) ([]*domain.ProjectIssueLink, error) {
	rows, err := s.pool.Query(ctx, issueLinkSelect()+` WHERE pil.project_id = $1 ORDER BY pil.created_at DESC, pil.id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.ProjectIssueLink{}
	for rows.Next() {
		item, err := scanIssueLinkRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateRepoProposal(ctx context.Context, p *domain.RepoProposal) (*domain.RepoProposal, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO project_repo_proposals (
			project_id, source_repo_id, source_issue_id, owner_name, repo_name,
			description, reason, module_boundary, status, target_repo_id, created_by
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, 0))
		RETURNING id`,
		p.ProjectID, p.SourceRepoID, p.SourceIssueID, p.OwnerName, p.RepoName,
		p.Description, p.Reason, p.ModuleBoundary, string(p.Status), p.TargetRepoID, p.CreatedBy,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.getRepoProposal(ctx, p.ProjectID, id)
}

func (s *PostgresStore) ListRepoProposals(ctx context.Context, projectID int64) ([]*domain.RepoProposal, error) {
	rows, err := s.pool.Query(ctx, repoProposalSelect()+` WHERE project_id = $1 ORDER BY created_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.RepoProposal{}
	for rows.Next() {
		item, err := scanRepoProposalRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateRepoProposalStatus(ctx context.Context, projectID, proposalID int64, status domain.RepoProposalStatus, targetRepoID *int64) (*domain.RepoProposal, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE project_repo_proposals
		SET status = $3, target_repo_id = $4, updated_at = now()
		WHERE project_id = $1 AND id = $2`, projectID, proposalID, string(status), targetRepoID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, domain.ErrProjectNotFound
	}
	return s.getRepoProposal(ctx, projectID, proposalID)
}

func (s *PostgresStore) getProjectRepo(ctx context.Context, projectID, repoID int64) (*domain.ProjectRepo, error) {
	return scanProjectRepo(s.pool.QueryRow(ctx, projectRepoSelect()+` WHERE pr.project_id = $1 AND pr.repo_id = $2`, projectID, repoID))
}

func (s *PostgresStore) getIssueLink(ctx context.Context, projectID, issueID int64) (*domain.ProjectIssueLink, error) {
	return scanIssueLink(s.pool.QueryRow(ctx, issueLinkSelect()+` WHERE pil.project_id = $1 AND pil.issue_id = $2`, projectID, issueID))
}

func (s *PostgresStore) getRepoProposal(ctx context.Context, projectID, id int64) (*domain.RepoProposal, error) {
	return scanRepoProposal(s.pool.QueryRow(ctx, repoProposalSelect()+` WHERE project_id = $1 AND id = $2`, projectID, id))
}

func projectSelect() string {
	return `
		SELECT p.id,
		       CASE WHEN p.owner_user_id IS NOT NULL THEN 'user' ELSE 'org' END AS owner_kind,
		       COALESCE(p.owner_user_id, p.owner_org_id) AS owner_id,
		       COALESCE(u.username, o.name) AS owner_name,
		       p.name, p.description, p.visibility, p.architecture, p.module_boundaries,
		       COALESCE(p.created_by, 0), p.created_at, p.updated_at
		FROM projects p
		LEFT JOIN users u ON u.id = p.owner_user_id
		LEFT JOIN organizations o ON o.id = p.owner_org_id AND o.deleted_at IS NULL`
}

func projectRepoSelect() string {
	return `
		SELECT pr.id, pr.project_id, pr.repo_id, COALESCE(u.username, o.name) AS owner_name,
		       r.name, pr.purpose, pr.role, COALESCE(pr.created_by, 0), pr.created_at
		FROM project_repos pr
		JOIN repos r ON r.id = pr.repo_id
		LEFT JOIN users u ON u.id = r.owner_user_id
		LEFT JOIN organizations o ON o.id = r.owner_org_id AND o.deleted_at IS NULL`
}

func issueLinkSelect() string {
	return `
		SELECT pil.id, pil.project_id, pil.repo_id, pil.issue_id,
		       COALESCE(u.username, o.name) AS owner_name, r.name,
		       i.number, i.title, i.state, pil.kind, pil.summary,
		       COALESCE(pil.created_by, 0), pil.created_at
		FROM project_issue_links pil
		JOIN repos r ON r.id = pil.repo_id
		JOIN issues i ON i.id = pil.issue_id
		LEFT JOIN users u ON u.id = r.owner_user_id
		LEFT JOIN organizations o ON o.id = r.owner_org_id AND o.deleted_at IS NULL`
}

func repoProposalSelect() string {
	return `
		SELECT id, project_id, source_repo_id, source_issue_id, owner_name, repo_name,
		       description, reason, module_boundary, status, target_repo_id,
		       COALESCE(created_by, 0), created_at, updated_at
		FROM project_repo_proposals`
}

func scanProject(row pgx.Row) (*domain.Project, error) {
	p, err := scanProjectScanner(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrProjectNotFound
	}
	return p, err
}

func scanProjectRows(rows pgx.Rows) (*domain.Project, error) { return scanProjectScanner(rows) }

type scanner interface {
	Scan(dest ...any) error
}

func scanProjectScanner(row scanner) (*domain.Project, error) {
	var p domain.Project
	var ownerKind, visibility string
	if err := row.Scan(&p.ID, &ownerKind, &p.OwnerID, &p.OwnerName, &p.Name, &p.Description, &visibility, &p.Architecture, &p.ModuleBoundaries, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.OwnerKind = domain.OwnerKind(ownerKind)
	p.Visibility = domain.Visibility(visibility)
	return &p, nil
}

func scanProjectRepo(row pgx.Row) (*domain.ProjectRepo, error) {
	p, err := scanProjectRepoScanner(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrProjectNotFound
	}
	return p, err
}

func scanProjectRepoRows(rows pgx.Rows) (*domain.ProjectRepo, error) {
	return scanProjectRepoScanner(rows)
}

func scanProjectRepoScanner(row scanner) (*domain.ProjectRepo, error) {
	var p domain.ProjectRepo
	if err := row.Scan(&p.ID, &p.ProjectID, &p.RepoID, &p.OwnerName, &p.RepoName, &p.Purpose, &p.Role, &p.CreatedBy, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func scanIssueLink(row pgx.Row) (*domain.ProjectIssueLink, error) {
	p, err := scanIssueLinkScanner(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrProjectNotFound
	}
	return p, err
}

func scanIssueLinkRows(rows pgx.Rows) (*domain.ProjectIssueLink, error) {
	return scanIssueLinkScanner(rows)
}

func scanIssueLinkScanner(row scanner) (*domain.ProjectIssueLink, error) {
	var p domain.ProjectIssueLink
	if err := row.Scan(&p.ID, &p.ProjectID, &p.RepoID, &p.IssueID, &p.OwnerName, &p.RepoName, &p.IssueNumber, &p.IssueTitle, &p.IssueState, &p.Kind, &p.Summary, &p.CreatedBy, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func scanRepoProposal(row pgx.Row) (*domain.RepoProposal, error) {
	p, err := scanRepoProposalScanner(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrProjectNotFound
	}
	return p, err
}

func scanRepoProposalRows(rows pgx.Rows) (*domain.RepoProposal, error) {
	return scanRepoProposalScanner(rows)
}

func scanRepoProposalScanner(row scanner) (*domain.RepoProposal, error) {
	var p domain.RepoProposal
	var status string
	if err := row.Scan(&p.ID, &p.ProjectID, &p.SourceRepoID, &p.SourceIssueID, &p.OwnerName, &p.RepoName, &p.Description, &p.Reason, &p.ModuleBoundary, &status, &p.TargetRepoID, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Status = domain.RepoProposalStatus(status)
	return &p, nil
}
