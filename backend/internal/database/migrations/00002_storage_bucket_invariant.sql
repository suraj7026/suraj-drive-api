-- +goose Up
CREATE UNIQUE INDEX drive_space_storage_bucket_unique
    ON drive.drive_space (storage_bucket);

-- +goose Down
DROP INDEX IF EXISTS drive.drive_space_storage_bucket_unique;
