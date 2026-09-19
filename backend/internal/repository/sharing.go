package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"surajdrive/backend/internal/model"
	pagecursor "surajdrive/backend/internal/pagination"
)

var (
	ErrPermissionDenied = errors.New("permission denied")
	ErrInviteeNotFound  = errors.New("invitee does not have an account")
	ErrInvalidRole      = errors.New("invalid permission role")
)

type ItemPermission struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type ShareInvitation struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func (m *Metadata) ListSharedWithMeCursor(ctx context.Context, userPublicID string, after *pagecursor.Position, limit int) (model.ListResponse, *pagecursor.Position, error) {
	hasCursor := after != nil
	positionTime, positionID := time.Time{}, int64(0)
	if after != nil {
		positionTime = time.UnixMicro(after.Time)
		positionID = after.ID
	}
	rows, err := m.pool.Query(ctx, `
		SELECT permission.id, item.public_id::text, item.kind, item.name,
			COALESCE(version.storage_key, ''), COALESCE(version.size_bytes, 0),
			COALESCE(version.source_modified_at, version.created_at, item.updated_at),
			COALESCE(version.mime_type, ''), COALESCE(version.storage_etag, ''),
			permission.created_at, state.starred_at, state.last_opened_at, COALESCE(item.folder_color, '')
		FROM drive.user_account actor
		JOIN drive.item_permission permission ON permission.grantee_user_id = actor.id
			AND (permission.expires_at IS NULL OR permission.expires_at > now())
		JOIN drive.item item ON item.id = permission.item_id AND item.trashed_at IS NULL
		JOIN drive.drive_space drive ON drive.id = item.drive_id AND drive.status = 'active'
		LEFT JOIN drive.drive_member own_member ON own_member.drive_id = drive.id AND own_member.user_id = actor.id
		LEFT JOIN drive.file_version version ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		LEFT JOIN drive.user_item_state state ON state.item_id = item.id AND state.user_id = actor.id
		WHERE actor.public_id = $1::uuid AND actor.status = 'active'
			AND own_member.user_id IS NULL
			AND item.kind IN ('folder', 'file', 'shortcut') AND (item.kind IN ('folder', 'shortcut') OR version.id IS NOT NULL)
			AND (NOT $2::boolean OR permission.created_at < $3
				OR (permission.created_at = $3 AND permission.id < $4))
		ORDER BY permission.created_at DESC, permission.id DESC
		LIMIT $5
	`, userPublicID, hasCursor, positionTime, positionID, limit+1)
	if err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("list shared items: %w", err)
	}
	defer rows.Close()
	folders := make([]model.FolderEntry, 0, limit)
	files := make([]model.FileObject, 0, limit)
	shortcuts := make([]model.ShortcutEntry, 0, limit)
	positions := make([]pagecursor.Position, 0, limit+1)
	for rows.Next() {
		var permissionID int64
		var publicID, kind, name string
		var sharedAt time.Time
		var starredAt, openedAt *time.Time
		var file model.FileObject
		var folderColor string
		if err := rows.Scan(
			&permissionID, &publicID, &kind, &name, &file.Key, &file.Size,
			&file.LastModified, &file.ContentType, &file.ETag, &sharedAt, &starredAt, &openedAt, &folderColor,
		); err != nil {
			return model.ListResponse{}, nil, fmt.Errorf("scan shared item: %w", err)
		}
		positions = append(positions, pagecursor.Position{ID: permissionID, Time: sharedAt.UnixMicro()})
		if len(positions) > limit {
			continue
		}
		if kind == "folder" {
			folders = append(folders, model.FolderEntry{ID: publicID, Name: name, StarredAt: starredAt, LastOpenedAt: openedAt, Shared: true, FolderColor: folderColor})
		} else if kind == "shortcut" {
			shortcuts = append(shortcuts, model.ShortcutEntry{ID: publicID, Name: name, StarredAt: starredAt, LastOpenedAt: openedAt, Shared: true})
		} else {
			file.ID, file.Name, file.StarredAt, file.LastOpenedAt = publicID, name, starredAt, openedAt
			file.Shared = true
			files = append(files, file)
		}
	}
	if err := rows.Err(); err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("iterate shared items: %w", err)
	}
	hasMore := len(positions) > limit
	var next *pagecursor.Position
	if hasMore {
		last := positions[limit-1]
		next = &last
	}
	return model.ListResponse{
		Folders: folders, Files: files, Shortcuts: shortcuts,
		Pagination: model.Pagination{Limit: limit, Returned: len(folders) + len(files) + len(shortcuts), HasMore: hasMore},
	}, next, nil
}

func (m *Metadata) ListAccessibleChildrenCursor(ctx context.Context, userPublicID, parentPublicID string, after *pagecursor.Position, limit int) (model.ListResponse, *pagecursor.Position, error) {
	hasCursor := after != nil
	position := pagecursor.Position{}
	if after != nil {
		position = *after
	}
	rows, err := m.pool.Query(ctx, `
		WITH RECURSIVE selected_user AS (
			SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
		), parent AS (
			SELECT id, parent_id, drive_id FROM drive.item
			WHERE public_id = $2::uuid AND kind = 'folder' AND trashed_at IS NULL
		), ancestors AS (
			SELECT id, parent_id FROM parent
			UNION ALL
			SELECT item.id, item.parent_id FROM drive.item item JOIN ancestors child ON child.parent_id = item.id
		), authorized_parent AS (
			SELECT parent.* FROM parent, selected_user actor
			WHERE EXISTS (SELECT 1 FROM drive.drive_member member WHERE member.drive_id = parent.drive_id AND member.user_id = actor.id)
				OR EXISTS (
					SELECT 1 FROM drive.item_permission permission
					WHERE permission.item_id IN (SELECT id FROM ancestors)
						AND permission.grantee_user_id = actor.id
						AND (permission.expires_at IS NULL OR permission.expires_at > now())
				)
		), children AS (
			SELECT item.id, item.public_id, item.kind, item.name, COALESCE(item.folder_color, '') AS folder_color,
				CASE WHEN item.kind = 'folder' THEN 0 ELSE 1 END AS sort_group,
				lower(item.name) AS sort_name
			FROM drive.item item JOIN authorized_parent parent ON parent.id = item.parent_id
			WHERE item.trashed_at IS NULL AND item.kind IN ('folder', 'file', 'shortcut')
		)
		SELECT child.id, child.public_id::text, child.kind, child.name, child.folder_color, child.sort_group, child.sort_name,
			COALESCE(version.storage_key, ''), COALESCE(version.size_bytes, 0),
			COALESCE(version.source_modified_at, version.created_at, now()),
			COALESCE(version.mime_type, ''), COALESCE(version.storage_etag, ''),
			state.starred_at, state.last_opened_at
		FROM children child
		LEFT JOIN drive.file_version version ON version.item_id = child.id AND version.is_current AND version.state = 'ready'
		CROSS JOIN selected_user actor
		LEFT JOIN drive.user_item_state state ON state.item_id = child.id AND state.user_id = actor.id
		WHERE (child.kind IN ('folder', 'shortcut') OR version.id IS NOT NULL)
			AND (NOT $3::boolean OR child.sort_group > $4
				OR (child.sort_group = $4 AND (child.sort_name > $5
					OR (child.sort_name = $5 AND child.id > $6))))
		ORDER BY child.sort_group, child.sort_name, child.id
		LIMIT $7
	`, userPublicID, parentPublicID, hasCursor, position.Group, position.Name, position.ID, limit+1)
	if err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("list accessible children: %w", err)
	}
	defer rows.Close()
	folders := make([]model.FolderEntry, 0, limit)
	files := make([]model.FileObject, 0, limit)
	shortcuts := make([]model.ShortcutEntry, 0, limit)
	positions := make([]pagecursor.Position, 0, limit+1)
	for rows.Next() {
		var internalID int64
		var publicID, kind, name, sortName string
		var sortGroup int
		var file model.FileObject
		var starredAt, openedAt *time.Time
		var folderColor string
		if err := rows.Scan(
			&internalID, &publicID, &kind, &name, &folderColor, &sortGroup, &sortName,
			&file.Key, &file.Size, &file.LastModified, &file.ContentType, &file.ETag,
			&starredAt, &openedAt,
		); err != nil {
			return model.ListResponse{}, nil, fmt.Errorf("scan accessible child: %w", err)
		}
		positions = append(positions, pagecursor.Position{Group: sortGroup, Name: sortName, ID: internalID})
		if len(positions) > limit {
			continue
		}
		if kind == "folder" {
			folders = append(folders, model.FolderEntry{ID: publicID, Name: name, StarredAt: starredAt, LastOpenedAt: openedAt, FolderColor: folderColor})
		} else if kind == "shortcut" {
			shortcuts = append(shortcuts, model.ShortcutEntry{ID: publicID, Name: name, StarredAt: starredAt, LastOpenedAt: openedAt})
		} else {
			file.ID, file.Name, file.StarredAt, file.LastOpenedAt = publicID, name, starredAt, openedAt
			files = append(files, file)
		}
	}
	if err := rows.Err(); err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("iterate accessible children: %w", err)
	}
	if len(positions) == 0 {
		var allowed bool
		if err := m.pool.QueryRow(ctx, `
			WITH RECURSIVE ancestors AS (
				SELECT id, parent_id, drive_id FROM drive.item WHERE public_id = $2::uuid AND kind = 'folder' AND trashed_at IS NULL
				UNION ALL SELECT item.id, item.parent_id, item.drive_id FROM drive.item item JOIN ancestors child ON child.parent_id = item.id
			)
			SELECT EXISTS (
				SELECT 1 FROM drive.user_account actor
				WHERE actor.public_id = $1::uuid AND (
					EXISTS (SELECT 1 FROM drive.drive_member member WHERE member.drive_id = (SELECT drive_id FROM ancestors LIMIT 1) AND member.user_id = actor.id)
					OR EXISTS (SELECT 1 FROM drive.item_permission permission WHERE permission.item_id IN (SELECT id FROM ancestors) AND permission.grantee_user_id = actor.id AND (permission.expires_at IS NULL OR permission.expires_at > now()))
				)
			)
		`, userPublicID, parentPublicID).Scan(&allowed); err != nil || !allowed {
			return model.ListResponse{}, nil, ErrItemNotFound
		}
	}
	hasMore := len(positions) > limit
	var next *pagecursor.Position
	if hasMore {
		last := positions[limit-1]
		next = &last
	}
	return model.ListResponse{Folders: folders, Files: files, Shortcuts: shortcuts, Pagination: model.Pagination{Limit: limit, Returned: len(folders) + len(files) + len(shortcuts), HasMore: hasMore}}, next, nil
}

func (m *Metadata) ListItemPermissions(ctx context.Context, actorPublicID, itemPublicID string) ([]ItemPermission, error) {
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `
		SELECT permission.public_id::text, grantee.public_id::text, grantee.primary_email,
			grantee.display_name, permission.role, permission.expires_at, permission.created_at
		FROM drive.item_permission permission
		JOIN drive.user_account grantee ON grantee.id = permission.grantee_user_id
		JOIN drive.item item ON item.id = permission.item_id
		WHERE item.public_id = $1::uuid
		ORDER BY lower(grantee.primary_email), permission.id
	`, itemPublicID)
	if err != nil {
		return nil, fmt.Errorf("list item permissions: %w", err)
	}
	defer rows.Close()
	permissions := make([]ItemPermission, 0)
	for rows.Next() {
		var permission ItemPermission
		if err := rows.Scan(&permission.ID, &permission.UserID, &permission.Email, &permission.DisplayName, &permission.Role, &permission.ExpiresAt, &permission.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan item permission: %w", err)
		}
		permissions = append(permissions, permission)
	}
	return permissions, rows.Err()
}

func (m *Metadata) ListShareInvitations(ctx context.Context, actorPublicID, itemPublicID string) ([]ShareInvitation, error) {
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `
		SELECT invitation.public_id::text, invitation.invited_email, invitation.role,
			invitation.status, invitation.expires_at, invitation.created_at
		FROM drive.share_invitation invitation
		JOIN drive.item item ON item.id = invitation.item_id
		WHERE item.public_id = $1::uuid AND invitation.status = 'pending'
		ORDER BY lower(invitation.invited_email), invitation.id
	`, itemPublicID)
	if err != nil {
		return nil, fmt.Errorf("list share invitations: %w", err)
	}
	defer rows.Close()
	invitations := make([]ShareInvitation, 0)
	for rows.Next() {
		var invitation ShareInvitation
		if err := rows.Scan(&invitation.ID, &invitation.Email, &invitation.Role, &invitation.Status, &invitation.ExpiresAt, &invitation.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan share invitation: %w", err)
		}
		invitations = append(invitations, invitation)
	}
	return invitations, rows.Err()
}

func validateShareRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "viewer" && role != "commenter" && role != "editor" {
		return "", ErrInvalidRole
	}
	return role, nil
}

func normalizeInviteEmail(email string) (string, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || !strings.Contains(address.Address, "@") {
		return "", fmt.Errorf("invalid invitation email")
	}
	return strings.ToLower(address.Address), nil
}

func newInvitationToken() (string, []byte, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", nil, fmt.Errorf("generate invitation token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

func (m *Metadata) CreateShareInvitation(ctx context.Context, actorPublicID, itemPublicID, email, role string) (ShareInvitation, error) {
	role, err := validateShareRole(role)
	if err != nil {
		return ShareInvitation{}, err
	}
	email, err = normalizeInviteEmail(email)
	if err != nil {
		return ShareInvitation{}, err
	}
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return ShareInvitation{}, err
	}
	_, tokenHash, err := newInvitationToken()
	if err != nil {
		return ShareInvitation{}, err
	}
	var invitation ShareInvitation
	err = m.pool.QueryRow(ctx, `
		WITH actor AS (
			SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
		), target AS (
			SELECT id, drive_id FROM drive.item WHERE public_id = $2::uuid AND trashed_at IS NULL
		), invitation AS (
			INSERT INTO drive.share_invitation (
				item_id, invited_email, role, token_hash, invited_by_user_id, expires_at
			)
			SELECT target.id, $3, $4, $5, actor.id, now() + interval '7 days'
			FROM target, actor
			ON CONFLICT (item_id, (lower(invited_email))) WHERE status = 'pending'
			DO UPDATE SET role = EXCLUDED.role, token_hash = EXCLUDED.token_hash,
				expires_at = EXCLUDED.expires_at, invited_by_user_id = EXCLUDED.invited_by_user_id,
				updated_at = now()
			RETURNING id, public_id, invited_email, role, status, expires_at, created_at
		), notification AS (
			INSERT INTO drive.notification_outbox (recipient_email, notification_type, payload)
			SELECT invitation.invited_email, 'share.invited', jsonb_build_object(
				'item_id', $2::text, 'invitation_id', invitation.public_id::text,
				'role', invitation.role, 'expires_at', invitation.expires_at
			) FROM invitation
		), event AS (
			INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
			SELECT target.drive_id, target.id, actor.id, 'share.invited',
				jsonb_build_object('invited_email', $3::text, 'role', $4::text)
			FROM target, actor
		)
		SELECT public_id::text, invited_email, role, status, expires_at, created_at FROM invitation
	`, actorPublicID, itemPublicID, email, role, tokenHash).Scan(
		&invitation.ID, &invitation.Email, &invitation.Role, &invitation.Status, &invitation.ExpiresAt, &invitation.CreatedAt,
	)
	if err != nil {
		return ShareInvitation{}, fmt.Errorf("create share invitation: %w", err)
	}
	return invitation, nil
}

func (m *Metadata) RevokeShareInvitation(ctx context.Context, actorPublicID, itemPublicID, invitationPublicID string) error {
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return err
	}
	command, err := m.pool.Exec(ctx, `
		UPDATE drive.share_invitation invitation
		SET status = 'revoked', updated_at = now()
		FROM drive.item item
		WHERE invitation.item_id = item.id AND item.public_id = $1::uuid
			AND invitation.public_id::text = $2 AND invitation.status = 'pending'
	`, itemPublicID, invitationPublicID)
	if err != nil {
		return fmt.Errorf("revoke share invitation: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

func (m *Metadata) ResendShareInvitation(ctx context.Context, actorPublicID, itemPublicID, invitationPublicID string) (ShareInvitation, error) {
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return ShareInvitation{}, err
	}
	_, tokenHash, err := newInvitationToken()
	if err != nil {
		return ShareInvitation{}, err
	}
	var invitation ShareInvitation
	err = m.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE drive.share_invitation invitation
			SET token_hash = $3, expires_at = now() + interval '7 days', updated_at = now()
			FROM drive.item item
			WHERE invitation.item_id = item.id AND item.public_id = $1::uuid
				AND invitation.public_id::text = $2 AND invitation.status = 'pending'
			RETURNING invitation.public_id, invitation.invited_email, invitation.role,
				invitation.status, invitation.expires_at, invitation.created_at
		), notification AS (
			INSERT INTO drive.notification_outbox (recipient_email, notification_type, payload)
			SELECT invited_email, 'share.invited', jsonb_build_object(
				'item_id', $1::text, 'invitation_id', public_id::text,
				'role', role, 'expires_at', expires_at
			) FROM updated
		)
		SELECT public_id::text, invited_email, role, status, expires_at, created_at FROM updated
	`, itemPublicID, invitationPublicID, tokenHash).Scan(
		&invitation.ID, &invitation.Email, &invitation.Role, &invitation.Status, &invitation.ExpiresAt, &invitation.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareInvitation{}, ErrItemNotFound
	}
	if err != nil {
		return ShareInvitation{}, fmt.Errorf("resend share invitation: %w", err)
	}
	return invitation, nil
}

func (m *Metadata) GrantItemPermission(ctx context.Context, actorPublicID, itemPublicID, email, role string, expiresAt *time.Time) (ItemPermission, error) {
	role, err := validateShareRole(role)
	if err != nil {
		return ItemPermission{}, err
	}
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return ItemPermission{}, err
	}
	var permission ItemPermission
	err = m.pool.QueryRow(ctx, `
		WITH actor AS (
			SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
		), target AS (
			SELECT id, drive_id FROM drive.item WHERE public_id = $2::uuid AND trashed_at IS NULL
		), grantee AS (
			SELECT id, public_id, primary_email, display_name
			FROM drive.user_account
			WHERE lower(primary_email) = lower($3) AND status = 'active'
		), permission AS (
			INSERT INTO drive.item_permission (item_id, grantee_user_id, role, expires_at, created_by_user_id)
			SELECT target.id, grantee.id, $4, $5, actor.id
			FROM target, grantee, actor
			WHERE grantee.id <> actor.id
			ON CONFLICT (item_id, grantee_user_id) DO UPDATE
			SET role = EXCLUDED.role, expires_at = EXCLUDED.expires_at, created_by_user_id = EXCLUDED.created_by_user_id
			RETURNING id, public_id, grantee_user_id, role, expires_at, created_at
		), event AS (
			INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
			SELECT target.drive_id, target.id, actor.id, 'permission.granted',
				jsonb_build_object('grantee_user_id', permission.grantee_user_id, 'role', permission.role)
			FROM target, actor, permission
		), notification AS (
			INSERT INTO drive.notification_outbox (recipient_user_id, recipient_email, notification_type, payload)
			SELECT permission.grantee_user_id, grantee.primary_email, 'item.shared',
				jsonb_build_object('item_id', $2, 'role', permission.role)
			FROM permission JOIN grantee ON grantee.id = permission.grantee_user_id
		)
		SELECT permission.public_id::text, grantee.public_id::text, grantee.primary_email,
			grantee.display_name, permission.role, permission.expires_at, permission.created_at
		FROM permission JOIN grantee ON grantee.id = permission.grantee_user_id
	`, actorPublicID, itemPublicID, strings.TrimSpace(email), role, expiresAt).Scan(
		&permission.ID, &permission.UserID, &permission.Email, &permission.DisplayName,
		&permission.Role, &permission.ExpiresAt, &permission.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ItemPermission{}, ErrInviteeNotFound
	}
	if err != nil {
		return ItemPermission{}, fmt.Errorf("grant item permission: %w", err)
	}
	return permission, nil
}

func (m *Metadata) RevokeItemPermission(ctx context.Context, actorPublicID, itemPublicID, permissionPublicID string) error {
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return err
	}
	command, err := m.pool.Exec(ctx, `
		DELETE FROM drive.item_permission permission
		USING drive.item item
		WHERE permission.item_id = item.id AND item.public_id = $1::uuid
			AND permission.public_id = $2::uuid
	`, itemPublicID, permissionPublicID)
	if err != nil {
		return fmt.Errorf("revoke item permission: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

func (m *Metadata) requireShareManager(ctx context.Context, actorPublicID, itemPublicID string) error {
	var allowed bool
	err := m.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM drive.user_account actor
			JOIN drive.item item ON item.public_id = $2::uuid AND item.trashed_at IS NULL
			JOIN drive.drive_member member ON member.drive_id = item.drive_id AND member.user_id = actor.id
			WHERE actor.public_id = $1::uuid AND actor.status = 'active'
				AND member.role IN ('owner', 'manager')
		)
	`, actorPublicID, itemPublicID).Scan(&allowed)
	if err != nil {
		return fmt.Errorf("authorize share management: %w", err)
	}
	if !allowed {
		return ErrPermissionDenied
	}
	return nil
}
