-- +goose Up
ALTER TABLE drive.file_version DROP CONSTRAINT file_version_state_check;
ALTER TABLE drive.file_version ADD CONSTRAINT file_version_state_check
    CHECK (state IN ('pending', 'ready', 'failed', 'quarantined', 'deleting', 'deleted'));

-- An earlier worker may already have removed bytes before recording its result.
UPDATE drive.file_version version
SET state = 'deleting', is_current = false
FROM drive.blob_deletion_job job
WHERE job.file_version_id = version.id AND job.attempts > 0
    AND job.status <> 'succeeded' AND version.state <> 'deleted';

-- Expose only the migration version to the application role, not migration DDL.
-- +goose StatementBegin
CREATE FUNCTION drive.schema_version() RETURNS BIGINT
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT COALESCE(max(version_id), 0) FROM (
        SELECT DISTINCT ON (version_id) version_id, is_applied
        FROM public.goose_db_version ORDER BY version_id, id DESC
    ) versions WHERE is_applied;
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION drive.schema_version();
UPDATE drive.file_version SET state = 'failed', is_current = false WHERE state = 'deleting';
ALTER TABLE drive.file_version DROP CONSTRAINT file_version_state_check;
ALTER TABLE drive.file_version ADD CONSTRAINT file_version_state_check
    CHECK (state IN ('pending', 'ready', 'failed', 'quarantined', 'deleted'));
