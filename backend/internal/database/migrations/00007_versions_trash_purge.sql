-- +goose Up
CREATE TABLE drive.blob_deletion_job (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    file_version_id BIGINT NOT NULL REFERENCES drive.file_version(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 10,
    run_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT blob_deletion_job_public_id_unique UNIQUE (public_id),
    CONSTRAINT blob_deletion_job_version_unique UNIQUE (file_version_id),
    CONSTRAINT blob_deletion_job_status_check CHECK (status IN ('queued', 'processing', 'succeeded', 'failed')),
    CONSTRAINT blob_deletion_job_attempts_check CHECK (attempts >= 0 AND max_attempts BETWEEN 1 AND 100),
    CONSTRAINT blob_deletion_job_deleted_check CHECK ((status = 'succeeded') = (deleted_at IS NOT NULL))
);

CREATE INDEX blob_deletion_job_claim_idx
    ON drive.blob_deletion_job (run_after, id)
    WHERE status = 'queued';
CREATE INDEX blob_deletion_job_lease_idx
    ON drive.blob_deletion_job (lease_expires_at, id)
    WHERE status = 'processing';

-- +goose Down
DROP TABLE IF EXISTS drive.blob_deletion_job;
