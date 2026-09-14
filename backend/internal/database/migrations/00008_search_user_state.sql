-- +goose Up
SET search_path = drive, public;

CREATE TABLE drive.item_search_content (
    item_id BIGINT PRIMARY KEY REFERENCES drive.item(id) ON DELETE CASCADE,
    extracted_text TEXT NOT NULL DEFAULT '',
    extraction_status TEXT NOT NULL DEFAULT 'pending',
    extraction_error TEXT,
    source_version_id BIGINT REFERENCES drive.file_version(id) ON DELETE SET NULL,
    extracted_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT item_search_content_status_check
        CHECK (extraction_status IN ('pending', 'processing', 'ready', 'failed'))
);

CREATE INDEX item_search_content_text_idx
    ON drive.item_search_content USING GIN (to_tsvector('simple', extracted_text));
CREATE INDEX item_search_content_pending_idx
    ON drive.item_search_content (updated_at, item_id)
    WHERE extraction_status IN ('pending', 'failed');

CREATE TABLE drive.user_drive_preference (
    user_id BIGINT NOT NULL REFERENCES drive.user_account(id) ON DELETE CASCADE,
    drive_id BIGINT NOT NULL REFERENCES drive.drive_space(id) ON DELETE CASCADE,
    view_mode TEXT NOT NULL DEFAULT 'list',
    sort_key TEXT NOT NULL DEFAULT 'name',
    sort_direction TEXT NOT NULL DEFAULT 'asc',
    density TEXT NOT NULL DEFAULT 'comfortable',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, drive_id),
    CONSTRAINT user_drive_preference_view_check CHECK (view_mode IN ('list', 'grid')),
    CONSTRAINT user_drive_preference_sort_check CHECK (sort_key IN ('name', 'modified', 'opened', 'size')),
    CONSTRAINT user_drive_preference_direction_check CHECK (sort_direction IN ('asc', 'desc')),
    CONSTRAINT user_drive_preference_density_check CHECK (density IN ('compact', 'comfortable'))
);

CREATE INDEX item_active_updated_idx
    ON drive.item (drive_id, updated_at DESC, id DESC)
    WHERE trashed_at IS NULL AND parent_id IS NOT NULL;
CREATE INDEX file_version_storage_accounting_idx
    ON drive.file_version (item_id, state, is_current) INCLUDE (size_bytes);

-- +goose Down
DROP INDEX IF EXISTS drive.file_version_storage_accounting_idx;
DROP INDEX IF EXISTS drive.item_active_updated_idx;
DROP TABLE IF EXISTS drive.user_drive_preference;
DROP INDEX IF EXISTS drive.item_search_content_pending_idx;
DROP INDEX IF EXISTS drive.item_search_content_text_idx;
DROP TABLE IF EXISTS drive.item_search_content;
