-- +goose Up
CREATE INDEX item_child_name_cursor_idx
    ON drive.item (
        drive_id,
        parent_id,
        (CASE WHEN kind = 'folder' THEN 0 ELSE 1 END),
        (lower(name)),
        id
    )
    WHERE trashed_at IS NULL AND kind IN ('folder', 'file');

-- +goose Down
DROP INDEX IF EXISTS drive.item_child_name_cursor_idx;
