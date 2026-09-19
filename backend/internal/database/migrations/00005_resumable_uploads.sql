-- +goose Up
ALTER TABLE drive.upload_session
    ADD COLUMN upload_mode TEXT NOT NULL DEFAULT 'single',
    ADD COLUMN part_size_bytes BIGINT;

ALTER TABLE drive.upload_session
    ADD CONSTRAINT upload_session_mode_check
        CHECK (upload_mode IN ('single', 'multipart')),
    ADD CONSTRAINT upload_session_part_size_check
        CHECK (
            (upload_mode = 'single' AND part_size_bytes IS NULL)
            OR (upload_mode = 'multipart' AND part_size_bytes >= 5242880)
        );

CREATE TABLE drive.upload_part (
    upload_session_id BIGINT NOT NULL REFERENCES drive.upload_session(id) ON DELETE CASCADE,
    part_number INTEGER NOT NULL,
    storage_etag TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    acknowledged_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (upload_session_id, part_number),
    CONSTRAINT upload_part_number_check CHECK (part_number BETWEEN 1 AND 10000),
    CONSTRAINT upload_part_etag_nonempty_check CHECK (btrim(storage_etag) <> ''),
    CONSTRAINT upload_part_size_check CHECK (size_bytes > 0)
);

CREATE INDEX upload_part_session_acknowledged_idx
    ON drive.upload_part (upload_session_id, acknowledged_at DESC);

-- +goose Down
DROP TABLE IF EXISTS drive.upload_part;

ALTER TABLE drive.upload_session
    DROP CONSTRAINT IF EXISTS upload_session_part_size_check,
    DROP CONSTRAINT IF EXISTS upload_session_mode_check,
    DROP COLUMN IF EXISTS part_size_bytes,
    DROP COLUMN IF EXISTS upload_mode;
