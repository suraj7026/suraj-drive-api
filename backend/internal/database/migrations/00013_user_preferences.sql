-- +goose Up
CREATE TABLE drive.user_preference (
    user_id BIGINT PRIMARY KEY REFERENCES drive.user_account(id) ON DELETE CASCADE,
    view_mode TEXT NOT NULL DEFAULT 'list',
    density TEXT NOT NULL DEFAULT 'comfortable',
    sort_key TEXT NOT NULL DEFAULT 'default',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT user_preference_view_mode_check CHECK (view_mode IN ('list', 'grid')),
    CONSTRAINT user_preference_density_check CHECK (density IN ('comfortable', 'compact')),
    CONSTRAINT user_preference_sort_key_check CHECK (
        sort_key IN ('default', 'name-asc', 'name-desc', 'date-newest', 'date-oldest', 'size-largest', 'size-smallest')
    )
);

-- +goose Down
DROP TABLE IF EXISTS drive.user_preference;
