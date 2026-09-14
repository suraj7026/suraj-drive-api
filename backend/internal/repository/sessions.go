package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type AuthSession struct {
	ID        string    `json:"id"`
	UserAgent string    `json:"user_agent,omitempty"`
	IPAddress string    `json:"ip_address,omitempty"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (m *Metadata) ListAuthSessions(ctx context.Context, userPublicID, currentToken string) ([]AuthSession, error) {
	currentHash := sha256.Sum256([]byte(currentToken))
	rows, err := m.pool.Query(ctx, `
		SELECT session.public_id::text, COALESCE(session.user_agent, ''),
			COALESCE(host(session.ip_address), ''), session.token_hash = $2,
			session.created_at, session.last_seen_at, session.expires_at
		FROM drive.user_account actor
		JOIN drive.auth_session session ON session.user_id = actor.id
		WHERE actor.public_id = $1::uuid AND actor.status = 'active'
			AND session.revoked_at IS NULL AND session.expires_at > now()
		ORDER BY session.token_hash = $2 DESC, session.last_seen_at DESC, session.id DESC
	`, userPublicID, currentHash[:])
	if err != nil {
		return nil, fmt.Errorf("list auth sessions: %w", err)
	}
	defer rows.Close()
	sessions := make([]AuthSession, 0)
	for rows.Next() {
		var session AuthSession
		if err := rows.Scan(&session.ID, &session.UserAgent, &session.IPAddress, &session.Current, &session.CreatedAt, &session.LastSeen, &session.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan auth session: %w", err)
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (m *Metadata) RevokeAuthSessionByID(ctx context.Context, userPublicID, sessionPublicID, currentToken string) (bool, error) {
	currentHash := sha256.Sum256([]byte(currentToken))
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin session revocation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID int64
	var current bool
	if err := tx.QueryRow(ctx, `
		UPDATE drive.auth_session session SET revoked_at = COALESCE(session.revoked_at, now())
		FROM drive.user_account actor
		WHERE actor.public_id = $1::uuid AND session.user_id = actor.id
			AND session.public_id = $2::uuid AND session.revoked_at IS NULL
		RETURNING actor.id, session.token_hash = $3
	`, userPublicID, sessionPublicID, currentHash[:]).Scan(&userID, &current); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrItemNotFound
	} else if err != nil {
		return false, fmt.Errorf("revoke auth session: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, actor_user_id, event_type, details)
		SELECT drive.id, $1, 'auth.session_revoked', jsonb_build_object('session_id', $2::text, 'current', $3::boolean)
		FROM drive.drive_space drive WHERE drive.owner_user_id = $1 AND drive.kind = 'personal' AND drive.status = 'active'
	`, userID, sessionPublicID, current); err != nil {
		return false, fmt.Errorf("record session revocation: %w", err)
	}
	return current, tx.Commit(ctx)
}

func (m *Metadata) RevokeOtherAuthSessions(ctx context.Context, userPublicID, currentToken string) (int64, error) {
	currentHash := sha256.Sum256([]byte(currentToken))
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin other-session revocation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'`, userPublicID).Scan(&userID); err != nil {
		return 0, ErrItemNotFound
	}
	command, err := tx.Exec(ctx, `
		UPDATE drive.auth_session SET revoked_at = now()
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now() AND token_hash <> $2
	`, userID, currentHash[:])
	if err != nil {
		return 0, fmt.Errorf("revoke other auth sessions: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, actor_user_id, event_type, details)
		SELECT drive.id, $1, 'auth.other_sessions_revoked', jsonb_build_object('count', $2::bigint)
		FROM drive.drive_space drive WHERE drive.owner_user_id = $1 AND drive.kind = 'personal' AND drive.status = 'active'
	`, userID, command.RowsAffected()); err != nil {
		return 0, fmt.Errorf("record other-session revocation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}
