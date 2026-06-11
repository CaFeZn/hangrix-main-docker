-- +goose Up
CREATE TABLE repo_silence_state (
    repo_id          BIGINT PRIMARY KEY,
    active           BOOLEAN NOT NULL DEFAULT false,
    source           TEXT NOT NULL DEFAULT '',
    source_ref       TEXT NOT NULL DEFAULT '',
    entered_at       TIMESTAMPTZ,
    expected_exit_at TIMESTAMPTZ,
    reason           TEXT NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE repo_silence_audit (
    id         BIGSERIAL PRIMARY KEY,
    repo_id    BIGINT NOT NULL,
    event      TEXT NOT NULL,
    source     TEXT NOT NULL,
    actor_id   BIGINT,
    session_id BIGINT,
    payload    JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_silence_audit_repo ON repo_silence_audit (repo_id, created_at DESC);

CREATE TABLE repo_silence_overrides (
    session_id BIGINT PRIMARY KEY,
    repo_id    BIGINT NOT NULL,
    granted_by BIGINT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX idx_silence_overrides_repo_active ON repo_silence_overrides (repo_id) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE repo_silence_overrides;
DROP TABLE repo_silence_audit;
DROP TABLE repo_silence_state;
