-- +goose Up
CREATE TABLE drive.preview_job (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    file_version_id BIGINT NOT NULL REFERENCES drive.file_version(id) ON DELETE CASCADE,
    profile TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    run_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT preview_job_public_id_unique UNIQUE (public_id),
    CONSTRAINT preview_job_version_profile_unique UNIQUE (file_version_id, profile),
    CONSTRAINT preview_job_profile_nonempty_check CHECK (btrim(profile) <> ''),
    CONSTRAINT preview_job_status_check CHECK (status IN ('queued', 'processing', 'succeeded', 'failed')),
    CONSTRAINT preview_job_attempts_check CHECK (attempts >= 0 AND max_attempts BETWEEN 1 AND 20)
);

CREATE INDEX preview_job_claim_idx
    ON drive.preview_job (run_after, id)
    WHERE status = 'queued';
CREATE INDEX preview_job_lease_idx
    ON drive.preview_job (lease_expires_at, id)
    WHERE status = 'processing';

CREATE TABLE drive.preview_artifact (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    file_version_id BIGINT NOT NULL REFERENCES drive.file_version(id) ON DELETE CASCADE,
    profile TEXT NOT NULL,
    storage_bucket TEXT NOT NULL,
    storage_key TEXT NOT NULL,
    storage_etag TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    mime_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT preview_artifact_public_id_unique UNIQUE (public_id),
    CONSTRAINT preview_artifact_version_profile_unique UNIQUE (file_version_id, profile),
    CONSTRAINT preview_artifact_location_unique UNIQUE (storage_bucket, storage_key),
    CONSTRAINT preview_artifact_profile_nonempty_check CHECK (btrim(profile) <> ''),
    CONSTRAINT preview_artifact_key_nonempty_check CHECK (btrim(storage_bucket) <> '' AND btrim(storage_key) <> ''),
    CONSTRAINT preview_artifact_size_check CHECK (size_bytes >= 0),
    CONSTRAINT preview_artifact_mime_nonempty_check CHECK (btrim(mime_type) <> '')
);

CREATE INDEX preview_artifact_version_idx ON drive.preview_artifact (file_version_id);

-- +goose Down
DROP TABLE IF EXISTS drive.preview_artifact;
DROP TABLE IF EXISTS drive.preview_job;
