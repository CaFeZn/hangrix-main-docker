-- name: GetState :one
SELECT repo_id, active, source, source_ref, entered_at,
       expected_exit_at, reason, updated_at
FROM repo_silence_state
WHERE repo_id = sqlc.arg('repo_id');

-- name: UpdateState :execrows
UPDATE repo_silence_state
SET active           = sqlc.arg('active'),
    source           = sqlc.arg('source'),
    source_ref       = sqlc.arg('source_ref'),
    entered_at       = sqlc.narg('entered_at'),
    expected_exit_at = sqlc.narg('expected_exit_at'),
    reason           = sqlc.arg('reason'),
    updated_at       = now()
WHERE repo_id = sqlc.arg('repo_id')
  AND updated_at = sqlc.arg('updated_at_witness');

-- name: UpsertState :exec
INSERT INTO repo_silence_state
    (repo_id, active, source, source_ref, entered_at,
     expected_exit_at, reason, updated_at)
VALUES (
    sqlc.arg('repo_id'),
    sqlc.arg('active'),
    sqlc.arg('source'),
    sqlc.arg('source_ref'),
    sqlc.narg('entered_at'),
    sqlc.narg('expected_exit_at'),
    sqlc.arg('reason'),
    now()
)
ON CONFLICT (repo_id) DO UPDATE SET
    active           = EXCLUDED.active,
    source           = EXCLUDED.source,
    source_ref       = EXCLUDED.source_ref,
    entered_at       = EXCLUDED.entered_at,
    expected_exit_at = EXCLUDED.expected_exit_at,
    reason           = EXCLUDED.reason,
    updated_at       = now();

-- name: AppendAudit :exec
INSERT INTO repo_silence_audit
    (repo_id, event, source, actor_id, session_id, payload)
VALUES (
    sqlc.arg('repo_id'),
    sqlc.arg('event'),
    sqlc.arg('source'),
    sqlc.narg('actor_id'),
    sqlc.narg('session_id'),
    sqlc.arg('payload')
);

-- name: ListAudit :many
SELECT id, repo_id, event, source, actor_id, session_id,
       payload, created_at
FROM repo_silence_audit
WHERE repo_id = sqlc.arg('repo_id')
ORDER BY created_at DESC
LIMIT sqlc.arg('limit');

-- name: GrantOverride :exec
INSERT INTO repo_silence_overrides
    (session_id, repo_id, granted_by, reason, expires_at)
VALUES (
    sqlc.arg('session_id'),
    sqlc.arg('repo_id'),
    sqlc.arg('granted_by'),
    sqlc.arg('reason'),
    sqlc.narg('expires_at')
)
ON CONFLICT (session_id) DO UPDATE SET
    repo_id    = EXCLUDED.repo_id,
    granted_by = EXCLUDED.granted_by,
    reason     = EXCLUDED.reason,
    expires_at = EXCLUDED.expires_at,
    granted_at = now(),
    revoked_at = NULL;

-- name: RevokeOverride :exec
UPDATE repo_silence_overrides
SET revoked_at = now()
WHERE session_id = sqlc.arg('session_id')
  AND revoked_at IS NULL;

-- name: ActiveOverride :one
SELECT session_id, repo_id, granted_by, reason, expires_at,
       granted_at, revoked_at
FROM repo_silence_overrides
WHERE session_id = sqlc.arg('session_id')
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > now());

-- name: ListOverrides :many
SELECT session_id, repo_id, granted_by, reason, expires_at,
       granted_at, revoked_at
FROM repo_silence_overrides
WHERE repo_id = sqlc.arg('repo_id')
  AND revoked_at IS NULL
ORDER BY granted_at DESC;
