package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPasswordHashAndVerification(t *testing.T) {
	hasher, err := NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	password := []byte("correct horse battery staple")
	first, err := hasher.Hash(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hasher.Hash(context.Background(), password)
	if err != nil || first == second || !strings.HasPrefix(first, passwordPrefix) {
		t.Fatalf("password hashes did not use distinct salts: err=%v", err)
	}
	match, err := hasher.Verify(context.Background(), password, first)
	if err != nil || !match {
		t.Fatalf("correct password rejected: match=%t err=%v", match, err)
	}
	match, err = hasher.Verify(context.Background(), []byte("incorrect"), first)
	if err != nil || match {
		t.Fatalf("incorrect password accepted: match=%t err=%v", match, err)
	}
}

func TestPasswordRejectsUnsafeInputAndParameters(t *testing.T) {
	hasher, err := NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hasher.Hash(context.Background(), nil); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("empty password accepted: %v", err)
	}
	if _, err := hasher.Hash(context.Background(), make([]byte, passwordMaxBytes+1)); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("oversized password accepted: %v", err)
	}
	for _, encoded := range []string{
		"",
		"$argon2id$v=19$m=2147483647,t=99,p=99$bad$bad",
		passwordPrefix + "bad$bad",
		passwordPrefix + strings.Repeat("A", 300),
	} {
		if _, err := hasher.Verify(context.Background(), []byte("password"), encoded); !errors.Is(err, ErrInvalidPasswordHash) {
			t.Fatalf("invalid or unbounded hash accepted: %v", err)
		}
	}
	if _, err := NewPasswordHasher(0); err == nil {
		t.Fatal("unbounded or invalid concurrency accepted")
	}
	if _, err := NewPasswordHasher(3); err == nil {
		t.Fatal("excessive hashing concurrency accepted")
	}
}

func TestPasswordHashingWaitCanBeCancelled(t *testing.T) {
	hasher, err := NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	hasher.slots <- struct{}{}
	defer hasher.release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hasher.Hash(ctx, []byte("password")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled hash did not stop before acquiring resources: %v", err)
	}
}

func TestSessionTokenRejectsNoncanonicalInput(t *testing.T) {
	for _, token := range []string{"", "0.AA", "01.AA", "-1.AA", "1.AA", "1." + strings.Repeat("A", 43) + "\n", strings.Repeat("A", 65)} {
		if _, _, err := parseToken(token); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("malformed session token accepted: %q err=%v", token, err)
		}
	}
}
