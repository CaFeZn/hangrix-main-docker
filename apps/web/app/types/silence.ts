// Silence source values mirror the Go domain constants.
export type SilenceSource = 'manual' | 'schedule' | 'api'

// RepoSilence is the silence state for a repository, returned by
// GET /api/repos/{owner}/{name}/silence or embedded in the repo
// detail response.
export interface RepoSilence {
  active: boolean
  source: SilenceSource | null
  source_ref: string
  entered_at?: string       // RFC3339
  expected_exit_at?: string // RFC3339, nil for manual
  reason?: string
  overrides: number         // count of active overrides
}

// SessionSilenceView is the per-session silence view embedded in
// agent session list responses.
export interface SessionSilenceView {
  silenced: boolean
  overridden: boolean
  expected_exit_at?: string // RFC3339
}

// SilenceAuditEntry is one audit event in the silence audit log.
export interface SilenceAuditEntry {
  id: number
  event: string          // "entered" | "exited" | "override_granted" | "override_revoked" | "suspended" | "resumed"
  source: string
  actor_id?: number | null
  session_id?: number | null
  payload: string        // JSON-encoded payload
  created_at: string     // RFC3339
}

// SilenceOverride is a per-session silence exemption.
export interface SilenceOverride {
  session_id: number
  granted_by: number
  reason: string
  expires_at?: string | null  // RFC3339, null = until revoked
  granted_at: string          // RFC3339
  revoked_at?: string | null
}
