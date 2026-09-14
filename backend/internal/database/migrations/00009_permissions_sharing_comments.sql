-- +goose Up
SET search_path = drive, public;

ALTER TABLE drive.item_permission
    ADD COLUMN allow_reshare BOOLEAN NOT NULL DEFAULT false,
    ADD CONSTRAINT item_permission_expiry_check CHECK (expires_at IS NULL OR expires_at > created_at);

CREATE INDEX item_permission_effective_idx
    ON drive.item_permission (grantee_user_id, item_id, expires_at, role);

CREATE TABLE drive.share_invitation (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    item_id BIGINT NOT NULL REFERENCES drive.item(id) ON DELETE CASCADE,
    invited_email TEXT NOT NULL,
    role TEXT NOT NULL,
    token_hash BYTEA NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    invited_by_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE RESTRICT,
    expires_at TIMESTAMPTZ NOT NULL,
    accepted_by_user_id BIGINT REFERENCES drive.user_account(id) ON DELETE SET NULL,
    accepted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT share_invitation_public_id_unique UNIQUE (public_id),
    CONSTRAINT share_invitation_token_hash_unique UNIQUE (token_hash),
    CONSTRAINT share_invitation_token_hash_length_check CHECK (octet_length(token_hash) = 32),
    CONSTRAINT share_invitation_email_check CHECK (btrim(invited_email) <> ''),
    CONSTRAINT share_invitation_role_check CHECK (role IN ('viewer', 'commenter', 'editor')),
    CONSTRAINT share_invitation_status_check CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    CONSTRAINT share_invitation_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT share_invitation_acceptance_check CHECK (
        (status = 'accepted' AND accepted_by_user_id IS NOT NULL AND accepted_at IS NOT NULL)
        OR (status <> 'accepted' AND accepted_at IS NULL)
    )
);

CREATE UNIQUE INDEX share_invitation_pending_unique
    ON drive.share_invitation (item_id, lower(invited_email))
    WHERE status = 'pending';
CREATE INDEX share_invitation_email_idx
    ON drive.share_invitation (lower(invited_email), status, expires_at);

CREATE TABLE drive.access_request (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    item_id BIGINT NOT NULL REFERENCES drive.item(id) ON DELETE CASCADE,
    requester_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    requested_role TEXT NOT NULL DEFAULT 'viewer',
    message TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    decided_by_user_id BIGINT REFERENCES drive.user_account(id) ON DELETE SET NULL,
    decided_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT access_request_public_id_unique UNIQUE (public_id),
    CONSTRAINT access_request_role_check CHECK (requested_role IN ('viewer', 'commenter', 'editor')),
    CONSTRAINT access_request_status_check CHECK (status IN ('pending', 'approved', 'denied', 'cancelled')),
    CONSTRAINT access_request_decision_check CHECK (
        (status IN ('approved', 'denied') AND decided_by_user_id IS NOT NULL AND decided_at IS NOT NULL)
        OR (status IN ('pending', 'cancelled') AND decided_at IS NULL)
    )
);

CREATE UNIQUE INDEX access_request_pending_unique
    ON drive.access_request (item_id, requester_user_id)
    WHERE status = 'pending';

-- +goose Down
DROP INDEX IF EXISTS drive.access_request_pending_unique;
DROP TABLE IF EXISTS drive.access_request;
DROP INDEX IF EXISTS drive.share_invitation_email_idx;
DROP INDEX IF EXISTS drive.share_invitation_pending_unique;
DROP TABLE IF EXISTS drive.share_invitation;
DROP INDEX IF EXISTS drive.item_permission_effective_idx;
ALTER TABLE drive.item_permission
    DROP CONSTRAINT IF EXISTS item_permission_expiry_check,
    DROP COLUMN IF EXISTS allow_reshare;
