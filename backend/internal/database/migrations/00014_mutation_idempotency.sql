-- +goose Up
CREATE UNIQUE INDEX item_active_sibling_name_unique
    ON drive.item (drive_id, parent_id, lower(name))
    WHERE parent_id IS NOT NULL AND trashed_at IS NULL;

CREATE TABLE drive.mutation_request (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    operation TEXT NOT NULL,
    request_hash BYTEA NOT NULL,
    response JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '7 days'),
    CONSTRAINT mutation_request_user_key_unique UNIQUE (user_id, idempotency_key),
    CONSTRAINT mutation_request_key_length_check CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    CONSTRAINT mutation_request_completion_check CHECK (
        (response IS NULL AND completed_at IS NULL) OR (response IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE INDEX mutation_request_expiry_idx ON drive.mutation_request (expires_at, id);

-- +goose Down
DROP TABLE IF EXISTS drive.mutation_request;
DROP INDEX IF EXISTS drive.item_active_sibling_name_unique;
