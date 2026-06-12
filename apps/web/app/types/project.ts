export type ProjectVisibility = 'public' | 'private'

export interface ProjectRepo {
  id: number
  project_id: number
  repo_id: number
  owner_name: string
  repo_name: string
  purpose: string
  role: string
  created_by: number
  created_at: string
}

export interface ProjectIssueLink {
  id: number
  project_id: number
  repo_id: number
  issue_id: number
  owner_name: string
  repo_name: string
  issue_number: number
  issue_title: string
  issue_state: string
  kind: string
  summary: string
  created_by: number
  created_at: string
}

export type RepoProposalStatus = 'pending' | 'approved' | 'rejected' | 'provisioned'

export interface RepoProposal {
  id: number
  project_id: number
  source_repo_id?: number
  source_issue_id?: number
  owner_name: string
  repo_name: string
  description: string
  reason: string
  module_boundary: string
  status: RepoProposalStatus
  target_repo_id?: number
  created_by: number
  created_at: string
  updated_at: string
}

export interface Project {
  id: number
  owner_kind: 'user' | 'org'
  owner_id: number
  owner_name: string
  name: string
  description: string
  visibility: ProjectVisibility
  architecture: string
  module_boundaries: string
  repos?: ProjectRepo[]
  issue_links?: ProjectIssueLink[]
  repo_proposals?: RepoProposal[]
  created_at: string
  updated_at: string
}

export interface ProjectListResp {
  items: Project[]
  total: number
  limit: number
  offset: number
}
