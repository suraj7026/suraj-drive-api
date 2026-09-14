package pagination

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const cursorVersion = 1

var ErrInvalidCursor = errors.New("invalid cursor")

type Position struct {
	Group int
	Name  string
	ID    int64
	Score float64
	Time  int64
}

type Codec struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

type payload struct {
	Version   int     `json:"v"`
	ScopeHash string  `json:"s"`
	Group     int     `json:"g"`
	Name      string  `json:"n"`
	ID        int64   `json:"i"`
	Score     float64 `json:"r,omitempty"`
	Time      int64   `json:"t,omitempty"`
	ExpiresAt int64   `json:"e"`
}

func NewCodec(secret string, ttl time.Duration) *Codec {
	key := sha256.Sum256([]byte("surajdrive/cursor/v1\x00" + secret))
	return &Codec{key: key[:], ttl: ttl, now: time.Now}
}

func (c *Codec) Encode(scope string, position Position) (string, error) {
	body, err := json.Marshal(payload{
		Version:   cursorVersion,
		ScopeHash: hashScope(scope),
		Group:     position.Group,
		Name:      position.Name,
		ID:        position.ID,
		Score:     position.Score,
		Time:      position.Time,
		ExpiresAt: c.now().Add(c.ttl).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}

	signature := c.sign(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c *Codec) Decode(token, scope string) (Position, error) {
	var result Position
	separator := -1
	for index := len(token) - 1; index >= 0; index-- {
		if token[index] == '.' {
			separator = index
			break
		}
	}
	if separator <= 0 || separator == len(token)-1 {
		return result, ErrInvalidCursor
	}

	body, err := base64.RawURLEncoding.DecodeString(token[:separator])
	if err != nil {
		return result, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(token[separator+1:])
	if err != nil || !hmac.Equal(signature, c.sign(body)) {
		return result, ErrInvalidCursor
	}

	var decoded payload
	if err := json.Unmarshal(body, &decoded); err != nil {
		return result, ErrInvalidCursor
	}
	if decoded.Version != cursorVersion || decoded.ScopeHash != hashScope(scope) || decoded.ID <= 0 || decoded.Group < 0 || decoded.Group > 1 {
		return result, ErrInvalidCursor
	}
	if c.now().Unix() > decoded.ExpiresAt {
		return result, ErrInvalidCursor
	}

	return Position{Group: decoded.Group, Name: decoded.Name, ID: decoded.ID, Score: decoded.Score, Time: decoded.Time}, nil
}

func (c *Codec) sign(body []byte) []byte {
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

func hashScope(scope string) string {
	digest := sha256.Sum256([]byte(scope))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
