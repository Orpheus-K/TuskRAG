package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidPassword     = errors.New("invalid password length")
	ErrInvalidPasswordHash = errors.New("invalid password hash")
)

const (
	passwordMemoryKiB = 64 * 1024
	passwordTime      = 3
	passwordThreads   = 4
	passwordSaltSize  = 16
	passwordHashSize  = 32
	passwordMaxBytes  = 1024
	passwordPrefix    = "$argon2id$v=19$m=65536,t=3,p=4$"
)

// PasswordHasher bounds concurrent Argon2 work and rejects unrecognized cost parameters.
type PasswordHasher struct{ slots chan struct{} }

func NewPasswordHasher(concurrency int) (*PasswordHasher, error) {
	if concurrency < 1 || concurrency > 2 {
		return nil, errors.New("password hashing concurrency must be 1 or 2")
	}
	return &PasswordHasher{slots: make(chan struct{}, concurrency)}, nil
}

func (h *PasswordHasher) Hash(ctx context.Context, password []byte) (string, error) {
	if len(password) == 0 || len(password) > passwordMaxBytes {
		return "", ErrInvalidPassword
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	var salt [passwordSaltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey(password, salt[:], passwordTime, passwordMemoryKiB, passwordThreads, passwordHashSize)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt[:]) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func (h *PasswordHasher) Verify(ctx context.Context, password []byte, encoded string) (bool, error) {
	if len(password) == 0 || len(password) > passwordMaxBytes {
		return false, ErrInvalidPassword
	}
	if len(encoded) > 256 || !strings.HasPrefix(encoded, passwordPrefix) {
		return false, ErrInvalidPasswordHash
	}
	parts := strings.Split(strings.TrimPrefix(encoded, passwordPrefix), "$")
	if len(parts) != 2 {
		return false, ErrInvalidPasswordHash
	}
	salt, saltErr := base64.RawStdEncoding.Strict().DecodeString(parts[0])
	expected, hashErr := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if saltErr != nil || hashErr != nil || len(salt) != passwordSaltSize || len(expected) != passwordHashSize {
		return false, ErrInvalidPasswordHash
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	actual := argon2.IDKey(password, salt, passwordTime, passwordMemoryKiB, passwordThreads, passwordHashSize)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func (h *PasswordHasher) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *PasswordHasher) release() { <-h.slots }
