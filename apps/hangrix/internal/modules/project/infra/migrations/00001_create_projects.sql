-- +goose Up
CREATE TABLE IF NOT EXISTS projects (
  id BIGSERIAL PRIMARY KEY,
  owner_user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
  owner_org_id BIGINT REFERENCES organizations(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('public', 'private')),
  architecture TEXT NOT NULL DEFAULT '',
  module_boundaries TEXT NOT NULL DEFAULT '',
  created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((owner_user_id IS NOT NULL)::int + (owner_org_id IS NOT NULL)::int = 1),
  UNIQUE (owner_user_id, name),
  UNIQUE (owner_org_id, name)
);

CREATE INDEX IF NOT EXISTS idx_projects_owner_user ON projects(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_projects_owner_org ON projects(owner_org_id);

CREATE TABLE IF NOT EXISTS project_repos (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  repo_id BIGINT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  purpose TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT '',
  created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (project_id, repo_id)
);

CREATE INDEX IF NOT EXISTS idx_project_repos_project ON project_repos(project_id);
CREATE INDEX IF NOT EXISTS idx_project_repos_repo ON project_repos(repo_id);

CREATE TABLE IF NOT EXISTS project_issue_links (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  repo_id BIGINT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  kind TEXT NOT NULL DEFAULT 'implementation',
  summary TEXT NOT NULL DEFAULT '',
  created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (project_id, issue_id)
);

CREATE INDEX IF NOT EXISTS idx_project_issue_links_project ON project_issue_links(project_id);
CREATE INDEX IF NOT EXISTS idx_project_issue_links_repo ON project_issue_links(repo_id);

CREATE TABLE IF NOT EXISTS project_repo_proposals (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_repo_id BIGINT REFERENCES repos(id) ON DELETE SET NULL,
  source_issue_id BIGINT REFERENCES issues(id) ON DELETE SET NULL,
  owner_name TEXT NOT NULL,
  repo_name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  module_boundary TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'provisioned')),
  target_repo_id BIGINT REFERENCES repos(id) ON DELETE SET NULL,
  created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_project_repo_proposals_project ON project_repo_proposals(project_id);
CREATE INDEX IF NOT EXISTS idx_project_repo_proposals_status ON project_repo_proposals(status);

-- +goose Down
DROP TABLE IF EXISTS project_repo_proposals;
DROP TABLE IF EXISTS project_issue_links;
DROP TABLE IF EXISTS project_repos;
DROP TABLE IF EXISTS projects;
