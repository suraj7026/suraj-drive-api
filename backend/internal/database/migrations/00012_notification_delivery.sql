-- +goose Up
ALTER TABLE drive.notification_outbox
    ADD COLUMN lease_expires_at TIMESTAMPTZ,
    ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 10,
    ADD CONSTRAINT notification_outbox_max_attempts_check CHECK (max_attempts BETWEEN 1 AND 100);

CREATE INDEX notification_outbox_lease_idx
    ON drive.notification_outbox (lease_expires_at, id)
    WHERE status = 'processing';

-- +goose Down
DROP INDEX IF EXISTS drive.notification_outbox_lease_idx;
ALTER TABLE drive.notification_outbox
    DROP CONSTRAINT IF EXISTS notification_outbox_max_attempts_check,
    DROP COLUMN IF EXISTS max_attempts,
    DROP COLUMN IF EXISTS lease_expires_at;
