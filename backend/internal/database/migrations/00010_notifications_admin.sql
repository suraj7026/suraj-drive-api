-- +goose Up
SET search_path = drive, public;

ALTER TABLE drive.item_permission
    ADD COLUMN public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD CONSTRAINT item_permission_public_id_unique UNIQUE (public_id);

CREATE TABLE drive.notification_outbox (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    recipient_user_id BIGINT REFERENCES drive.user_account(id) ON DELETE CASCADE,
    recipient_email TEXT,
    notification_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    run_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT notification_outbox_public_id_unique UNIQUE (public_id),
    CONSTRAINT notification_outbox_recipient_check CHECK (recipient_user_id IS NOT NULL OR btrim(recipient_email) <> ''),
    CONSTRAINT notification_outbox_type_check CHECK (btrim(notification_type) <> ''),
    CONSTRAINT notification_outbox_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT notification_outbox_status_check CHECK (status IN ('queued', 'processing', 'delivered', 'failed')),
    CONSTRAINT notification_outbox_attempts_check CHECK (attempts >= 0)
);

CREATE INDEX notification_outbox_claim_idx
    ON drive.notification_outbox (run_after, id)
    WHERE status = 'queued';
CREATE INDEX notification_outbox_recipient_idx
    ON drive.notification_outbox (recipient_user_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS drive.notification_outbox_recipient_idx;
DROP INDEX IF EXISTS drive.notification_outbox_claim_idx;
DROP TABLE IF EXISTS drive.notification_outbox;
ALTER TABLE drive.item_permission DROP CONSTRAINT IF EXISTS item_permission_public_id_unique;
ALTER TABLE drive.item_permission DROP COLUMN IF EXISTS public_id;
