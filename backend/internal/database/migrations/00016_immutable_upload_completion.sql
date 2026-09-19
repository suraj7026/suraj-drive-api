-- +goose Up
SET search_path = drive, public;

CREATE TABLE drive.upload_completion_job (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    upload_session_id BIGINT NOT NULL REFERENCES drive.upload_session(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    run_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_expires_at TIMESTAMPTZ,
    final_storage_key TEXT,
    last_error TEXT,
    completed_at TIMESTAMPTZ,
    staging_cleanup_after TIMESTAMPTZ,
    staging_cleaned_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT upload_completion_job_public_id_unique UNIQUE (public_id),
    CONSTRAINT upload_completion_job_session_unique UNIQUE (upload_session_id),
    CONSTRAINT upload_completion_job_status_check CHECK (status IN ('queued', 'processing', 'succeeded', 'failed')),
    CONSTRAINT upload_completion_job_attempts_check CHECK (attempts >= 0 AND max_attempts > 0),
    CONSTRAINT upload_completion_job_final_key_check CHECK (final_storage_key IS NULL OR btrim(final_storage_key) <> ''),
    CONSTRAINT upload_completion_job_completion_check CHECK ((status = 'succeeded') = (completed_at IS NOT NULL))
);

CREATE INDEX upload_completion_job_claim_idx
    ON drive.upload_completion_job (run_after, id)
    WHERE status = 'queued';

CREATE INDEX upload_completion_job_staging_cleanup_idx
    ON drive.upload_completion_job (staging_cleanup_after, id)
    WHERE status = 'succeeded' AND staging_cleaned_at IS NULL;

-- Existing pending uploads predate staging-key isolation. Fail them closed so
-- an already-issued URL can never become a published immutable version.
UPDATE drive.upload_session session
SET status = 'failed', failure_code = 'upgrade_requires_restart',
    failure_message = 'upload must be restarted after the immutable upload upgrade'
WHERE status IN ('initiated', 'uploading', 'completing');

UPDATE drive.file_version version
SET state = 'failed', is_current = false
WHERE state = 'pending' AND EXISTS (
    SELECT 1 FROM drive.upload_session session
    WHERE session.file_version_id = version.id AND session.failure_code = 'upgrade_requires_restart'
);

INSERT INTO drive.blob_deletion_job (file_version_id)
SELECT version.id
FROM drive.file_version version
JOIN drive.upload_session session ON session.file_version_id = version.id
WHERE session.failure_code = 'upgrade_requires_restart'
ON CONFLICT (file_version_id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS drive.upload_completion_job;
