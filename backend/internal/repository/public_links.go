package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"surajdrive/backend/internal/model"
)

type ShareLink struct {
	ID            string     `json:"id"`
	Role          string     `json:"role"`
	AllowDownload bool       `json:"allow_download"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type PublicLinkItem struct {
	LinkID        string `json:"-"`
	ItemID        string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	MIMEType      string `json:"mime_type,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	Bucket        string `json:"-"`
	StorageKey    string `json:"-"`
	AllowDownload bool   `json:"allow_download"`
}

func newShareLinkToken() (string, []byte, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", nil, fmt.Errorf("generate share link token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

func (m *Metadata) ListShareLinks(ctx context.Context, actorPublicID, itemPublicID string) ([]ShareLink, error) {
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `
		SELECT link.public_id::text, link.role, link.allow_download, link.expires_at, link.created_at
		FROM drive.share_link link JOIN drive.item item ON item.id = link.item_id
		WHERE item.public_id = $1::uuid AND link.revoked_at IS NULL
			AND (link.expires_at IS NULL OR link.expires_at > now())
		ORDER BY link.created_at DESC, link.id DESC
	`, itemPublicID)
	if err != nil {
		return nil, fmt.Errorf("list share links: %w", err)
	}
	defer rows.Close()
	links := make([]ShareLink, 0)
	for rows.Next() {
		var link ShareLink
		if err := rows.Scan(&link.ID, &link.Role, &link.AllowDownload, &link.ExpiresAt, &link.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan share link: %w", err)
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func (m *Metadata) CreateShareLink(ctx context.Context, actorPublicID, itemPublicID, role string, allowDownload bool, expiresAt *time.Time) (ShareLink, string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "viewer" && role != "commenter" {
		return ShareLink{}, "", ErrInvalidRole
	}
	if expiresAt != nil && (expiresAt.Before(time.Now()) || expiresAt.After(time.Now().Add(366*24*time.Hour))) {
		return ShareLink{}, "", fmt.Errorf("share link expiry must be within one year")
	}
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return ShareLink{}, "", err
	}
	token, tokenHash, err := newShareLinkToken()
	if err != nil {
		return ShareLink{}, "", err
	}
	var link ShareLink
	err = m.pool.QueryRow(ctx, `
		WITH actor AS (
			SELECT id FROM drive.user_account WHERE public_id = $1::uuid
		), target AS (
			SELECT id, drive_id FROM drive.item WHERE public_id = $2::uuid AND trashed_at IS NULL
		), created AS (
			INSERT INTO drive.share_link (item_id, token_hash, role, allow_download, expires_at, created_by_user_id)
			SELECT target.id, $3, $4, $5, $6, actor.id FROM target, actor
			RETURNING id, public_id, role, allow_download, expires_at, created_at
		), event AS (
			INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
			SELECT target.drive_id, target.id, actor.id, 'share_link.created',
				jsonb_build_object('link_id', created.public_id::text, 'allow_download', created.allow_download)
			FROM target, actor, created
		)
		SELECT public_id::text, role, allow_download, expires_at, created_at FROM created
	`, actorPublicID, itemPublicID, tokenHash, role, allowDownload, expiresAt).Scan(
		&link.ID, &link.Role, &link.AllowDownload, &link.ExpiresAt, &link.CreatedAt,
	)
	if err != nil {
		return ShareLink{}, "", fmt.Errorf("create share link: %w", err)
	}
	return link, token, nil
}

func (m *Metadata) RevokeShareLink(ctx context.Context, actorPublicID, itemPublicID, linkPublicID string) error {
	if err := m.requireShareManager(ctx, actorPublicID, itemPublicID); err != nil {
		return err
	}
	command, err := m.pool.Exec(ctx, `
		UPDATE drive.share_link link SET revoked_at = COALESCE(revoked_at, now())
		FROM drive.item item
		WHERE link.item_id = item.id AND item.public_id = $1::uuid
			AND link.public_id::text = $2 AND link.revoked_at IS NULL
	`, itemPublicID, linkPublicID)
	if err != nil {
		return fmt.Errorf("revoke share link: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

func (m *Metadata) ResolvePublicLink(ctx context.Context, token string) (PublicLinkItem, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil || len(decoded) != 32 {
		return PublicLinkItem{}, ErrItemNotFound
	}
	digest := sha256.Sum256([]byte(token))
	var item PublicLinkItem
	err = m.pool.QueryRow(ctx, `
		SELECT link.public_id::text, item.public_id::text, item.kind, item.name,
			COALESCE(version.mime_type, ''), COALESCE(version.size_bytes, 0),
			COALESCE(version.storage_bucket, ''), COALESCE(version.storage_key, ''), link.allow_download
		FROM drive.share_link link
		JOIN drive.item item ON item.id = link.item_id AND item.trashed_at IS NULL
		LEFT JOIN drive.file_version version ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		WHERE link.token_hash = $1 AND link.revoked_at IS NULL
			AND (link.expires_at IS NULL OR link.expires_at > now())
			AND (item.kind = 'folder' OR version.id IS NOT NULL)
	`, digest[:]).Scan(
		&item.LinkID, &item.ItemID, &item.Kind, &item.Name, &item.MIMEType, &item.SizeBytes,
		&item.Bucket, &item.StorageKey, &item.AllowDownload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicLinkItem{}, ErrItemNotFound
	}
	if err != nil {
		return PublicLinkItem{}, fmt.Errorf("resolve public share link: %w", err)
	}
	return item, nil
}

func (m *Metadata) ResolvePublicLinkFile(ctx context.Context, token, itemPublicID string) (PublicLinkItem, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil || len(decoded) != 32 {
		return PublicLinkItem{}, ErrItemNotFound
	}
	digest := sha256.Sum256([]byte(token))
	var item PublicLinkItem
	err = m.pool.QueryRow(ctx, `
		WITH RECURSIVE link_root AS (
			SELECT link.public_id AS link_public_id, link.item_id, link.allow_download
			FROM drive.share_link link
			WHERE link.token_hash = $1 AND link.revoked_at IS NULL
				AND (link.expires_at IS NULL OR link.expires_at > now())
		), descendants AS (
			SELECT item.id FROM drive.item item JOIN link_root root ON root.item_id = item.id
			UNION ALL SELECT child.id FROM drive.item child JOIN descendants parent ON child.parent_id = parent.id
			WHERE child.trashed_at IS NULL
		)
		SELECT root.link_public_id::text, item.public_id::text, item.kind, item.name,
			version.mime_type, version.size_bytes, version.storage_bucket, version.storage_key, root.allow_download
		FROM link_root root
		JOIN descendants tree ON true
		JOIN drive.item item ON item.id = tree.id AND item.public_id::text = $2
		JOIN drive.file_version version ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		WHERE item.kind = 'file' AND item.trashed_at IS NULL
	`, digest[:], itemPublicID).Scan(
		&item.LinkID, &item.ItemID, &item.Kind, &item.Name, &item.MIMEType, &item.SizeBytes,
		&item.Bucket, &item.StorageKey, &item.AllowDownload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicLinkItem{}, ErrItemNotFound
	}
	if err != nil {
		return PublicLinkItem{}, fmt.Errorf("resolve public link file: %w", err)
	}
	return item, nil
}

func (m *Metadata) ListPublicLinkChildren(ctx context.Context, token, parentPublicID string) (PublicLinkItem, model.ListResponse, error) {
	root, err := m.ResolvePublicLink(ctx, token)
	if err != nil {
		return PublicLinkItem{}, model.ListResponse{}, err
	}
	if root.Kind != "folder" {
		return PublicLinkItem{}, model.ListResponse{}, ErrItemNotFound
	}
	if parentPublicID == "" {
		parentPublicID = root.ItemID
	}
	digest := sha256.Sum256([]byte(token))
	rows, err := m.pool.Query(ctx, `
		WITH RECURSIVE link_root AS (
			SELECT item.id FROM drive.share_link link JOIN drive.item item ON item.id = link.item_id
			WHERE link.token_hash = $1 AND link.revoked_at IS NULL
				AND (link.expires_at IS NULL OR link.expires_at > now())
		), descendants AS (
			SELECT item.id, item.parent_id FROM drive.item item JOIN link_root root ON root.id = item.id
			UNION ALL SELECT child.id, child.parent_id FROM drive.item child JOIN descendants parent ON child.parent_id = parent.id
			WHERE child.trashed_at IS NULL
		), selected_parent AS (
			SELECT item.id FROM drive.item item JOIN descendants tree ON tree.id = item.id
			WHERE item.public_id::text = $2 AND item.kind = 'folder' AND item.trashed_at IS NULL
		)
		SELECT child.public_id::text, child.kind, child.name,
			COALESCE(version.storage_key, ''), COALESCE(version.size_bytes, 0),
			COALESCE(version.source_modified_at, version.created_at, child.updated_at),
			COALESCE(version.mime_type, ''), COALESCE(version.storage_etag, ''), COALESCE(child.folder_color, '')
		FROM drive.item child JOIN selected_parent parent ON child.parent_id = parent.id
		LEFT JOIN drive.file_version version ON version.item_id = child.id AND version.is_current AND version.state = 'ready'
		WHERE child.trashed_at IS NULL AND child.kind IN ('folder', 'file')
			AND (child.kind = 'folder' OR version.id IS NOT NULL)
		ORDER BY child.kind = 'file', lower(child.name), child.id
		LIMIT 501
	`, digest[:], parentPublicID)
	if err != nil {
		return PublicLinkItem{}, model.ListResponse{}, fmt.Errorf("list public link children: %w", err)
	}
	defer rows.Close()
	response := model.ListResponse{CurrentFolderID: parentPublicID, Folders: []model.FolderEntry{}, Files: []model.FileObject{}}
	for rows.Next() {
		var publicID, kind, name, key, folderColor string
		var file model.FileObject
		if err := rows.Scan(&publicID, &kind, &name, &key, &file.Size, &file.LastModified, &file.ContentType, &file.ETag, &folderColor); err != nil {
			return PublicLinkItem{}, model.ListResponse{}, fmt.Errorf("scan public link child: %w", err)
		}
		if kind == "folder" {
			response.Folders = append(response.Folders, model.FolderEntry{ID: publicID, Name: name, FolderColor: folderColor})
		} else {
			file.ID, file.Name, file.Key = publicID, name, ""
			response.Files = append(response.Files, file)
		}
	}
	if err := rows.Err(); err != nil {
		return PublicLinkItem{}, model.ListResponse{}, err
	}
	response.Pagination = model.Pagination{Limit: 500, Returned: len(response.Folders) + len(response.Files), HasMore: len(response.Folders)+len(response.Files) > 500}
	if response.Pagination.HasMore {
		response.Folders, response.Files = truncatePublicListing(response.Folders, response.Files, 500)
		response.Pagination.Returned = 500
	}
	return root, response, nil
}

func truncatePublicListing(folders []model.FolderEntry, files []model.FileObject, limit int) ([]model.FolderEntry, []model.FileObject) {
	if len(folders) >= limit {
		return folders[:limit], []model.FileObject{}
	}
	return folders, files[:min(len(files), limit-len(folders))]
}
