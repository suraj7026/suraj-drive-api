package auth

import (
	"context"
	"errors"
)

const PrincipalKey contextKey = "principal"

var ErrInvalidSession = errors.New("invalid or expired session")

type Principal struct {
	UserID        string
	Email         string
	Name          string
	Picture       string
	DriveID       string
	DriveRole     string
	StorageBucket string
}

func PrincipalFromContext(ctx context.Context) *Principal {
	principal, _ := ctx.Value(PrincipalKey).(*Principal)
	return principal
}
