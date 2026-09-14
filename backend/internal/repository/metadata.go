package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"surajdrive/backend/internal/auth"
)

var ErrInvalidSession = auth.ErrInvalidSession
var ErrAccountUnavailable = errors.New("account is unavailable")

type GoogleAccount struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
	StorageBucket string
}

type Metadata struct {
	pool *pgxpool.Pool
}

func NewMetadata(pool *pgxpool.Pool) *Metadata {
	return &Metadata{pool: pool}
}

func (m *Metadata) ProvisionGoogleAccount(ctx context.Context, account GoogleAccount) (*auth.Principal, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin account provisioning: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID int64
	err = tx.QueryRow(ctx, `
		SELECT user_id
		FROM drive.oauth_identity
		WHERE provider = 'google' AND provider_subject = $1
		FOR UPDATE
	`, account.Subject).Scan(&userID)
	switch {
	case err == nil:
		updated, err := tx.Exec(ctx, `
			UPDATE drive.user_account
			SET primary_email = $2, display_name = $3, picture_url = NULLIF($4, ''),
				last_login_at = now()
			WHERE id = $1 AND status = 'active'
		`, userID, account.Email, account.Name, account.Picture)
		if err != nil {
			return nil, fmt.Errorf("update user account: %w", err)
		}
		if updated.RowsAffected() != 1 {
			return nil, ErrAccountUnavailable
		}
		if _, err := tx.Exec(ctx, `
			UPDATE drive.oauth_identity
			SET provider_email = $2, email_verified = $3
			WHERE provider = 'google' AND provider_subject = $1
		`, account.Subject, account.Email, account.EmailVerified); err != nil {
			return nil, fmt.Errorf("update Google identity: %w", err)
		}
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `
			INSERT INTO drive.user_account (
				primary_email, display_name, picture_url, last_login_at
			)
			VALUES ($1, $2, NULLIF($3, ''), now())
			ON CONFLICT (lower(primary_email)) DO UPDATE
			SET display_name = EXCLUDED.display_name,
				picture_url = EXCLUDED.picture_url,
				last_login_at = now()
			WHERE user_account.status = 'active'
			RETURNING id
		`, account.Email, account.Name, account.Picture).Scan(&userID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrAccountUnavailable
			}
			return nil, fmt.Errorf("create user account: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO drive.oauth_identity (
				user_id, provider, provider_subject, provider_email, email_verified
			)
			VALUES ($1, 'google', $2, $3, $4)
			ON CONFLICT (provider, provider_subject) DO UPDATE
			SET provider_email = EXCLUDED.provider_email,
				email_verified = EXCLUDED.email_verified
			RETURNING user_id
		`, userID, account.Subject, account.Email, account.EmailVerified).Scan(&userID); err != nil {
			return nil, fmt.Errorf("create Google identity: %w", err)
		}
	default:
		return nil, fmt.Errorf("find Google identity: %w", err)
	}

	var principal auth.Principal
	var driveInternalID int64
	if err := tx.QueryRow(ctx, `
		SELECT public_id::text, primary_email, display_name, COALESCE(picture_url, '')
		FROM drive.user_account
		WHERE id = $1
	`, userID).Scan(&principal.UserID, &principal.Email, &principal.Name, &principal.Picture); err != nil {
		return nil, fmt.Errorf("load provisioned user: %w", err)
	}

	if err := tx.QueryRow(ctx, `
		INSERT INTO drive.drive_space (kind, name, owner_user_id, storage_bucket)
		VALUES ('personal', 'My Drive', $1, $2)
		ON CONFLICT (owner_user_id) WHERE kind = 'personal' AND status = 'active'
		DO UPDATE SET name = drive.drive_space.name
		RETURNING id, public_id::text, storage_bucket
	`, userID, account.StorageBucket).Scan(&driveInternalID, &principal.DriveID, &principal.StorageBucket); err != nil {
		return nil, fmt.Errorf("provision personal drive: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.drive_member (drive_id, user_id, role, created_by_user_id)
		VALUES ($1, $2, 'owner', $2)
		ON CONFLICT (drive_id, user_id) DO UPDATE SET role = 'owner'
	`, driveInternalID, userID); err != nil {
		return nil, fmt.Errorf("provision drive membership: %w", err)
	}
	principal.DriveRole = "owner"

	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
		VALUES ($1, NULL, 'folder', 'My Drive', $2)
		ON CONFLICT (drive_id) WHERE parent_id IS NULL DO NOTHING
	`, driveInternalID, userID); err != nil {
		return nil, fmt.Errorf("provision drive root: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE drive.share_invitation
		SET status = 'expired', updated_at = now()
		WHERE status = 'pending' AND expires_at <= now()
	`); err != nil {
		return nil, fmt.Errorf("expire stale share invitations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH pending AS (
			SELECT invitation.id, invitation.item_id, invitation.role, invitation.invited_by_user_id
			FROM drive.share_invitation invitation
			WHERE lower(invitation.invited_email) = lower($2) AND invitation.status = 'pending'
				AND invitation.expires_at > now()
			FOR UPDATE
		), permissions AS (
			INSERT INTO drive.item_permission (item_id, grantee_user_id, role, created_by_user_id)
			SELECT pending.item_id, $1, pending.role, pending.invited_by_user_id FROM pending
			ON CONFLICT (item_id, grantee_user_id) DO UPDATE
			SET role = EXCLUDED.role, created_by_user_id = EXCLUDED.created_by_user_id
			RETURNING item_id
		), accepted AS (
			UPDATE drive.share_invitation invitation
			SET status = 'accepted', accepted_by_user_id = $1, accepted_at = now(), updated_at = now()
			WHERE invitation.id IN (SELECT id FROM pending)
			RETURNING invitation.item_id, invitation.invited_by_user_id
		)
		INSERT INTO drive.notification_outbox (recipient_user_id, notification_type, payload)
		SELECT accepted.invited_by_user_id, 'share.accepted',
			jsonb_build_object('item_id', item.public_id::text, 'accepted_by_user_id', $3::text)
		FROM accepted JOIN drive.item item ON item.id = accepted.item_id
	`, userID, account.Email, principal.UserID); err != nil {
		return nil, fmt.Errorf("accept pending share invitations: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit account provisioning: %w", err)
	}
	return &principal, nil
}

func (m *Metadata) CreateSession(ctx context.Context, userPublicID, userAgent, ipAddress string, ttl time.Duration) (string, time.Time, error) {
	sessionToken, tokenHash, err := newSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(ttl)

	commandTag, err := m.pool.Exec(ctx, `
		INSERT INTO drive.auth_session (user_id, token_hash, user_agent, ip_address, expires_at)
		SELECT id, $2, NULLIF($3, ''), NULLIF($4, '')::inet, $5
		FROM drive.user_account
		WHERE public_id = $1::uuid AND status = 'active'
	`, userPublicID, tokenHash, userAgent, ipAddress, expiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create auth session: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return "", time.Time{}, fmt.Errorf("create auth session: user not found")
	}
	return sessionToken, expiresAt, nil
}

func (m *Metadata) AuthenticateSession(ctx context.Context, userPublicID, sessionToken string) (*auth.Principal, error) {
	if userPublicID == "" || sessionToken == "" {
		return nil, ErrInvalidSession
	}
	tokenHash := sha256.Sum256([]byte(sessionToken))

	var principal auth.Principal
	err := m.pool.QueryRow(ctx, `
		SELECT u.public_id::text, u.primary_email, u.display_name,
			COALESCE(u.picture_url, ''), d.public_id::text, dm.role, d.storage_bucket
		FROM drive.auth_session s
		JOIN drive.user_account u ON u.id = s.user_id
		JOIN drive.drive_space d ON d.owner_user_id = u.id
			AND d.kind = 'personal' AND d.status = 'active'
		JOIN drive.drive_member dm ON dm.drive_id = d.id AND dm.user_id = u.id
		WHERE u.public_id = $1::uuid
			AND s.token_hash = $2
			AND s.revoked_at IS NULL
			AND s.expires_at > now()
			AND u.status = 'active'
	`, userPublicID, tokenHash[:]).Scan(
		&principal.UserID,
		&principal.Email,
		&principal.Name,
		&principal.Picture,
		&principal.DriveID,
		&principal.DriveRole,
		&principal.StorageBucket,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidSession
	}
	if err != nil {
		return nil, fmt.Errorf("authenticate session: %w", err)
	}

	_, _ = m.pool.Exec(ctx, `
		UPDATE drive.auth_session
		SET last_seen_at = now()
		WHERE token_hash = $1 AND last_seen_at < now() - interval '5 minutes'
	`, tokenHash[:])

	return &principal, nil
}

func (m *Metadata) RevokeSession(ctx context.Context, userPublicID, sessionToken string) error {
	if userPublicID == "" || sessionToken == "" {
		return nil
	}
	tokenHash := sha256.Sum256([]byte(sessionToken))
	if _, err := m.pool.Exec(ctx, `
		UPDATE drive.auth_session s
		SET revoked_at = COALESCE(revoked_at, now())
		FROM drive.user_account u
		WHERE s.user_id = u.id AND u.public_id = $1::uuid AND s.token_hash = $2
	`, userPublicID, tokenHash[:]); err != nil {
		return fmt.Errorf("revoke auth session: %w", err)
	}
	return nil
}

func newSessionToken() (string, []byte, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}
