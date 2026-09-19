package repository

import (
	"context"
	"fmt"
)

type ExpectedBlob struct {
	VersionID     string
	ItemID        string
	Name          string
	Bucket        string
	Key           string
	SizeBytes     int64
	ETag          string
	Required      bool
	CleanupQueued bool
}

func (m *Metadata) ListExpectedBlobs(ctx context.Context, drivePublicID, userPublicID string) ([]ExpectedBlob, error) {
	var authorized bool
	if err := m.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM drive.drive_space drive
			JOIN drive.user_account actor ON actor.public_id = $2::uuid AND actor.status = 'active'
			JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = actor.id
			WHERE drive.public_id = $1::uuid AND drive.status = 'active'
				AND member.role IN ('owner', 'manager')
		)
	`, drivePublicID, userPublicID).Scan(&authorized); err != nil {
		return nil, fmt.Errorf("authorize consistency audit: %w", err)
	}
	if !authorized {
		return nil, ErrPermissionDenied
	}
	rows, err := m.pool.Query(ctx, `
		SELECT version.public_id::text, item.public_id::text, item.name,
			version.storage_bucket, version.storage_key,
			COALESCE(version.size_bytes, session.expected_size_bytes, 0),
			COALESCE(version.storage_etag, ''),
			version.state IN ('ready', 'quarantined') AS required,
			COALESCE(deletion.status IN ('queued', 'processing'), false) AS cleanup_queued
		FROM drive.drive_space drive
		JOIN drive.item item ON item.drive_id = drive.id
		JOIN drive.file_version version ON version.item_id = item.id
		LEFT JOIN drive.upload_session session ON session.file_version_id = version.id
			AND session.status IN ('initiated', 'uploading', 'completing')
		LEFT JOIN drive.blob_deletion_job deletion ON deletion.file_version_id = version.id
		WHERE drive.public_id = $1::uuid AND (
			version.state IN ('ready', 'quarantined')
			OR session.id IS NOT NULL
			OR deletion.status IN ('queued', 'processing')
		)
		ORDER BY version.id
	`, drivePublicID)
	if err != nil {
		return nil, fmt.Errorf("list expected blobs: %w", err)
	}
	defer rows.Close()
	result := make([]ExpectedBlob, 0)
	for rows.Next() {
		var blob ExpectedBlob
		if err := rows.Scan(
			&blob.VersionID, &blob.ItemID, &blob.Name, &blob.Bucket, &blob.Key,
			&blob.SizeBytes, &blob.ETag, &blob.Required, &blob.CleanupQueued,
		); err != nil {
			return nil, fmt.Errorf("scan expected blob: %w", err)
		}
		result = append(result, blob)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expected blobs: %w", err)
	}
	return result, nil
}

func (m *Metadata) RecordConsistencyAudit(ctx context.Context, drivePublicID, userPublicID string, expectedCount, actualCount, issueCount int) error {
	command, err := m.pool.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, actor_user_id, event_type, details)
		SELECT drive.id, actor.id, 'drive.consistency_audited', jsonb_build_object(
			'expected_count', $3::integer, 'actual_count', $4::integer, 'issue_count', $5::integer
		)
		FROM drive.drive_space drive
		JOIN drive.user_account actor ON actor.public_id = $2::uuid
		JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = actor.id
		WHERE drive.public_id = $1::uuid AND member.role IN ('owner', 'manager')
	`, drivePublicID, userPublicID, expectedCount, actualCount, issueCount)
	if err != nil {
		return fmt.Errorf("record consistency audit: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrPermissionDenied
	}
	return nil
}
