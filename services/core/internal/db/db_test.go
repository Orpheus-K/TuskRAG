package db

import (
	"context"
	"strings"
	"testing"
)

func TestInvalidDatabaseURLDoesNotExposePassword(t *testing.T) {
	const secret = "test-secret-must-not-be-logged"
	const invalidURL = "postgres://user:" + secret + "@localhost:invalid/db"
	if _, err := Open(context.Background(), invalidURL); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid application database URL was not safely rejected: %v", err)
	}
	if err := Migrate(context.Background(), invalidURL); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid migration database URL was not safely rejected: %v", err)
	}
}
