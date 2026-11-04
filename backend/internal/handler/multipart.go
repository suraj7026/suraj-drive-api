package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/storage"
)

const multipartPartSize int64 = 32 << 20

type MultipartHandler struct {
	store *storage.MinIOClient
	key   []byte
}

type multipartClaims struct {
	jwt.RegisteredClaims
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	UploadID string `json:"upload_id"`
	Size     int64  `json:"size"`
}

func NewMultipartHandler(store *storage.MinIOClient, secret string) *MultipartHandler {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("surajdrive:legacy-multipart:v1"))
	return &MultipartHandler{store: store, key: mac.Sum(nil)}
}

func (h *MultipartHandler) Begin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key         string `json:"key"`
		Size        int64  `json:"size"`
		ContentType string `json:"content_type"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Key == "" || len(body.Key) > 1024 || body.Size <= 0 || body.Size > multipartPartSize*10_000 {
		writeError(w, http.StatusBadRequest, errors.New("invalid file path or size"))
		return
	}
	bucket, err := bucketFromRequest(r, h.store)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	marker := rand.Text()
	key, id, err := h.store.BeginMultipart(r.Context(), bucket, body.Key, body.ContentType, marker)
	if err != nil {
		writeMultipartError(w, err)
		return
	}
	claims := multipartClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   auth.ClaimsFromContext(r.Context()).Subject,
			ID:        marker,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		},
		Bucket: bucket, Key: key, UploadID: id, Size: body.Size,
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.key)
	if err != nil {
		writeMultipartError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "key": key, "part_size": multipartPartSize})
}

func (h *MultipartHandler) Update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Part  int    `json:"part"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid multipart request"))
		return
	}
	var claims multipartClaims
	_, err := jwt.ParseWithClaims(body.Token, &claims, func(_ *jwt.Token) (any, error) { return h.key, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(),
		jwt.WithSubject(auth.ClaimsFromContext(r.Context()).Subject))
	if err != nil {
		writeError(w, http.StatusForbidden, errors.New("upload session is invalid or expired"))
		return
	}
	ctx := r.Context()
	switch chi.URLParam(r, "action") {
	case "part":
		count := int((claims.Size + multipartPartSize - 1) / multipartPartSize)
		if body.Part < 1 || body.Part > count {
			writeError(w, http.StatusBadRequest, errors.New("invalid upload part"))
			return
		}
		size := min(multipartPartSize, claims.Size-int64(body.Part-1)*multipartPartSize)
		signed, err := h.store.PresignMultipartPart(ctx, claims.Bucket, claims.Key, claims.UploadID, body.Part, size)
		if err != nil {
			writeMultipartError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": signed})
	case "complete":
		if err := h.store.FinishMultipart(ctx, claims.Bucket, claims.Key, claims.UploadID, claims.ID, claims.Size, multipartPartSize); err != nil {
			writeMultipartError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"key": claims.Key})
	case "abort":
		if err := h.store.CancelMultipart(ctx, claims.Bucket, claims.Key, claims.UploadID); err != nil {
			writeMultipartError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func writeMultipartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, storage.ErrVersionLimitReached), errors.Is(err, storage.ErrMultipartConflict), errors.Is(err, storage.ErrMultipartIncomplete):
		writeError(w, http.StatusConflict, err)
	default:
		log.Error().Err(err).Msg("multipart upload operation failed")
		writeError(w, http.StatusBadGateway, fmt.Errorf("storage did not finish the upload request"))
	}
}
