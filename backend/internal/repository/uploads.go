package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"surajdrive/backend/internal/validation"
)

var (
	ErrQuotaExceeded      = errors.New("storage quota exceeded")
	ErrUploadNotFound     = errors.New("upload not found")
	ErrUploadState        = errors.New("upload is not completable")
	ErrUploadSizeMismatch = errors.New("uploaded size does not match reservation")
	ErrUploadIntegrity    = errors.New("uploaded object is missing integrity metadata")
)

type UploadReservation struct {
	ID              string    `json:"upload_id"`
	StorageKey      string    `json:"key"`
	Name            string    `json:"name"`
	ExpectedSize    int64     `json:"expected_size"`
	MIMEType        string    `json:"mime_type"`
	Status          string    `json:"status"`
	ExpiresAt       time.Time `json:"expires_at"`
	UploadMode      string    `json:"upload_mode"`
	PartSize        int64     `json:"part_size,omitempty"`
	ExpectedSHA256  string    `json:"expected_sha256,omitempty"`
	FinalStorageKey string    `json:"-"`
	ProcessingStage string    `json:"processing_stage,omitempty"`
}

type ReserveUploadInput struct {
	DrivePublicID   string
	UserPublicID    string
	Bucket          string
	ParentPublicID  string
	Prefix          string
	Name            string
	SizeBytes       int64
	MIMEType        string
	IdempotencyKey  string
	TTL             time.Duration
	UploadMode      string
	PartSize        int64
	ConflictMode    string
	SourceVersionID string
	ExpectedSHA256  []byte
}

type StoredUpload struct {
	Bucket          string
	StorageKey      string
	ExpectedSize    int64
	Status          string
	MIMEType        string
	MinIOUploadID   string
	UploadMode      string
	PartSize        int64
	ExpiresAt       time.Time
	ExpectedSHA256  []byte
	FinalStorageKey string
}

type UploadPart struct {
	Number int    `json:"part_number"`
	ETag   string `json:"etag"`
	Size   int64  `json:"size"`
}

type ActiveUpload struct {
	UploadReservation
	UploadedParts []UploadPart `json:"uploaded_parts,omitempty"`
}

type ExpiredUpload struct {
	ID            string
	Bucket        string
	StorageKey    string
	MinIOUploadID string
	UploadMode    string
}

type CompleteUploadInput struct {
	DrivePublicID      string
	UserPublicID       string
	UploadID           string
	ETag               string
	SizeBytes          int64
	MIMEType           string
	LastModified       time.Time
	SHA256             []byte
	RequireMalwareScan bool
	StorageKey         string
	CompletionJobID    string
	CompletionAttempt  int
}

func (m *Metadata) ReserveUpload(ctx context.Context, input ReserveUploadInput) (UploadReservation, error) {
	if err := validation.ItemName(input.Name); err != nil {
		return UploadReservation{}, err
	}
	if input.ParentPublicID == "" {
		if err := validation.ItemPath(strings.TrimSuffix(input.Prefix, "/"), true); err != nil {
			return UploadReservation{}, err
		}
	}
	if input.SizeBytes < 0 || strings.TrimSpace(input.MIMEType) == "" || strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 200 {
		return UploadReservation{}, fmt.Errorf("invalid upload reservation")
	}
	if len(input.ExpectedSHA256) != 0 && len(input.ExpectedSHA256) != sha256.Size {
		return UploadReservation{}, ErrUploadIntegrity
	}
	if input.TTL <= 0 {
		input.TTL = 30 * time.Minute
	}
	if input.UploadMode == "" {
		input.UploadMode = "single"
	}
	if input.UploadMode != "single" && input.UploadMode != "multipart" {
		return UploadReservation{}, fmt.Errorf("invalid upload mode")
	}
	if input.UploadMode == "multipart" && input.PartSize < 5<<20 {
		return UploadReservation{}, fmt.Errorf("multipart part size is too small")
	}
	if input.UploadMode == "single" {
		input.PartSize = 0
	}
	if input.ConflictMode == "" {
		input.ConflictMode = "keep_both"
	}
	if input.ConflictMode != "keep_both" && input.ConflictMode != "new_version" {
		return UploadReservation{}, fmt.Errorf("invalid upload conflict mode")
	}

	var driveID, parentID int64
	var err error
	if input.ParentPublicID != "" {
		driveID, parentID, err = m.resolveFolderByPublicID(ctx, input.DrivePublicID, input.ParentPublicID)
	} else {
		driveID, parentID, _, err = m.resolveFolder(ctx, input.DrivePublicID, input.Prefix)
	}
	if err != nil {
		return UploadReservation{}, err
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadReservation{}, fmt.Errorf("begin upload reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID int64
	var quota int64
	if err := tx.QueryRow(ctx, `
		SELECT id, storage_quota_bytes
		FROM drive.user_account
		WHERE public_id = $1::uuid AND status = 'active'
		FOR UPDATE
	`, input.UserPublicID).Scan(&userID, &quota); err != nil {
		return UploadReservation{}, fmt.Errorf("load upload owner: %w", err)
	}
	request, err := json.Marshal(struct {
		DriveID, ParentID                                                     int64
		Bucket, Name, MIMEType, Mode, Conflict, SourceVersion, ExpectedSHA256 string
		Size, PartSize                                                        int64
	}{driveID, parentID, input.Bucket, input.Name, input.MIMEType, input.UploadMode, input.ConflictMode, input.SourceVersionID, hex.EncodeToString(input.ExpectedSHA256), input.SizeBytes, input.PartSize})
	if err != nil {
		return UploadReservation{}, err
	}
	requestHash := sha256.Sum256(request)
	keyHash := sha256.Sum256([]byte(input.IdempotencyKey))
	ledgerKey := fmt.Sprintf("upload:%x", keyHash)
	replayed, err := claimMutationRequest(ctx, tx, userID, ledgerKey, "upload.reserve", requestHash[:])
	if err != nil {
		return UploadReservation{}, err
	}

	if existing, found, err := findUploadByIdempotency(ctx, tx, userID, input.IdempotencyKey); err != nil {
		return UploadReservation{}, err
	} else if found {
		if !replayed {
			return UploadReservation{}, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return UploadReservation{}, fmt.Errorf("commit idempotent upload lookup: %w", err)
		}
		return existing, nil
	}
	if replayed {
		return UploadReservation{}, ErrUploadNotFound
	}

	usedBytes, reservedBytes, err := accountStorageUsage(ctx, tx, userID)
	if err != nil {
		return UploadReservation{}, err
	}
	if input.SizeBytes > quota-usedBytes-reservedBytes {
		return UploadReservation{}, ErrQuotaExceeded
	}

	lockScope := fmt.Sprintf("%d:%d", driveID, parentID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockScope); err != nil {
		return UploadReservation{}, fmt.Errorf("lock upload namespace: %w", err)
	}

	var itemID, versionID int64
	var itemPublicID string
	reservedName := input.Name
	versionNumber := int64(1)
	if input.ConflictMode == "new_version" {
		err := tx.QueryRow(ctx, `
			SELECT item.id, item.public_id::text,
				COALESCE((SELECT max(version_number) + 1 FROM drive.file_version WHERE item_id = item.id), 1)
			FROM drive.item item
			WHERE item.drive_id = $1 AND item.parent_id = $2 AND lower(item.name) = lower($3)
				AND item.kind = 'file' AND item.trashed_at IS NULL
			FOR UPDATE OF item
		`, driveID, parentID, input.Name).Scan(&itemID, &itemPublicID, &versionNumber)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return UploadReservation{}, fmt.Errorf("find revision target: %w", err)
		}
	}
	if itemID == 0 {
		reservedName, err = reserveAvailableName(ctx, tx, driveID, parentID, input.Name)
		if err != nil {
			return UploadReservation{}, err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
			VALUES ($1, $2, 'file', $3, $4)
			RETURNING id, public_id::text
		`, driveID, parentID, reservedName, userID).Scan(&itemID, &itemPublicID); err != nil {
			return UploadReservation{}, fmt.Errorf("reserve upload item: %w", err)
		}
	}
	var versionPublicID, uploadPublicID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&versionPublicID, &uploadPublicID); err != nil {
		return UploadReservation{}, fmt.Errorf("generate upload identifiers: %w", err)
	}
	finalStorageKey := ".objects/" + itemPublicID + "/" + versionPublicID + "/server/content"
	stagingStorageKey := ".uploads/" + uploadPublicID + "/content"
	if err := tx.QueryRow(ctx, `
		INSERT INTO drive.file_version (
			public_id, item_id, version_number, state, storage_bucket, storage_key,
			mime_type, created_by_user_id
		)
		VALUES ($1::uuid, $2, $7, 'pending', $3, $4, $5, $6)
		RETURNING id
	`, versionPublicID, itemID, input.Bucket, finalStorageKey, input.MIMEType, userID, versionNumber).Scan(&versionID); err != nil {
		return UploadReservation{}, fmt.Errorf("reserve upload version: %w", err)
	}

	expiresAt := time.Now().Add(input.TTL)
	var reservation UploadReservation
	if err := tx.QueryRow(ctx, `
		INSERT INTO drive.upload_session (
			user_id, drive_id, parent_item_id, item_id, file_version_id,
			public_id, idempotency_key, requested_name, expected_size_bytes, expected_sha256, mime_type,
			reserved_storage_key, status, expires_at, upload_mode, part_size_bytes
		)
		VALUES ($1, $2, $3, $4, $5, $14::uuid, $6, $7, $8, NULLIF($15::bytea, ''::bytea), $9, $10, 'initiated', $11, $12, NULLIF($13, 0))
		RETURNING public_id::text, reserved_storage_key, $16::text,
			expected_size_bytes, mime_type, status, expires_at, upload_mode, COALESCE(part_size_bytes, 0), COALESCE(encode(expected_sha256, 'hex'), '')
	`, userID, driveID, parentID, itemID, versionID, input.IdempotencyKey,
		input.Name, input.SizeBytes, input.MIMEType, stagingStorageKey, expiresAt, input.UploadMode, input.PartSize, uploadPublicID, input.ExpectedSHA256, reservedName).Scan(
		&reservation.ID, &reservation.StorageKey, &reservation.Name,
		&reservation.ExpectedSize, &reservation.MIMEType, &reservation.Status, &reservation.ExpiresAt,
		&reservation.UploadMode, &reservation.PartSize, &reservation.ExpectedSHA256,
	); err != nil {
		return UploadReservation{}, fmt.Errorf("create upload reservation: %w", err)
	}
	reservation.FinalStorageKey = finalStorageKey
	if err := completeMutationRequest(ctx, tx, userID, ledgerKey); err != nil {
		return UploadReservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadReservation{}, fmt.Errorf("commit upload reservation: %w", err)
	}
	return reservation, nil
}

func findUploadByIdempotency(ctx context.Context, tx pgx.Tx, userID int64, key string) (UploadReservation, bool, error) {
	var reservation UploadReservation
	err := tx.QueryRow(ctx, `
		SELECT session.public_id::text, COALESCE(session.reserved_storage_key, ''), item.name,
			session.expected_size_bytes, session.mime_type, session.status, session.expires_at, session.upload_mode, COALESCE(session.part_size_bytes, 0), COALESCE(encode(session.expected_sha256, 'hex'), '')
			, version.storage_key
		FROM drive.upload_session session JOIN drive.item item ON item.id = session.item_id
		JOIN drive.file_version version ON version.id = session.file_version_id
		WHERE session.user_id = $1 AND session.idempotency_key = $2
	`, userID, key).Scan(
		&reservation.ID, &reservation.StorageKey, &reservation.Name,
		&reservation.ExpectedSize, &reservation.MIMEType, &reservation.Status, &reservation.ExpiresAt,
		&reservation.UploadMode, &reservation.PartSize, &reservation.ExpectedSHA256, &reservation.FinalStorageKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadReservation{}, false, nil
	}
	if err != nil {
		return UploadReservation{}, false, fmt.Errorf("find idempotent upload: %w", err)
	}
	return reservation, true, nil
}

func reserveAvailableName(ctx context.Context, tx pgx.Tx, driveID, parentID int64, requestedName string) (string, error) {
	extension := path.Ext(requestedName)
	base := strings.TrimSuffix(requestedName, extension)
	for suffix := 0; suffix < 10_000; suffix++ {
		candidateName := requestedName
		if suffix > 0 {
			candidateName = fmt.Sprintf("%s (%d)%s", base, suffix, extension)
		}
		if err := validation.ItemName(candidateName); err != nil {
			return "", err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM drive.item
				WHERE drive_id = $1 AND parent_id = $2 AND lower(name) = lower($3)
					AND trashed_at IS NULL
			)
		`, driveID, parentID, candidateName).Scan(&exists); err != nil {
			return "", fmt.Errorf("check reserved upload name: %w", err)
		}
		if !exists {
			return candidateName, nil
		}
	}
	return "", fmt.Errorf("no available name for upload")
}

func (m *Metadata) LoadUpload(ctx context.Context, drivePublicID, userPublicID, uploadID string) (StoredUpload, error) {
	var upload StoredUpload
	err := m.pool.QueryRow(ctx, `
		SELECT drive.storage_bucket, COALESCE(session.reserved_storage_key, ''),
			session.expected_size_bytes, session.status, session.mime_type,
			COALESCE(session.minio_upload_id, ''), session.upload_mode,
			COALESCE(session.part_size_bytes, 0), session.expires_at,
			COALESCE(session.expected_sha256, ''::bytea), version.storage_key
		FROM drive.upload_session session
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		JOIN drive.user_account owner ON owner.id = session.user_id
		JOIN drive.file_version version ON version.id = session.file_version_id
		WHERE session.public_id = $1::uuid AND drive.public_id = $2::uuid
			AND owner.public_id = $3::uuid
	`, uploadID, drivePublicID, userPublicID).Scan(
		&upload.Bucket, &upload.StorageKey, &upload.ExpectedSize, &upload.Status,
		&upload.MIMEType, &upload.MinIOUploadID, &upload.UploadMode, &upload.PartSize,
		&upload.ExpiresAt, &upload.ExpectedSHA256, &upload.FinalStorageKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredUpload{}, ErrUploadNotFound
	}
	if err != nil {
		return StoredUpload{}, fmt.Errorf("load upload: %w", err)
	}
	return upload, nil
}

func (m *Metadata) ListActiveUploads(ctx context.Context, drivePublicID, userPublicID string) ([]ActiveUpload, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT session.id, session.public_id::text, COALESCE(session.reserved_storage_key, ''),
			item.name, session.expected_size_bytes, session.mime_type, session.status,
			session.expires_at, session.upload_mode, COALESCE(session.part_size_bytes, 0), COALESCE(encode(session.expected_sha256, 'hex'), ''),
			CASE WHEN session.status = 'completing' THEN 'verifying'
				WHEN version.state = 'quarantined' AND scan.status IN ('queued', 'processing') THEN 'scanning'
				ELSE '' END
		FROM drive.upload_session session
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		JOIN drive.user_account actor ON actor.id = session.user_id
		JOIN drive.item item ON item.id = session.item_id
		JOIN drive.file_version version ON version.id = session.file_version_id
		LEFT JOIN drive.malware_scan_job scan ON scan.file_version_id = version.id
		WHERE drive.public_id = $1::uuid AND actor.public_id = $2::uuid
			AND ((session.status IN ('initiated', 'uploading', 'completing')
				AND (session.status = 'completing' OR session.expires_at > now()))
				OR (session.status = 'completed' AND version.state = 'quarantined'
					AND scan.status IN ('queued', 'processing')))
		ORDER BY session.created_at, session.id
	`, drivePublicID, userPublicID)
	if err != nil {
		return nil, fmt.Errorf("list active uploads: %w", err)
	}
	defer rows.Close()
	type activeRow struct {
		internalID int64
		upload     ActiveUpload
	}
	activeRows := make([]activeRow, 0)
	for rows.Next() {
		var row activeRow
		if err := rows.Scan(
			&row.internalID, &row.upload.ID, &row.upload.StorageKey, &row.upload.Name,
			&row.upload.ExpectedSize, &row.upload.MIMEType, &row.upload.Status,
			&row.upload.ExpiresAt, &row.upload.UploadMode, &row.upload.PartSize, &row.upload.ExpectedSHA256,
			&row.upload.ProcessingStage,
		); err != nil {
			return nil, fmt.Errorf("scan active upload: %w", err)
		}
		activeRows = append(activeRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active uploads: %w", err)
	}
	result := make([]ActiveUpload, 0, len(activeRows))
	for _, row := range activeRows {
		partRows, err := m.pool.Query(ctx, `
			SELECT part_number, storage_etag, size_bytes
			FROM drive.upload_part WHERE upload_session_id = $1 ORDER BY part_number
		`, row.internalID)
		if err != nil {
			return nil, fmt.Errorf("list active upload parts: %w", err)
		}
		for partRows.Next() {
			var part UploadPart
			if err := partRows.Scan(&part.Number, &part.ETag, &part.Size); err != nil {
				partRows.Close()
				return nil, fmt.Errorf("scan active upload part: %w", err)
			}
			row.upload.UploadedParts = append(row.upload.UploadedParts, part)
		}
		if err := partRows.Err(); err != nil {
			partRows.Close()
			return nil, fmt.Errorf("iterate active upload parts: %w", err)
		}
		partRows.Close()
		result = append(result, row.upload)
	}
	return result, nil
}

func (m *Metadata) GetActiveUpload(ctx context.Context, drivePublicID, userPublicID, uploadID string) (ActiveUpload, error) {
	uploads, err := m.ListActiveUploads(ctx, drivePublicID, userPublicID)
	if err != nil {
		return ActiveUpload{}, err
	}
	for _, upload := range uploads {
		if upload.ID == uploadID {
			return upload, nil
		}
	}
	return ActiveUpload{}, ErrUploadNotFound
}

func (m *Metadata) AttachMultipartUpload(ctx context.Context, drivePublicID, userPublicID, uploadID, minioUploadID string) (string, error) {
	var actual string
	err := m.pool.QueryRow(ctx, `
		UPDATE drive.upload_session session
		SET minio_upload_id = COALESCE(minio_upload_id, $4), status = 'uploading'
		FROM drive.drive_space drive, drive.user_account owner
		WHERE session.drive_id = drive.id AND session.user_id = owner.id
			AND session.public_id = $1::uuid AND drive.public_id = $2::uuid
			AND owner.public_id = $3::uuid AND session.upload_mode = 'multipart'
			AND session.status IN ('initiated', 'uploading') AND session.expires_at > now()
		RETURNING session.minio_upload_id
	`, uploadID, drivePublicID, userPublicID, minioUploadID).Scan(&actual)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUploadState
	}
	if err != nil {
		return "", fmt.Errorf("attach multipart upload: %w", err)
	}
	return actual, nil
}

func (m *Metadata) RenewUpload(ctx context.Context, drivePublicID, userPublicID, uploadID string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	command, err := m.pool.Exec(ctx, `
		UPDATE drive.upload_session session
		SET expires_at = GREATEST(expires_at, now() + make_interval(secs => $4)), updated_at = now()
		FROM drive.drive_space drive, drive.user_account owner
		WHERE session.drive_id = drive.id AND session.user_id = owner.id
			AND session.public_id = $1::uuid AND drive.public_id = $2::uuid
			AND owner.public_id = $3::uuid AND session.status IN ('initiated', 'uploading')
			AND session.expires_at > now()
	`, uploadID, drivePublicID, userPublicID, max(1, int(ttl.Seconds())))
	if err != nil {
		return fmt.Errorf("renew upload reservation: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrUploadState
	}
	return nil
}

func (m *Metadata) ReplaceUploadParts(ctx context.Context, drivePublicID, userPublicID, uploadID string, parts []UploadPart) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin upload part sync: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sessionID int64
	if err := tx.QueryRow(ctx, `
		SELECT session.id
		FROM drive.upload_session session
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		JOIN drive.user_account owner ON owner.id = session.user_id
		WHERE session.public_id = $1::uuid AND drive.public_id = $2::uuid
			AND owner.public_id = $3::uuid AND session.upload_mode = 'multipart'
		FOR UPDATE OF session
	`, uploadID, drivePublicID, userPublicID).Scan(&sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUploadNotFound
		}
		return fmt.Errorf("lock upload part sync: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM drive.upload_part WHERE upload_session_id = $1`, sessionID); err != nil {
		return fmt.Errorf("clear upload parts: %w", err)
	}
	for _, part := range parts {
		if part.Number < 1 || part.Number > 10_000 || part.Size <= 0 || strings.TrimSpace(part.ETag) == "" {
			return fmt.Errorf("invalid upload part")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.upload_part (upload_session_id, part_number, storage_etag, size_bytes)
			VALUES ($1, $2, $3, $4)
		`, sessionID, part.Number, part.ETag, part.Size); err != nil {
			return fmt.Errorf("record upload part: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit upload part sync: %w", err)
	}
	return nil
}

func (m *Metadata) ListExpiredUploads(ctx context.Context, limit int) ([]ExpiredUpload, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := m.pool.Query(ctx, `
		SELECT session.public_id::text, drive.storage_bucket,
			COALESCE(session.reserved_storage_key, ''), COALESCE(session.minio_upload_id, ''),
			session.upload_mode
		FROM drive.upload_session session
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		WHERE session.status IN ('initiated', 'uploading')
			AND session.expires_at <= now()
		ORDER BY session.expires_at, session.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list expired uploads: %w", err)
	}
	defer rows.Close()
	uploads := make([]ExpiredUpload, 0, limit)
	for rows.Next() {
		var upload ExpiredUpload
		if err := rows.Scan(&upload.ID, &upload.Bucket, &upload.StorageKey, &upload.MinIOUploadID, &upload.UploadMode); err != nil {
			return nil, fmt.Errorf("scan expired upload: %w", err)
		}
		uploads = append(uploads, upload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired uploads: %w", err)
	}
	return uploads, nil
}

func (m *Metadata) MarkUploadExpired(ctx context.Context, uploadID string) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin upload expiry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var versionID int64
	if err := tx.QueryRow(ctx, `
		UPDATE drive.upload_session
		SET status = 'expired', failure_code = 'expired',
			failure_message = 'upload reservation expired before completion'
		WHERE public_id = $1::uuid AND status IN ('initiated', 'uploading')
			AND expires_at <= now()
		RETURNING file_version_id
	`, uploadID).Scan(&versionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("expire upload: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE drive.file_version SET state = 'failed' WHERE id = $1 AND state = 'pending'`, versionID); err != nil {
		return fmt.Errorf("expire upload version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.blob_deletion_job (file_version_id) VALUES ($1)
		ON CONFLICT (file_version_id) DO NOTHING
	`, versionID); err != nil {
		return fmt.Errorf("queue expired upload cleanup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit upload expiry: %w", err)
	}
	return nil
}

func (m *Metadata) AbortUpload(ctx context.Context, drivePublicID, userPublicID, uploadID string) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin upload abort: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sessionID, versionID, itemID, driveID, userID int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT session.id, session.file_version_id, session.item_id, session.drive_id, session.user_id, session.status
		FROM drive.upload_session session
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		JOIN drive.user_account actor ON actor.id = session.user_id
		WHERE session.public_id = $1::uuid AND drive.public_id = $2::uuid AND actor.public_id = $3::uuid
		FOR UPDATE OF session
	`, uploadID, drivePublicID, userPublicID).Scan(&sessionID, &versionID, &itemID, &driveID, &userID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUploadNotFound
		}
		return fmt.Errorf("lock upload abort: %w", err)
	}
	if status == "aborted" || status == "expired" || status == "failed" {
		return tx.Commit(ctx)
	}
	if status == "completed" {
		return ErrUploadState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.upload_session
		SET status = 'aborted', failure_code = 'user_cancelled', failure_message = 'upload cancelled by user'
		WHERE id = $1
	`, sessionID); err != nil {
		return fmt.Errorf("abort upload session: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE drive.file_version SET state = 'failed', is_current = false WHERE id = $1 AND state = 'pending'`, versionID); err != nil {
		return fmt.Errorf("abort upload version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.blob_deletion_job (file_version_id) VALUES ($1)
		ON CONFLICT (file_version_id) DO NOTHING
	`, versionID); err != nil {
		return fmt.Errorf("queue aborted upload cleanup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, file_version_id, actor_user_id, event_type)
		VALUES ($1, $2, $3, $4, 'upload.aborted')
	`, driveID, itemID, versionID, userID); err != nil {
		return fmt.Errorf("record upload abort: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit upload abort: %w", err)
	}
	return nil
}

func (m *Metadata) CompleteUpload(ctx context.Context, input CompleteUploadInput) (UploadReservation, error) {
	if len(input.SHA256) != 32 {
		return UploadReservation{}, ErrUploadIntegrity
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadReservation{}, fmt.Errorf("begin upload completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var reservation UploadReservation
	var sessionID, itemID, versionID, userID int64
	var quota int64
	if err := tx.QueryRow(ctx, `
		SELECT id, storage_quota_bytes FROM drive.user_account
		WHERE public_id = $1::uuid AND status = 'active' FOR UPDATE
	`, input.UserPublicID).Scan(&userID, &quota); err != nil {
		return UploadReservation{}, fmt.Errorf("lock upload owner: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT session.id, session.item_id, session.file_version_id, session.user_id,
			session.public_id::text, session.reserved_storage_key, item.name,
			session.expected_size_bytes, session.mime_type, session.status, session.expires_at,
			session.upload_mode, COALESCE(session.part_size_bytes, 0)
		FROM drive.upload_session session
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		JOIN drive.user_account owner ON owner.id = session.user_id
		JOIN drive.item item ON item.id = session.item_id
		WHERE session.public_id = $1::uuid AND drive.public_id = $2::uuid
			AND owner.public_id = $3::uuid
		FOR UPDATE OF session
	`, input.UploadID, input.DrivePublicID, input.UserPublicID).Scan(
		&sessionID, &itemID, &versionID, &userID, &reservation.ID, &reservation.StorageKey,
		&reservation.Name, &reservation.ExpectedSize, &reservation.MIMEType,
		&reservation.Status, &reservation.ExpiresAt, &reservation.UploadMode, &reservation.PartSize,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UploadReservation{}, ErrUploadNotFound
		}
		return UploadReservation{}, fmt.Errorf("lock upload completion: %w", err)
	}
	if reservation.Status == "completed" {
		if err := tx.Commit(ctx); err != nil {
			return UploadReservation{}, fmt.Errorf("commit completed upload lookup: %w", err)
		}
		return reservation, nil
	}
	if reservation.Status != "initiated" && reservation.Status != "uploading" && reservation.Status != "completing" {
		return UploadReservation{}, ErrUploadState
	}
	if !reservation.ExpiresAt.After(time.Now()) {
		return UploadReservation{}, ErrUploadState
	}
	if strings.TrimSpace(input.ETag) == "" {
		return UploadReservation{}, ErrUploadIntegrity
	}
	if input.CompletionJobID != "" {
		var claimed bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM drive.upload_completion_job
				WHERE public_id = $1::uuid AND upload_session_id = $2
					AND status = 'processing' AND attempts = $3
					AND lease_expires_at > clock_timestamp() AND final_storage_key = $4
			)
		`, input.CompletionJobID, sessionID, input.CompletionAttempt, input.StorageKey).Scan(&claimed); err != nil {
			return UploadReservation{}, fmt.Errorf("verify upload completion claim: %w", err)
		}
		if !claimed || reservation.Status != "completing" {
			return UploadReservation{}, ErrJobLeaseLost
		}
		if len(input.StorageKey) < len(".objects/") || !strings.HasPrefix(input.StorageKey, ".objects/") {
			return UploadReservation{}, ErrUploadIntegrity
		}
	}
	usedBytes, reservedBytes, err := accountStorageUsage(ctx, tx, userID)
	if err != nil {
		return UploadReservation{}, err
	}
	if reservedBytes > quota-usedBytes {
		return UploadReservation{}, ErrQuotaExceeded
	}
	if input.SizeBytes != reservation.ExpectedSize {
		if _, err := tx.Exec(ctx, `
			UPDATE drive.upload_session
			SET status = 'failed', bytes_received = $2, failure_code = 'size_mismatch',
				failure_message = 'uploaded size does not match reservation'
			WHERE id = $1
		`, sessionID, input.SizeBytes); err != nil {
			return UploadReservation{}, fmt.Errorf("fail mismatched upload: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE drive.file_version SET state = 'failed' WHERE id = $1`, versionID); err != nil {
			return UploadReservation{}, fmt.Errorf("fail mismatched upload version: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.blob_deletion_job (file_version_id) VALUES ($1)
			ON CONFLICT (file_version_id) DO NOTHING
		`, versionID); err != nil {
			return UploadReservation{}, fmt.Errorf("queue mismatched upload cleanup: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return UploadReservation{}, fmt.Errorf("commit mismatched upload: %w", err)
		}
		return UploadReservation{}, ErrUploadSizeMismatch
	}
	var expectedSHA []byte
	if err := tx.QueryRow(ctx, `SELECT COALESCE(expected_sha256, ''::bytea) FROM drive.upload_session WHERE id = $1`, sessionID).Scan(&expectedSHA); err != nil {
		return UploadReservation{}, fmt.Errorf("load expected upload checksum: %w", err)
	}
	if len(expectedSHA) != 0 && !bytes.Equal(expectedSHA, input.SHA256) {
		return UploadReservation{}, ErrUploadIntegrity
	}

	mimeType := strings.TrimSpace(input.MIMEType)
	expectedMIMEType := strings.TrimSpace(reservation.MIMEType)
	if mimeType == "" {
		mimeType = expectedMIMEType
	}
	if input.RequireMalwareScan {
		if _, err := tx.Exec(ctx, `
			UPDATE drive.file_version
			SET state = 'quarantined', storage_key = COALESCE(NULLIF($7, ''), storage_key),
				storage_etag = $2, size_bytes = $3, mime_type = $4,
				source_modified_at = $5, sha256 = $6, ready_at = NULL, is_current = false
			WHERE id = $1
		`, versionID, input.ETag, input.SizeBytes, mimeType, input.LastModified, input.SHA256, input.StorageKey); err != nil {
			return UploadReservation{}, fmt.Errorf("quarantine upload version: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.malware_scan_job (file_version_id) VALUES ($1)
			ON CONFLICT (file_version_id) DO UPDATE
			SET status = 'queued', run_after = now(), lease_expires_at = NULL,
				last_error = NULL, updated_at = now()
		`, versionID); err != nil {
			return UploadReservation{}, fmt.Errorf("queue malware scan: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE drive.file_version
			SET is_current = false
			WHERE item_id = $1 AND id <> $2 AND is_current AND state = 'ready'
		`, itemID, versionID); err != nil {
			return UploadReservation{}, fmt.Errorf("retire previous file version: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE drive.file_version
			SET state = 'ready', storage_key = COALESCE(NULLIF($7, ''), storage_key),
				storage_etag = $2, size_bytes = $3, mime_type = $4,
				source_modified_at = $5, sha256 = $6, ready_at = now(), is_current = true
			WHERE id = $1
		`, versionID, input.ETag, input.SizeBytes, mimeType, input.LastModified, input.SHA256, input.StorageKey); err != nil {
			return UploadReservation{}, fmt.Errorf("complete upload version: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.upload_session
		SET status = 'completed', bytes_received = $2, completed_at = now(),
			failure_code = NULL, failure_message = NULL
		WHERE id = $1
	`, sessionID, input.SizeBytes); err != nil {
		return UploadReservation{}, fmt.Errorf("complete upload session: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, file_version_id, actor_user_id, event_type, details)
		SELECT session.drive_id, session.item_id, session.file_version_id, session.user_id,
			CASE WHEN $2::boolean THEN 'upload.quarantined'
				WHEN version.version_number = 1 THEN 'file.created' ELSE 'file.version_created' END,
			jsonb_build_object('storage_key', reserved_storage_key, 'malware_scan_required', $2::boolean)
		FROM drive.upload_session session
		JOIN drive.file_version version ON version.id = session.file_version_id
		WHERE session.id = $1
	`, sessionID, input.RequireMalwareScan); err != nil {
		return UploadReservation{}, fmt.Errorf("record upload activity: %w", err)
	}
	if input.CompletionJobID != "" {
		command, err := tx.Exec(ctx, `
			UPDATE drive.upload_completion_job
			SET status = 'succeeded', completed_at = now(), lease_expires_at = NULL,
				staging_cleanup_after = now() + interval '16 minutes',
				last_error = NULL, updated_at = now()
			WHERE public_id = $1::uuid AND status = 'processing' AND attempts = $2
				AND lease_expires_at > clock_timestamp() AND final_storage_key = $3
		`, input.CompletionJobID, input.CompletionAttempt, input.StorageKey)
		if err != nil {
			return UploadReservation{}, fmt.Errorf("complete upload completion job: %w", err)
		}
		if command.RowsAffected() != 1 {
			return UploadReservation{}, ErrJobLeaseLost
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadReservation{}, fmt.Errorf("commit upload completion: %w", err)
	}
	reservation.Status = "completed"
	return reservation, nil
}
