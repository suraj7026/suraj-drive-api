package repository

import (
	"context"
	"fmt"
	"time"

	pagecursor "surajdrive/backend/internal/pagination"
)

type ActivityEvent struct {
	ID         string    `json:"id"`
	EventType  string    `json:"event_type"`
	ActorName  string    `json:"actor_name"`
	ActorEmail string    `json:"actor_email"`
	CreatedAt  time.Time `json:"created_at"`
}

func (m *Metadata) ListItemActivity(ctx context.Context, userPublicID, itemPublicID string, after *pagecursor.Position, limit int) ([]ActivityEvent, *pagecursor.Position, error) {
	allowed, err := m.hasItemAccess(ctx, userPublicID, itemPublicID)
	if err != nil {
		return nil, nil, err
	}
	if !allowed {
		return nil, nil, ErrItemNotFound
	}
	hasCursor := after != nil
	positionTime, positionID := time.Time{}, int64(0)
	if after != nil {
		positionTime = time.UnixMicro(after.Time)
		positionID = after.ID
	}
	rows, err := m.pool.Query(ctx, `
		SELECT event.id, event.public_id::text, event.event_type,
			COALESCE(actor.display_name, 'System'), COALESCE(actor.primary_email, ''), event.created_at
		FROM drive.activity_event event
		JOIN drive.item item ON item.id = event.item_id AND item.public_id = $1::uuid
		LEFT JOIN drive.user_account actor ON actor.id = event.actor_user_id
		WHERE NOT $2::boolean OR event.created_at < $3
			OR (event.created_at = $3 AND event.id < $4)
		ORDER BY event.created_at DESC, event.id DESC
		LIMIT $5
	`, itemPublicID, hasCursor, positionTime, positionID, limit+1)
	if err != nil {
		return nil, nil, fmt.Errorf("list item activity: %w", err)
	}
	defer rows.Close()
	events := make([]ActivityEvent, 0, limit)
	positions := make([]pagecursor.Position, 0, limit+1)
	for rows.Next() {
		var internalID int64
		var event ActivityEvent
		if err := rows.Scan(&internalID, &event.ID, &event.EventType, &event.ActorName, &event.ActorEmail, &event.CreatedAt); err != nil {
			return nil, nil, fmt.Errorf("scan item activity: %w", err)
		}
		positions = append(positions, pagecursor.Position{ID: internalID, Time: event.CreatedAt.UnixMicro()})
		if len(events) < limit {
			events = append(events, event)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate item activity: %w", err)
	}
	if len(positions) <= limit {
		return events, nil, nil
	}
	next := positions[limit-1]
	return events, &next, nil
}

func (m *Metadata) hasItemAccess(ctx context.Context, userPublicID, itemPublicID string) (bool, error) {
	var allowed bool
	err := m.pool.QueryRow(ctx, `
		WITH RECURSIVE selected_user AS (
			SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
		), target AS (
			SELECT id, parent_id, drive_id FROM drive.item
			WHERE public_id = $2::uuid AND trashed_at IS NULL
		), ancestors AS (
			SELECT id, parent_id FROM target
			UNION ALL
			SELECT parent.id, parent.parent_id
			FROM drive.item parent JOIN ancestors child ON child.parent_id = parent.id
		)
		SELECT EXISTS (
			SELECT 1 FROM target, selected_user actor
			WHERE EXISTS (
				SELECT 1 FROM drive.drive_member member
				WHERE member.drive_id = target.drive_id AND member.user_id = actor.id
			) OR EXISTS (
				SELECT 1 FROM drive.item_permission permission
				WHERE permission.item_id IN (SELECT id FROM ancestors)
					AND permission.grantee_user_id = actor.id
					AND (permission.expires_at IS NULL OR permission.expires_at > now())
			)
		)
	`, userPublicID, itemPublicID).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("authorize item activity: %w", err)
	}
	return allowed, nil
}
