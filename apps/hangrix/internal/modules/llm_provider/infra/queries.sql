
-- name: ExportUsageCSV :many
-- Page through the usage log in small chunks so the zip writer can stream
-- rows without holding the whole export in memory.
SELECT u.id, u.session_id, u.provider_id, u.model,
       u.prompt_tokens, u.completion_tokens, u.total_tokens,
       u.latency_ms, u.status_code, u.error_message, u.request_path,
       u.created_at,
       p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
ORDER BY u.created_at DESC
LIMIT sqlc.arg('lim')
OFFSET sqlc.arg('off');

-- name: ExportUsageJSONL :many
-- Page through the full usage log in small chunks so JSONL export can
-- stream request_body / response_body without loading the whole table.
SELECT u.id, u.session_id, u.provider_id, u.model,
       u.prompt_tokens, u.completion_tokens, u.total_tokens,
       u.latency_ms, u.status_code, u.error_message, u.request_path,
       u.created_at, u.request_body, u.response_body,
       p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
ORDER BY u.created_at DESC
LIMIT sqlc.arg('lim')
OFFSET sqlc.arg('off');

-- name: CreateProvider :one
INSERT INTO llm_providers (
    name, type, base_url, api_key_encrypted, allowed_models, actor_id
) VALUES (
    sqlc.arg('name'),
    sqlc.arg('type'),
    sqlc.arg('base_url'),
    sqlc.arg('api_key_encrypted'),
    sqlc.arg('allowed_models'),
    sqlc.arg('actor_id')
)
RETURNING *;

-- name: UpdateProvider :one
-- sqlc.narg('api_key_encrypted') makes the parameter nullable; COALESCE
-- keeps the stored sealed blob when the caller passes NULL (= no rotation).
UPDATE llm_providers SET
    base_url          = sqlc.arg('base_url'),
    api_key_encrypted = COALESCE(sqlc.narg('api_key_encrypted'), api_key_encrypted),
    allowed_models    = sqlc.arg('allowed_models'),
    disabled          = sqlc.arg('disabled'),
    updated_at        = NOW()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: SetProviderDisabled :one
-- Dedicated flip so the admin UI can toggle enable/disable without having
-- to round-trip the full UpdateProvider payload (and without risking a
-- stale base_url / allowed_models clobber).
UPDATE llm_providers SET
    disabled   = sqlc.arg('disabled'),
    updated_at = NOW()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: GetProviderByID :one
SELECT * FROM llm_providers WHERE id = sqlc.arg('id');

-- name: GetProviderByName :one
SELECT * FROM llm_providers WHERE name = sqlc.arg('name');

-- name: ListProviders :many
SELECT * FROM llm_providers ORDER BY name ASC;

-- name: DeleteProvider :execrows
DELETE FROM llm_providers WHERE id = sqlc.arg('id');

-- name: RecordUsage :exec
INSERT INTO llm_usage_log (
    session_id, provider_id, model,
    prompt_tokens, completion_tokens, total_tokens,
    latency_ms, status_code, error_message, request_path,
    request_body, response_body
) VALUES (
    sqlc.narg('session_id'),
    sqlc.arg('provider_id'),
    sqlc.arg('model'),
    sqlc.arg('prompt_tokens'),
    sqlc.arg('completion_tokens'),
    sqlc.arg('total_tokens'),
    sqlc.arg('latency_ms'),
    sqlc.arg('status_code'),
    sqlc.arg('error_message'),
    sqlc.arg('request_path'),
    sqlc.arg('request_body'),
    sqlc.arg('response_body')
);

-- name: ListUsage :many
-- Explicit column list excludes request_body/response_body so the list
-- query stays fast — the detail endpoint (GetUsageByID) carries the large
-- body columns on a single row.
SELECT u.id, u.session_id, u.provider_id, u.model,
       u.prompt_tokens, u.completion_tokens, u.total_tokens,
       u.latency_ms, u.status_code, u.error_message, u.request_path,
       u.created_at,
       p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'))
ORDER BY u.created_at DESC
LIMIT sqlc.arg('lim')
OFFSET sqlc.arg('off');

-- name: CountUsage :one
-- Mirrors ListUsage's WHERE clause so the admin usage page can render the
-- total row count alongside the paged window.
SELECT COUNT(*)::BIGINT
FROM llm_usage_log u
WHERE (sqlc.narg('provider_id')::BIGINT IS NULL OR u.provider_id = sqlc.narg('provider_id'))
  AND (sqlc.narg('since')::TIMESTAMPTZ IS NULL OR u.created_at >= sqlc.narg('since'));

-- name: GetUsageByID :one
-- Single-row detail query that includes the large body columns the list
-- endpoint deliberately omits. Used by the admin detail popup.
SELECT u.*, p.name AS provider_name
FROM llm_usage_log u
JOIN llm_providers p ON p.id = u.provider_id
WHERE u.id = sqlc.arg('id');

-- name: DeleteUsageBefore :execrows
-- Hard-deletes usage-log rows whose created_at is strictly before :cutoff.
-- Called by the background reaper (service/reaper.go); not exposed through
-- the domain.Repo interface or the admin handler.
DELETE FROM llm_usage_log WHERE created_at < sqlc.arg('cutoff');

-- ---- model groups ----

-- name: CreateGroup :one
INSERT INTO llm_model_groups (name, description, actor_id)
VALUES (sqlc.arg('name'), sqlc.arg('description'), sqlc.arg('actor_id'))
RETURNING *;

-- name: GetGroupByName :one
SELECT * FROM llm_model_groups WHERE name = sqlc.arg('name');

-- name: GetGroupByID :one
SELECT * FROM llm_model_groups WHERE id = sqlc.arg('id');

-- name: ListGroups :many
SELECT * FROM llm_model_groups ORDER BY name ASC;

-- name: UpdateGroup :one
UPDATE llm_model_groups SET
    description = sqlc.arg('description'),
    updated_at  = NOW()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteGroup :execrows
DELETE FROM llm_model_groups WHERE id = sqlc.arg('id');

-- name: CountGroupsByName :one
SELECT COUNT(*)::BIGINT FROM llm_model_groups WHERE name = sqlc.arg('name');

-- ---- model group members ----

-- name: ReplaceMembers :exec
-- Delete all existing members for the group, then insert the new list.
-- Callers must wrap both in a transaction.
DELETE FROM llm_model_group_members WHERE group_id = sqlc.arg('group_id');

-- name: InsertMember :exec
INSERT INTO llm_model_group_members (
    group_id, provider_id, model, priority
) VALUES (
    sqlc.arg('group_id'), sqlc.arg('provider_id'), sqlc.arg('model'), sqlc.arg('priority')
);

-- name: ListMembersByGroupID :many
-- Join provider columns so the service layer can compute MemberHealth
-- without a separate provider query per member.
SELECT
    m.id, m.group_id, m.provider_id,
    p.name AS provider_name,
    m.model, m.priority,
    m.manual_disabled, m.auto_disabled_until, m.backoff_step,
    m.last_failure_at, m.last_failure_msg,
    m.last_success_at, m.last_checked_at,
    p.disabled AS provider_disabled,
    m.created_at, m.updated_at
FROM llm_model_group_members m
JOIN llm_providers p ON p.id = m.provider_id
WHERE m.group_id = sqlc.arg('group_id')
ORDER BY m.priority ASC;

-- name: GetMemberByID :one
SELECT
    m.id, m.group_id, m.provider_id,
    p.name AS provider_name,
    m.model, m.priority,
    m.manual_disabled, m.auto_disabled_until, m.backoff_step,
    m.last_failure_at, m.last_failure_msg,
    m.last_success_at, m.last_checked_at,
    p.disabled AS provider_disabled,
    m.created_at, m.updated_at
FROM llm_model_group_members m
JOIN llm_providers p ON p.id = m.provider_id
WHERE m.id = sqlc.arg('id');

-- name: UpdateMemberHealth :execrows
-- Atomically update health fields. Each field is only touched when its
-- corresponding parameter is non-null (sqlc.narg). This single-statement
-- pattern avoids a read-then-write race on backoff_step.
UPDATE llm_model_group_members SET
    manual_disabled     = COALESCE(sqlc.narg('manual_disabled'), manual_disabled),
    auto_disabled_until = CASE WHEN sqlc.narg('auto_disabled_until')::TIMESTAMPTZ IS NOT NULL
                               THEN sqlc.narg('auto_disabled_until')
                               WHEN sqlc.narg('auto_disabled_until_null')::BOOLEAN
                               THEN NULL
                               ELSE auto_disabled_until END,
    backoff_step        = COALESCE(sqlc.narg('backoff_step'), backoff_step),
    last_failure_at     = COALESCE(sqlc.narg('last_failure_at'), last_failure_at),
    last_failure_msg    = COALESCE(sqlc.narg('last_failure_msg'), last_failure_msg),
    last_success_at     = COALESCE(sqlc.narg('last_success_at'), last_success_at),
    last_checked_at     = COALESCE(sqlc.narg('last_checked_at'), last_checked_at),
    updated_at          = NOW()
WHERE id = sqlc.arg('id');



-- ---- llm models ----

-- name: CreateModel :one
INSERT INTO llm_models (
    name, display_name, context_window, max_output_tokens,
    vision, reasoning, reasoning_effort_map, group_id, actor_id
) VALUES (
    sqlc.arg('name'), sqlc.arg('display_name'), sqlc.arg('context_window'), sqlc.arg('max_output_tokens'),
    sqlc.arg('vision'), sqlc.arg('reasoning'), sqlc.arg('reasoning_effort_map'), sqlc.arg('group_id'), sqlc.arg('actor_id')
)
RETURNING *;

-- name: GetModelByName :one
SELECT * FROM llm_models WHERE name = sqlc.arg('name');

-- name: GetModelByID :one
SELECT * FROM llm_models WHERE id = sqlc.arg('id');

-- name: ListModels :many
SELECT * FROM llm_models ORDER BY name ASC;

-- name: UpdateModel :one
UPDATE llm_models SET
    display_name         = sqlc.arg('display_name'),
    context_window       = sqlc.arg('context_window'),
    max_output_tokens    = sqlc.arg('max_output_tokens'),
    vision               = sqlc.arg('vision'),
    reasoning            = sqlc.arg('reasoning'),
    reasoning_effort_map = sqlc.arg('reasoning_effort_map'),
    updated_at           = NOW()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteModel :execrows
DELETE FROM llm_models WHERE id = sqlc.arg('id');

-- name: CountModelsByName :one
SELECT COUNT(*)::BIGINT FROM llm_models WHERE name = sqlc.arg('name');
