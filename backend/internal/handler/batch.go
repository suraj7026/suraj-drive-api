package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
)

const maxBatchItems = 100

type batchResult struct {
	ItemID string `json:"item_id"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

func (h *FileHandler) BatchItems(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		Operation      string   `json:"operation"`
		ItemIDs        []string `json:"item_ids"`
		IdempotencyKey string   `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || len(body.ItemIDs) == 0 || len(body.ItemIDs) > maxBatchItems || strings.TrimSpace(body.IdempotencyKey) == "" || len(body.IdempotencyKey) > 64 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("item_ids must contain 1 to %d items", maxBatchItems))
		return
	}
	body.Operation = strings.ToLower(strings.TrimSpace(body.Operation))
	if body.Operation != "trash" && body.Operation != "restore" && body.Operation != "star" && body.Operation != "unstar" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported batch operation"))
		return
	}
	seen := make(map[string]struct{}, len(body.ItemIDs))
	results := make([]batchResult, 0, len(body.ItemIDs))
	failures := 0
	for _, itemID := range body.ItemIDs {
		itemID = strings.TrimSpace(itemID)
		if itemID == "" {
			failures++
			results = append(results, batchResult{Status: "failed", Code: "invalid_item_id"})
			continue
		}
		if _, duplicate := seen[itemID]; duplicate {
			failures++
			results = append(results, batchResult{ItemID: itemID, Status: "failed", Code: "duplicate_item_id"})
			continue
		}
		seen[itemID] = struct{}{}
		var err error
		itemKey := body.IdempotencyKey + ":" + itemID
		switch body.Operation {
		case "trash":
			err = h.metadata.SetItemTrashedIdempotent(r.Context(), principal.DriveID, principal.UserID, itemID, true, itemKey)
		case "restore":
			err = h.metadata.SetItemTrashedIdempotent(r.Context(), principal.DriveID, principal.UserID, itemID, false, itemKey)
		case "star":
			err = h.metadata.SetItemStarredIdempotent(r.Context(), principal.UserID, itemID, true, itemKey)
		case "unstar":
			err = h.metadata.SetItemStarredIdempotent(r.Context(), principal.UserID, itemID, false, itemKey)
		}
		if err == nil {
			results = append(results, batchResult{ItemID: itemID, Status: "succeeded"})
			continue
		}
		failures++
		code := "operation_failed"
		if errors.Is(err, repository.ErrItemNotFound) {
			code = "item_not_found"
		} else if errors.Is(err, repository.ErrPermissionDenied) {
			code = "permission_denied"
		} else if errors.Is(err, repository.ErrDeletionStarted) {
			code = "deletion_started"
		}
		results = append(results, batchResult{ItemID: itemID, Status: "failed", Code: code})
	}
	status := http.StatusOK
	if failures > 0 {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, map[string]any{
		"operation": body.Operation, "succeeded": len(results) - failures,
		"failed": failures, "results": results,
	})
}
