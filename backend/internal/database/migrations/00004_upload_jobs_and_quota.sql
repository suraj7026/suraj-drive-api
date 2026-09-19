-- +goose Up
ALTER TABLE drive.upload_session
    ADD COLUMN reserved_storage_key TEXT,
    ADD COLUMN bytes_received BIGINT,
    ADD COLUMN completed_at TIMESTAMPTZ,
    ADD COLUMN failure_code TEXT,
    ADD COLUMN failure_message TEXT;

UPDATE drive.upload_session session
SET reserved_storage_key = version.storage_key
FROM drive.file_version version
WHERE version.id = session.file_version_id
    AND session.reserved_storage_key IS NULL;

UPDATE drive.upload_session
SET completed_at = updated_at
WHERE status = 'completed' AND completed_at IS NULL;

ALTER TABLE drive.upload_session
    ADD CONSTRAINT upload_session_reserved_key_check
        CHECK (reserved_storage_key IS NULL OR btrim(reserved_storage_key) <> ''),
    ADD CONSTRAINT upload_session_bytes_received_check
        CHECK (bytes_received IS NULL OR bytes_received >= 0),
    ADD CONSTRAINT upload_session_completion_check
        CHECK ((status = 'completed') = (completed_at IS NOT NULL));

CREATE INDEX upload_session_cleanup_idx
    ON drive.upload_session (expires_at, id)
    WHERE status IN ('initiated', 'uploading', 'completing');

-- +goose Down
DROP INDEX IF EXISTS drive.upload_session_cleanup_idx;

ALTER TABLE drive.upload_session
    DROP CONSTRAINT IF EXISTS upload_session_completion_check,
    DROP CONSTRAINT IF EXISTS upload_session_bytes_received_check,
    DROP CONSTRAINT IF EXISTS upload_session_reserved_key_check,
    DROP COLUMN IF EXISTS failure_message,
    DROP COLUMN IF EXISTS failure_code,
    DROP COLUMN IF EXISTS completed_at,
    DROP COLUMN IF EXISTS bytes_received,
    DROP COLUMN IF EXISTS reserved_storage_key;
