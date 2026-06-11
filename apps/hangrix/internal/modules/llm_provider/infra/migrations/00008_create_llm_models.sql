-- +goose Up

-- llm_models: one row per model definition. Each model has a 1:1 relationship
-- with a llm_model_groups row (via group_id UNIQUE). The group carries the
-- multi-provider dispatch that was already built for model groups.
CREATE TABLE IF NOT EXISTS llm_models (
    id                   BIGSERIAL    PRIMARY KEY,
    name                 TEXT         NOT NULL UNIQUE,       -- [a-z0-9][a-z0-9._-]{0,63}; global unique vs groups & providers.allowed_models
    display_name         TEXT         NOT NULL DEFAULT '',
    context_window       INTEGER      NOT NULL CHECK (context_window > 0),
    max_output_tokens    INTEGER      NOT NULL CHECK (max_output_tokens > 0),
    vision               BOOLEAN      NOT NULL DEFAULT FALSE,
    reasoning            BOOLEAN      NOT NULL DEFAULT FALSE,
    reasoning_effort_map JSONB        NOT NULL DEFAULT '{}'::jsonb,  -- map[string]string: logical effort → provider native value
    group_id             BIGINT       NOT NULL UNIQUE REFERENCES llm_model_groups(id) ON DELETE RESTRICT,
    actor_id             BIGINT       NOT NULL REFERENCES actors(id) ON DELETE RESTRICT,
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS llm_models;
