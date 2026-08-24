-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE SCHEMA IF NOT EXISTS drive;
SET search_path = drive, public;

-- +goose StatementBegin
CREATE FUNCTION drive.set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TABLE drive.user_account (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    primary_email TEXT NOT NULL,
    display_name TEXT NOT NULL,
    picture_url TEXT,
    status TEXT NOT NULL DEFAULT 'active',
    storage_quota_bytes BIGINT NOT NULL DEFAULT 16106127360,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT user_account_public_id_unique UNIQUE (public_id),
    CONSTRAINT user_account_email_nonempty_check CHECK (btrim(primary_email) <> ''),
    CONSTRAINT user_account_display_name_nonempty_check CHECK (btrim(display_name) <> ''),
    CONSTRAINT user_account_status_check CHECK (status IN ('active', 'suspended', 'deleted')),
    CONSTRAINT user_account_storage_quota_check CHECK (storage_quota_bytes >= 0)
);

CREATE UNIQUE INDEX user_account_primary_email_unique
    ON drive.user_account (lower(primary_email));

CREATE TABLE drive.oauth_identity (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    provider_subject TEXT NOT NULL,
    provider_email TEXT NOT NULL,
    email_verified BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT oauth_identity_provider_check CHECK (provider IN ('google')),
    CONSTRAINT oauth_identity_provider_subject_nonempty_check CHECK (btrim(provider_subject) <> ''),
    CONSTRAINT oauth_identity_provider_subject_unique UNIQUE (provider, provider_subject),
    CONSTRAINT oauth_identity_user_provider_unique UNIQUE (user_id, provider)
);

CREATE INDEX oauth_identity_user_id_idx ON drive.oauth_identity (user_id);

CREATE TABLE drive.auth_session (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL,
    user_agent TEXT,
    ip_address INET,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT auth_session_public_id_unique UNIQUE (public_id),
    CONSTRAINT auth_session_token_hash_unique UNIQUE (token_hash),
    CONSTRAINT auth_session_token_hash_length_check CHECK (octet_length(token_hash) = 32),
    CONSTRAINT auth_session_expiry_check CHECK (expires_at > created_at)
);

CREATE INDEX auth_session_user_id_idx ON drive.auth_session (user_id);
CREATE INDEX auth_session_active_idx
    ON drive.auth_session (user_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE drive.drive_space (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    owner_user_id BIGINT REFERENCES drive.user_account(id) ON DELETE RESTRICT,
    storage_bucket TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT drive_space_public_id_unique UNIQUE (public_id),
    CONSTRAINT drive_space_kind_check CHECK (kind IN ('personal', 'shared')),
    CONSTRAINT drive_space_name_nonempty_check CHECK (btrim(name) <> ''),
    CONSTRAINT drive_space_bucket_nonempty_check CHECK (btrim(storage_bucket) <> ''),
    CONSTRAINT drive_space_status_check CHECK (status IN ('active', 'suspended', 'deleting')),
    CONSTRAINT drive_space_personal_owner_check CHECK (kind <> 'personal' OR owner_user_id IS NOT NULL)
);

CREATE INDEX drive_space_owner_user_id_idx ON drive.drive_space (owner_user_id);
CREATE UNIQUE INDEX drive_space_active_personal_owner_unique
    ON drive.drive_space (owner_user_id)
    WHERE kind = 'personal' AND status = 'active';

CREATE TABLE drive.drive_member (
    drive_id BIGINT NOT NULL REFERENCES drive.drive_space(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    created_by_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (drive_id, user_id),
    CONSTRAINT drive_member_role_check CHECK (role IN ('viewer', 'commenter', 'editor', 'manager', 'owner'))
);

CREATE INDEX drive_member_user_id_idx ON drive.drive_member (user_id, drive_id);
CREATE INDEX drive_member_created_by_user_id_idx ON drive.drive_member (created_by_user_id);

CREATE TABLE drive.item (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    drive_id BIGINT NOT NULL REFERENCES drive.drive_space(id) ON DELETE CASCADE,
    parent_id BIGINT,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    owner_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE RESTRICT,
    shortcut_target_item_id BIGINT REFERENCES drive.item(id) ON DELETE RESTRICT,
    description TEXT,
    folder_color TEXT,
    trashed_at TIMESTAMPTZ,
    trashed_by_user_id BIGINT REFERENCES drive.user_account(id) ON DELETE SET NULL,
    purge_after TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT item_public_id_unique UNIQUE (public_id),
    CONSTRAINT item_drive_id_id_unique UNIQUE (drive_id, id),
    CONSTRAINT item_kind_check CHECK (kind IN ('folder', 'file', 'shortcut')),
    CONSTRAINT item_name_nonempty_check CHECK (btrim(name) <> ''),
    CONSTRAINT item_name_length_check CHECK (length(name) <= 1024),
    CONSTRAINT item_name_segment_check CHECK (
        name NOT IN ('.', '..')
        AND strpos(name, '/') = 0
        AND strpos(name, chr(92)) = 0
        AND name !~ '[[:cntrl:]]'
    ),
    CONSTRAINT item_kind_target_check CHECK (
        (kind = 'shortcut' AND shortcut_target_item_id IS NOT NULL)
        OR (kind <> 'shortcut' AND shortcut_target_item_id IS NULL)
    ),
    CONSTRAINT item_trash_fields_check CHECK (
        (trashed_at IS NULL AND trashed_by_user_id IS NULL AND purge_after IS NULL)
        OR (trashed_at IS NOT NULL AND purge_after IS NOT NULL)
    ),
    CONSTRAINT item_purge_after_check CHECK (purge_after IS NULL OR purge_after >= trashed_at),
    CONSTRAINT item_parent_fk FOREIGN KEY (drive_id, parent_id)
        REFERENCES drive.item(drive_id, id) ON DELETE RESTRICT DEFERRABLE INITIALLY IMMEDIATE
);

CREATE UNIQUE INDEX item_drive_root_unique ON drive.item (drive_id) WHERE parent_id IS NULL;
CREATE INDEX item_parent_id_idx ON drive.item (parent_id);
CREATE INDEX item_owner_user_id_idx ON drive.item (owner_user_id);
CREATE INDEX item_shortcut_target_item_id_idx ON drive.item (shortcut_target_item_id);
CREATE INDEX item_trashed_by_user_id_idx ON drive.item (trashed_by_user_id);
CREATE INDEX item_folder_listing_idx
    ON drive.item (drive_id, parent_id, kind, name, id)
    WHERE trashed_at IS NULL;
CREATE INDEX item_trash_idx
    ON drive.item (drive_id, trashed_at DESC, id DESC)
    WHERE trashed_at IS NOT NULL;
CREATE INDEX item_name_trgm_idx ON drive.item USING GIN (name gin_trgm_ops);

-- +goose StatementBegin
CREATE FUNCTION drive.validate_item_relationships()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    parent_kind TEXT;
    target_kind TEXT;
BEGIN
    IF NEW.parent_id IS NOT NULL THEN
        SELECT kind INTO parent_kind
        FROM drive.item
        WHERE id = NEW.parent_id AND drive_id = NEW.drive_id;

        IF parent_kind IS DISTINCT FROM 'folder' THEN
            RAISE EXCEPTION 'item parent must be a folder in the same drive';
        END IF;

        IF TG_OP = 'UPDATE' AND NEW.parent_id IS DISTINCT FROM OLD.parent_id THEN
            IF EXISTS (
                WITH RECURSIVE descendants AS (
                    SELECT id FROM drive.item WHERE parent_id = NEW.id
                    UNION ALL
                    SELECT child.id
                    FROM drive.item child
                    JOIN descendants d ON child.parent_id = d.id
                )
                SELECT 1 FROM descendants WHERE id = NEW.parent_id
            ) THEN
                RAISE EXCEPTION 'item move would create a hierarchy cycle';
            END IF;
        END IF;
    ELSIF NEW.kind <> 'folder' THEN
        RAISE EXCEPTION 'drive root must be a folder';
    END IF;

    IF NEW.shortcut_target_item_id IS NOT NULL THEN
        SELECT kind INTO target_kind FROM drive.item WHERE id = NEW.shortcut_target_item_id;
        IF target_kind IS NULL OR target_kind = 'shortcut' THEN
            RAISE EXCEPTION 'shortcut target must be an existing non-shortcut item';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER item_relationships_trigger
BEFORE INSERT OR UPDATE OF drive_id, parent_id, kind, shortcut_target_item_id
ON drive.item
FOR EACH ROW EXECUTE FUNCTION drive.validate_item_relationships();

CREATE TABLE drive.file_version (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    item_id BIGINT NOT NULL REFERENCES drive.item(id) ON DELETE CASCADE,
    version_number BIGINT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    storage_bucket TEXT NOT NULL,
    storage_key TEXT NOT NULL,
    storage_etag TEXT,
    size_bytes BIGINT,
    mime_type TEXT NOT NULL,
    sha256 BYTEA,
    source_modified_at TIMESTAMPTZ,
    created_by_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE RESTRICT,
    keep_forever BOOLEAN NOT NULL DEFAULT false,
    is_current BOOLEAN NOT NULL DEFAULT false,
    legacy_object BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at TIMESTAMPTZ,
    CONSTRAINT file_version_public_id_unique UNIQUE (public_id),
    CONSTRAINT file_version_item_number_unique UNIQUE (item_id, version_number),
    CONSTRAINT file_version_storage_location_unique UNIQUE (storage_bucket, storage_key),
    CONSTRAINT file_version_number_check CHECK (version_number > 0),
    CONSTRAINT file_version_state_check CHECK (state IN ('pending', 'ready', 'failed', 'quarantined', 'deleted')),
    CONSTRAINT file_version_storage_bucket_nonempty_check CHECK (btrim(storage_bucket) <> ''),
    CONSTRAINT file_version_storage_key_nonempty_check CHECK (btrim(storage_key) <> ''),
    CONSTRAINT file_version_mime_type_nonempty_check CHECK (btrim(mime_type) <> ''),
    CONSTRAINT file_version_size_check CHECK (size_bytes IS NULL OR size_bytes >= 0),
    CONSTRAINT file_version_sha256_length_check CHECK (sha256 IS NULL OR octet_length(sha256) = 32),
    CONSTRAINT file_version_current_ready_check CHECK (NOT is_current OR state = 'ready'),
    CONSTRAINT file_version_ready_fields_check CHECK (
        state <> 'ready'
        OR (storage_etag IS NOT NULL AND size_bytes IS NOT NULL AND ready_at IS NOT NULL)
    )
);

CREATE INDEX file_version_item_id_idx ON drive.file_version (item_id);
CREATE INDEX file_version_created_by_user_id_idx ON drive.file_version (created_by_user_id);
CREATE INDEX file_version_history_idx ON drive.file_version (item_id, version_number DESC);
CREATE UNIQUE INDEX file_version_current_unique
    ON drive.file_version (item_id)
    WHERE is_current AND state = 'ready';

-- +goose StatementBegin
CREATE FUNCTION drive.validate_file_version_item()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM drive.item WHERE id = NEW.item_id AND kind = 'file') THEN
        RAISE EXCEPTION 'file version must belong to a file item';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER file_version_item_trigger
BEFORE INSERT OR UPDATE OF item_id ON drive.file_version
FOR EACH ROW EXECUTE FUNCTION drive.validate_file_version_item();

CREATE TABLE drive.upload_session (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    drive_id BIGINT NOT NULL REFERENCES drive.drive_space(id) ON DELETE CASCADE,
    parent_item_id BIGINT NOT NULL REFERENCES drive.item(id) ON DELETE RESTRICT,
    item_id BIGINT REFERENCES drive.item(id) ON DELETE CASCADE,
    file_version_id BIGINT REFERENCES drive.file_version(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    requested_name TEXT NOT NULL,
    expected_size_bytes BIGINT NOT NULL,
    expected_sha256 BYTEA,
    mime_type TEXT NOT NULL,
    minio_upload_id TEXT,
    status TEXT NOT NULL DEFAULT 'initiated',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT upload_session_public_id_unique UNIQUE (public_id),
    CONSTRAINT upload_session_user_idempotency_unique UNIQUE (user_id, idempotency_key),
    CONSTRAINT upload_session_name_nonempty_check CHECK (btrim(requested_name) <> ''),
    CONSTRAINT upload_session_size_check CHECK (expected_size_bytes >= 0),
    CONSTRAINT upload_session_sha256_length_check CHECK (expected_sha256 IS NULL OR octet_length(expected_sha256) = 32),
    CONSTRAINT upload_session_mime_type_nonempty_check CHECK (btrim(mime_type) <> ''),
    CONSTRAINT upload_session_status_check CHECK (status IN ('initiated', 'uploading', 'completing', 'completed', 'aborted', 'expired', 'failed')),
    CONSTRAINT upload_session_expiry_check CHECK (expires_at > created_at)
);

CREATE INDEX upload_session_drive_id_idx ON drive.upload_session (drive_id);
CREATE INDEX upload_session_parent_item_id_idx ON drive.upload_session (parent_item_id);
CREATE INDEX upload_session_item_id_idx ON drive.upload_session (item_id);
CREATE INDEX upload_session_file_version_id_idx ON drive.upload_session (file_version_id);
CREATE INDEX upload_session_active_idx ON drive.upload_session (user_id, status, expires_at);

-- +goose StatementBegin
CREATE FUNCTION drive.validate_upload_session_relationships()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM drive.item
        WHERE id = NEW.parent_item_id AND drive_id = NEW.drive_id AND kind = 'folder'
    ) THEN
        RAISE EXCEPTION 'upload parent must be a folder in the selected drive';
    END IF;

    IF NEW.item_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM drive.item
        WHERE id = NEW.item_id AND drive_id = NEW.drive_id AND kind = 'file'
    ) THEN
        RAISE EXCEPTION 'upload item must be a file in the selected drive';
    END IF;

    IF NEW.file_version_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM drive.file_version
        WHERE id = NEW.file_version_id AND item_id = NEW.item_id
    ) THEN
        RAISE EXCEPTION 'upload version must belong to the reserved item';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER upload_session_relationships_trigger
BEFORE INSERT OR UPDATE OF drive_id, parent_item_id, item_id, file_version_id
ON drive.upload_session
FOR EACH ROW EXECUTE FUNCTION drive.validate_upload_session_relationships();

CREATE TABLE drive.item_permission (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id BIGINT NOT NULL REFERENCES drive.item(id) ON DELETE CASCADE,
    grantee_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    expires_at TIMESTAMPTZ,
    created_by_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT item_permission_item_grantee_unique UNIQUE (item_id, grantee_user_id),
    CONSTRAINT item_permission_role_check CHECK (role IN ('viewer', 'commenter', 'editor'))
);

CREATE INDEX item_permission_grantee_idx ON drive.item_permission (grantee_user_id, item_id);
CREATE INDEX item_permission_created_by_user_id_idx ON drive.item_permission (created_by_user_id);

CREATE TABLE drive.share_link (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    item_id BIGINT NOT NULL REFERENCES drive.item(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL,
    role TEXT NOT NULL,
    allow_download BOOLEAN NOT NULL DEFAULT true,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_by_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT share_link_public_id_unique UNIQUE (public_id),
    CONSTRAINT share_link_token_hash_unique UNIQUE (token_hash),
    CONSTRAINT share_link_token_hash_length_check CHECK (octet_length(token_hash) = 32),
    CONSTRAINT share_link_role_check CHECK (role IN ('viewer', 'commenter'))
);

CREATE INDEX share_link_item_id_idx ON drive.share_link (item_id);
CREATE INDEX share_link_created_by_user_id_idx ON drive.share_link (created_by_user_id);
CREATE INDEX share_link_active_token_idx ON drive.share_link (token_hash) WHERE revoked_at IS NULL;

CREATE TABLE drive.user_item_state (
    user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    item_id BIGINT NOT NULL REFERENCES drive.item(id) ON DELETE CASCADE,
    starred_at TIMESTAMPTZ,
    last_opened_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, item_id)
);

CREATE INDEX user_item_state_item_id_idx ON drive.user_item_state (item_id);
CREATE INDEX user_item_state_starred_idx
    ON drive.user_item_state (user_id, starred_at DESC, item_id)
    WHERE starred_at IS NOT NULL;
CREATE INDEX user_item_state_recent_idx
    ON drive.user_item_state (user_id, last_opened_at DESC, item_id)
    WHERE last_opened_at IS NOT NULL;

CREATE TABLE drive.comment (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    item_id BIGINT NOT NULL REFERENCES drive.item(id) ON DELETE CASCADE,
    parent_comment_id BIGINT,
    author_user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE RESTRICT,
    body TEXT NOT NULL,
    resolved_at TIMESTAMPTZ,
    resolved_by_user_id BIGINT REFERENCES drive.user_account(id) ON DELETE SET NULL,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT comment_public_id_unique UNIQUE (public_id),
    CONSTRAINT comment_item_id_id_unique UNIQUE (item_id, id),
    CONSTRAINT comment_body_nonempty_check CHECK (btrim(body) <> ''),
    CONSTRAINT comment_parent_fk FOREIGN KEY (item_id, parent_comment_id)
        REFERENCES drive.comment(item_id, id) ON DELETE CASCADE
);

CREATE INDEX comment_parent_comment_id_idx ON drive.comment (parent_comment_id);
CREATE INDEX comment_author_user_id_idx ON drive.comment (author_user_id);
CREATE INDEX comment_resolved_by_user_id_idx ON drive.comment (resolved_by_user_id);
CREATE INDEX comment_item_created_idx ON drive.comment (item_id, created_at, id);

CREATE TABLE drive.activity_event (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    drive_id BIGINT REFERENCES drive.drive_space(id) ON DELETE SET NULL,
    item_id BIGINT REFERENCES drive.item(id) ON DELETE SET NULL,
    file_version_id BIGINT REFERENCES drive.file_version(id) ON DELETE SET NULL,
    actor_user_id BIGINT REFERENCES drive.user_account(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_id TEXT,
    ip_address INET,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT activity_event_public_id_unique UNIQUE (public_id),
    CONSTRAINT activity_event_type_nonempty_check CHECK (btrim(event_type) <> ''),
    CONSTRAINT activity_event_details_object_check CHECK (jsonb_typeof(details) = 'object')
);

CREATE INDEX activity_event_drive_id_idx ON drive.activity_event (drive_id);
CREATE INDEX activity_event_item_id_idx ON drive.activity_event (item_id);
CREATE INDEX activity_event_file_version_id_idx ON drive.activity_event (file_version_id);
CREATE INDEX activity_event_actor_user_id_idx ON drive.activity_event (actor_user_id);
CREATE INDEX activity_event_drive_created_idx ON drive.activity_event (drive_id, created_at DESC, id DESC);
CREATE INDEX activity_event_item_created_idx ON drive.activity_event (item_id, created_at DESC, id DESC);

CREATE TABLE drive.legacy_import_record (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    drive_id BIGINT NOT NULL REFERENCES drive.drive_space(id) ON DELETE CASCADE,
    legacy_bucket TEXT NOT NULL,
    legacy_key TEXT NOT NULL,
    legacy_etag TEXT NOT NULL,
    item_id BIGINT REFERENCES drive.item(id) ON DELETE SET NULL,
    file_version_id BIGINT REFERENCES drive.file_version(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT legacy_import_object_unique UNIQUE (legacy_bucket, legacy_key, legacy_etag),
    CONSTRAINT legacy_import_status_check CHECK (status IN ('pending', 'imported', 'skipped', 'failed')),
    CONSTRAINT legacy_import_bucket_nonempty_check CHECK (btrim(legacy_bucket) <> ''),
    CONSTRAINT legacy_import_key_nonempty_check CHECK (btrim(legacy_key) <> '')
);

CREATE INDEX legacy_import_drive_id_idx ON drive.legacy_import_record (drive_id);
CREATE INDEX legacy_import_item_id_idx ON drive.legacy_import_record (item_id);
CREATE INDEX legacy_import_file_version_id_idx ON drive.legacy_import_record (file_version_id);
CREATE INDEX legacy_import_drive_status_idx ON drive.legacy_import_record (drive_id, status);

CREATE TRIGGER user_account_updated_at_trigger
BEFORE UPDATE ON drive.user_account
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER oauth_identity_updated_at_trigger
BEFORE UPDATE ON drive.oauth_identity
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER drive_space_updated_at_trigger
BEFORE UPDATE ON drive.drive_space
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER drive_member_updated_at_trigger
BEFORE UPDATE ON drive.drive_member
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER item_updated_at_trigger
BEFORE UPDATE ON drive.item
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER upload_session_updated_at_trigger
BEFORE UPDATE ON drive.upload_session
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER item_permission_updated_at_trigger
BEFORE UPDATE ON drive.item_permission
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER user_item_state_updated_at_trigger
BEFORE UPDATE ON drive.user_item_state
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER comment_updated_at_trigger
BEFORE UPDATE ON drive.comment
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();
CREATE TRIGGER legacy_import_record_updated_at_trigger
BEFORE UPDATE ON drive.legacy_import_record
FOR EACH ROW EXECUTE FUNCTION drive.set_updated_at();

-- +goose Down
DROP SCHEMA IF EXISTS drive CASCADE;
