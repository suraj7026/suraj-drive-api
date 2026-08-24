package auth

import "testing"

func TestIssueAndValidateJWTIncludesRevocableSessionID(t *testing.T) {
	token, err := IssueJWT("test-secret", "user-public-id", "session-token", "user@example.com", "User", "", 1)
	if err != nil {
		t.Fatalf("issue JWT: %v", err)
	}
	claims, err := ValidateJWT("test-secret", token)
	if err != nil {
		t.Fatalf("validate JWT: %v", err)
	}
	if claims.Subject != "user-public-id" || claims.ID != "session-token" || claims.Email != "user@example.com" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestValidateJWTRejectsWrongSecret(t *testing.T) {
	token, err := IssueJWT("correct-secret", "user-public-id", "session-token", "user@example.com", "User", "", 1)
	if err != nil {
		t.Fatalf("issue JWT: %v", err)
	}
	if _, err := ValidateJWT("wrong-secret", token); err == nil {
		t.Fatal("expected signature validation failure")
	}
}
