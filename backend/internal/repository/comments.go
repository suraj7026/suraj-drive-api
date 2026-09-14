package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxCommentBodyBytes = 10_000

var mentionPattern = regexp.MustCompile(`(?i)(?:^|\s)@([a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+)`)

type Comment struct {
	ID          string     `json:"id"`
	ParentID    string     `json:"parent_id,omitempty"`
	AuthorID    string     `json:"author_id"`
	AuthorName  string     `json:"author_name"`
	AuthorEmail string     `json:"author_email"`
	Body        string     `json:"body"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Deleted     bool       `json:"deleted"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CanEdit     bool       `json:"can_edit"`
	CanResolve  bool       `json:"can_resolve"`
	CanReply    bool       `json:"can_reply"`
}

type commentAccess struct {
	UserID  int64
	ItemID  int64
	DriveID int64
	Rank    int
}

type commentRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadCommentAccess(ctx context.Context, query commentRowQuerier, userPublicID, itemPublicID string) (commentAccess, error) {
	var access commentAccess
	err := query.QueryRow(ctx, `
		WITH RECURSIVE actor AS (
			SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
		), target AS (
			SELECT id, parent_id, drive_id FROM drive.item
			WHERE public_id::text = $2 AND trashed_at IS NULL
		), ancestors AS (
			SELECT id, parent_id FROM target
			UNION ALL
			SELECT parent.id, parent.parent_id
			FROM drive.item parent JOIN ancestors child ON child.parent_id = parent.id
		), permission_rank AS (
			SELECT COALESCE(max(CASE permission.role
				WHEN 'viewer' THEN 1 WHEN 'commenter' THEN 2 WHEN 'editor' THEN 3 ELSE 0 END), 0) AS rank
			FROM drive.item_permission permission, actor
			WHERE permission.item_id IN (SELECT id FROM ancestors)
				AND permission.grantee_user_id = actor.id
				AND (permission.expires_at IS NULL OR permission.expires_at > now())
		)
		SELECT actor.id, target.id, target.drive_id,
			GREATEST(COALESCE(CASE member.role
				WHEN 'viewer' THEN 1 WHEN 'commenter' THEN 2 WHEN 'editor' THEN 3
				WHEN 'manager' THEN 4 WHEN 'owner' THEN 5 ELSE 0 END, 0), permission_rank.rank)
		FROM actor CROSS JOIN target CROSS JOIN permission_rank
		LEFT JOIN drive.drive_member member ON member.drive_id = target.drive_id AND member.user_id = actor.id
	`, userPublicID, itemPublicID).Scan(&access.UserID, &access.ItemID, &access.DriveID, &access.Rank)
	if errors.Is(err, pgx.ErrNoRows) {
		return commentAccess{}, ErrItemNotFound
	}
	if err != nil {
		return commentAccess{}, fmt.Errorf("authorize item comments: %w", err)
	}
	if access.Rank == 0 {
		return commentAccess{}, ErrPermissionDenied
	}
	return access, nil
}

func (m *Metadata) ListComments(ctx context.Context, userPublicID, itemPublicID string) ([]Comment, error) {
	access, err := loadCommentAccess(ctx, m.pool, userPublicID, itemPublicID)
	if err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `
		SELECT comment.public_id::text, COALESCE(parent.public_id::text, ''),
			author.public_id::text, author.display_name, author.primary_email,
			CASE WHEN comment.deleted_at IS NULL THEN comment.body ELSE '' END,
			comment.resolved_at, comment.deleted_at IS NOT NULL,
			comment.created_at, comment.updated_at, comment.author_user_id
		FROM drive.comment comment
		JOIN drive.user_account author ON author.id = comment.author_user_id
		LEFT JOIN drive.comment parent ON parent.id = comment.parent_comment_id
		WHERE comment.item_id = $1
		ORDER BY COALESCE(comment.parent_comment_id, comment.id),
			comment.parent_comment_id IS NOT NULL, comment.created_at, comment.id
	`, access.ItemID)
	if err != nil {
		return nil, fmt.Errorf("list item comments: %w", err)
	}
	defer rows.Close()
	comments := make([]Comment, 0)
	for rows.Next() {
		var comment Comment
		var authorInternalID int64
		if err := rows.Scan(
			&comment.ID, &comment.ParentID, &comment.AuthorID, &comment.AuthorName, &comment.AuthorEmail,
			&comment.Body, &comment.ResolvedAt, &comment.Deleted, &comment.CreatedAt, &comment.UpdatedAt,
			&authorInternalID,
		); err != nil {
			return nil, fmt.Errorf("scan item comment: %w", err)
		}
		comment.CanEdit = !comment.Deleted && (authorInternalID == access.UserID || access.Rank >= 4)
		comment.CanResolve = comment.ParentID == "" && !comment.Deleted && access.Rank >= 3
		comment.CanReply = comment.ParentID == "" && access.Rank >= 2
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate item comments: %w", err)
	}
	return comments, nil
}

func validateCommentBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" || len([]byte(body)) > maxCommentBodyBytes {
		return "", fmt.Errorf("comment body must contain 1 to %d bytes", maxCommentBodyBytes)
	}
	return body, nil
}

func (m *Metadata) CreateComment(ctx context.Context, userPublicID, itemPublicID, parentPublicID, body string) (Comment, error) {
	body, err := validateCommentBody(body)
	if err != nil {
		return Comment{}, err
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, fmt.Errorf("begin comment creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	access, err := loadCommentAccess(ctx, tx, userPublicID, itemPublicID)
	if err != nil {
		return Comment{}, err
	}
	if access.Rank < 2 {
		return Comment{}, ErrPermissionDenied
	}
	var parentID *int64
	if parentPublicID != "" {
		var resolvedParentID int64
		if err := tx.QueryRow(ctx, `
			SELECT id FROM drive.comment
			WHERE item_id = $1 AND public_id::text = $2 AND parent_comment_id IS NULL
		`, access.ItemID, parentPublicID).Scan(&resolvedParentID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Comment{}, ErrItemNotFound
			}
			return Comment{}, fmt.Errorf("resolve parent comment: %w", err)
		}
		parentID = &resolvedParentID
	}
	var commentID, commentPublicID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO drive.comment (item_id, parent_comment_id, author_user_id, body)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, public_id::text
	`, access.ItemID, parentID, access.UserID, body).Scan(&commentID, &commentPublicID); err != nil {
		return Comment{}, fmt.Errorf("create item comment: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
		VALUES ($1, $2, $3, $4, jsonb_build_object('comment_id', $5::text))
	`, access.DriveID, access.ItemID, access.UserID, map[bool]string{true: "comment.replied", false: "comment.created"}[parentID != nil], commentPublicID); err != nil {
		return Comment{}, fmt.Errorf("record comment activity: %w", err)
	}
	for _, email := range mentionedEmails(body) {
		if _, err := tx.Exec(ctx, `
			WITH RECURSIVE target AS (
				SELECT id, parent_id, drive_id FROM drive.item WHERE id = $1
			), ancestors AS (
				SELECT id, parent_id FROM target
				UNION ALL SELECT parent.id, parent.parent_id
				FROM drive.item parent JOIN ancestors child ON child.parent_id = parent.id
			), recipient AS (
				SELECT account.id, account.primary_email
				FROM drive.user_account account, target
				WHERE lower(account.primary_email) = lower($2) AND account.status = 'active' AND account.id <> $3
					AND (EXISTS (SELECT 1 FROM drive.drive_member member WHERE member.drive_id = target.drive_id AND member.user_id = account.id)
						OR EXISTS (SELECT 1 FROM drive.item_permission permission WHERE permission.item_id IN (SELECT id FROM ancestors)
							AND permission.grantee_user_id = account.id AND (permission.expires_at IS NULL OR permission.expires_at > now())))
			)
			INSERT INTO drive.notification_outbox (recipient_user_id, recipient_email, notification_type, payload)
			SELECT recipient.id, recipient.primary_email, 'comment.mentioned',
				jsonb_build_object('item_id', $4::text, 'comment_id', $5::text)
			FROM recipient
		`, access.ItemID, email, access.UserID, itemPublicID, commentPublicID); err != nil {
			return Comment{}, fmt.Errorf("queue comment mention: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, fmt.Errorf("commit comment creation: %w", err)
	}
	comments, err := m.ListComments(ctx, userPublicID, itemPublicID)
	if err != nil {
		return Comment{}, err
	}
	for _, comment := range comments {
		if comment.ID == commentPublicID {
			return comment, nil
		}
	}
	return Comment{}, fmt.Errorf("created comment %s not found", commentID)
}

func mentionedEmails(body string) []string {
	seen := make(map[string]struct{})
	emails := make([]string, 0)
	for _, match := range mentionPattern.FindAllStringSubmatch(body, -1) {
		email := strings.ToLower(match[1])
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		emails = append(emails, email)
	}
	return emails
}

func (m *Metadata) UpdateComment(ctx context.Context, userPublicID, itemPublicID, commentPublicID, body string) (Comment, error) {
	body, err := validateCommentBody(body)
	if err != nil {
		return Comment{}, err
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, fmt.Errorf("begin comment update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	access, err := loadCommentAccess(ctx, tx, userPublicID, itemPublicID)
	if err != nil {
		return Comment{}, err
	}
	command, err := tx.Exec(ctx, `
		UPDATE drive.comment
		SET body = $4
		WHERE item_id = $1 AND public_id::text = $2 AND deleted_at IS NULL
			AND (author_user_id = $3 OR $5 >= 4)
	`, access.ItemID, commentPublicID, access.UserID, body, access.Rank)
	if err != nil {
		return Comment{}, fmt.Errorf("update item comment: %w", err)
	}
	if command.RowsAffected() == 0 {
		return Comment{}, ErrPermissionDenied
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
		VALUES ($1, $2, $3, 'comment.updated', jsonb_build_object('comment_id', $4::text))
	`, access.DriveID, access.ItemID, access.UserID, commentPublicID); err != nil {
		return Comment{}, fmt.Errorf("record comment update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, fmt.Errorf("commit comment update: %w", err)
	}
	comments, err := m.ListComments(ctx, userPublicID, itemPublicID)
	if err != nil {
		return Comment{}, err
	}
	for _, comment := range comments {
		if comment.ID == commentPublicID {
			return comment, nil
		}
	}
	return Comment{}, ErrItemNotFound
}

func (m *Metadata) DeleteComment(ctx context.Context, userPublicID, itemPublicID, commentPublicID string) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin comment deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	access, err := loadCommentAccess(ctx, tx, userPublicID, itemPublicID)
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE drive.comment
		SET deleted_at = COALESCE(deleted_at, now())
		WHERE item_id = $1 AND public_id::text = $2
			AND (author_user_id = $3 OR $4 >= 4)
	`, access.ItemID, commentPublicID, access.UserID, access.Rank)
	if err != nil {
		return fmt.Errorf("delete item comment: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrPermissionDenied
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
		VALUES ($1, $2, $3, 'comment.deleted', jsonb_build_object('comment_id', $4::text))
	`, access.DriveID, access.ItemID, access.UserID, commentPublicID); err != nil {
		return fmt.Errorf("record comment deletion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit comment deletion: %w", err)
	}
	return nil
}

func (m *Metadata) SetCommentResolved(ctx context.Context, userPublicID, itemPublicID, commentPublicID string, resolved bool) (Comment, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, fmt.Errorf("begin comment resolution: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	access, err := loadCommentAccess(ctx, tx, userPublicID, itemPublicID)
	if err != nil {
		return Comment{}, err
	}
	if access.Rank < 3 {
		return Comment{}, ErrPermissionDenied
	}
	command, err := tx.Exec(ctx, `
		UPDATE drive.comment
		SET resolved_at = CASE WHEN $4 THEN now() ELSE NULL END,
			resolved_by_user_id = CASE WHEN $4 THEN $3::bigint ELSE NULL::bigint END
		WHERE item_id = $1 AND public_id::text = $2
			AND parent_comment_id IS NULL AND deleted_at IS NULL
	`, access.ItemID, commentPublicID, access.UserID, resolved)
	if err != nil {
		return Comment{}, fmt.Errorf("update comment resolution: %w", err)
	}
	if command.RowsAffected() == 0 {
		return Comment{}, ErrItemNotFound
	}
	eventType := "comment.reopened"
	if resolved {
		eventType = "comment.resolved"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
		VALUES ($1, $2, $3, $4, jsonb_build_object('comment_id', $5::text))
	`, access.DriveID, access.ItemID, access.UserID, eventType, commentPublicID); err != nil {
		return Comment{}, fmt.Errorf("record comment resolution: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, fmt.Errorf("commit comment resolution: %w", err)
	}
	comments, err := m.ListComments(ctx, userPublicID, itemPublicID)
	if err != nil {
		return Comment{}, err
	}
	for _, comment := range comments {
		if comment.ID == commentPublicID {
			return comment, nil
		}
	}
	return Comment{}, ErrItemNotFound
}
