package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrInvalidPreference = errors.New("invalid user preference")

type UserPreferences struct {
	ViewMode string `json:"view_mode"`
	Density  string `json:"density"`
	SortKey  string `json:"sort_key"`
}

type UpdateUserPreferencesInput struct {
	UserPublicID string
	ViewMode     *string
	Density      *string
	SortKey      *string
}

func (m *Metadata) GetUserPreferences(ctx context.Context, userPublicID string) (UserPreferences, error) {
	preferences := UserPreferences{ViewMode: "list", Density: "comfortable", SortKey: "default"}
	err := m.pool.QueryRow(ctx, `
		SELECT COALESCE(preference.view_mode, 'list'), COALESCE(preference.density, 'comfortable'),
			COALESCE(preference.sort_key, 'default')
		FROM drive.user_account actor
		LEFT JOIN drive.user_preference preference ON preference.user_id = actor.id
		WHERE actor.public_id = $1::uuid AND actor.status = 'active'
	`, userPublicID).Scan(&preferences.ViewMode, &preferences.Density, &preferences.SortKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserPreferences{}, ErrItemNotFound
	}
	if err != nil {
		return UserPreferences{}, fmt.Errorf("get user preferences: %w", err)
	}
	return preferences, nil
}

func (m *Metadata) UpdateUserPreferences(ctx context.Context, input UpdateUserPreferencesInput) (UserPreferences, error) {
	if input.ViewMode == nil && input.Density == nil && input.SortKey == nil {
		return UserPreferences{}, fmt.Errorf("%w: at least one field is required", ErrInvalidPreference)
	}
	if input.ViewMode != nil && *input.ViewMode != "list" && *input.ViewMode != "grid" {
		return UserPreferences{}, fmt.Errorf("%w: unsupported view mode", ErrInvalidPreference)
	}
	if input.Density != nil && *input.Density != "comfortable" && *input.Density != "compact" {
		return UserPreferences{}, fmt.Errorf("%w: unsupported density", ErrInvalidPreference)
	}
	if input.SortKey != nil && !validPreferenceSortKey(*input.SortKey) {
		return UserPreferences{}, fmt.Errorf("%w: unsupported sort key", ErrInvalidPreference)
	}

	var preferences UserPreferences
	err := m.pool.QueryRow(ctx, `
		WITH actor AS (
			SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
		), updated AS (
			INSERT INTO drive.user_preference (user_id, view_mode, density, sort_key)
			SELECT actor.id, COALESCE($2::text, 'list'), COALESCE($3::text, 'comfortable'), COALESCE($4::text, 'default')
			FROM actor
			ON CONFLICT (user_id) DO UPDATE SET
				view_mode = COALESCE($2::text, drive.user_preference.view_mode),
				density = COALESCE($3::text, drive.user_preference.density),
				sort_key = COALESCE($4::text, drive.user_preference.sort_key),
				updated_at = now()
			RETURNING view_mode, density, sort_key
		)
		SELECT view_mode, density, sort_key FROM updated
	`, input.UserPublicID, input.ViewMode, input.Density, input.SortKey).Scan(
		&preferences.ViewMode, &preferences.Density, &preferences.SortKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserPreferences{}, ErrItemNotFound
	}
	if err != nil {
		return UserPreferences{}, fmt.Errorf("update user preferences: %w", err)
	}
	return preferences, nil
}

func validPreferenceSortKey(value string) bool {
	switch value {
	case "default", "name-asc", "name-desc", "date-newest", "date-oldest", "size-largest", "size-smallest":
		return true
	default:
		return false
	}
}
