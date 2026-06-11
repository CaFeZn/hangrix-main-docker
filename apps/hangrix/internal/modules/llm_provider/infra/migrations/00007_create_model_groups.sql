-- +goose Up

-- llm_model_groups: one row per named model group.
-- A group name behaves as a model string from the agent's perspective.
CREATE TABLE llm_model_groups (
    id          BIGSERIAL    PRIMARY KEY,
    name        TEXT         NOT NULL UNIQUE,       -- [a-z0-9][a-z0-9-]{0,63}
    description TEXT         NOT NULL DEFAULT '',
    actor_id    BIGINT       NOT NULL REFERENCES actors(id) ON DELETE RESTRICT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- llm_model_group_members: (group, provider, model) triple + runtime health state.
-- Each row binds one upstream model from one provider into one group.
-- Health state is stored as raw columns; the derived MemberHealth enum is
-- computed in the service layer by evaluating manual_disabled, provider.disabled,
-- and auto_disabled_until against NOW().
CREATE TABLE llm_model_group_members (
    id                  BIGSERIAL    PRIMARY KEY,
    group_id            BIGINT       NOT NULL REFERENCES llm_model_groups(id) ON DELETE CASCADE,
    provider_id         BIGINT       NOT NULL REFERENCES llm_providers(id)    ON DELETE RESTRICT,
    model               TEXT         NOT NULL,            -- upstream model name; must be in provider.allowed_models
    priority            INTEGER      NOT NULL,            -- 0..N-1, lower = preferred

    -- Health state machine (raw fields; no derived enum stored)
    manual_disabled     BOOLEAN      NOT NULL DEFAULT FALSE,
    auto_disabled_until TIMESTAMPTZ,                     -- NULL = not auto-disabled; NOW() < this = disabled
    backoff_step        INTEGER      NOT NULL DEFAULT 0, -- exponential backoff tier (0/1/2/3...)
    last_failure_at     TIMESTAMPTZ,
    last_failure_msg    TEXT         NOT NULL DEFAULT '',
    last_success_at     TIMESTAMPTZ,
    last_checked_at     TIMESTAMPTZ,                     -- updated on every success or failure

    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    UNIQUE (group_id, provider_id, model),               -- same (group, provider, model) must not repeat
    UNIQUE (group_id, priority)                          -- priority must be unique within a group
);

CREATE INDEX llm_model_group_members_group_priority_idx
    ON llm_model_group_members (group_id, priority);

-- +goose Down
DROP INDEX IF EXISTS llm_model_group_members_group_priority_idx;
DROP TABLE IF EXISTS llm_model_group_members;
DROP TABLE IF EXISTS llm_model_groups;
