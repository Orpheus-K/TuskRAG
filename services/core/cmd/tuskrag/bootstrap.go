package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/secrets"
)

func bootstrap(ctx context.Context) error {
	databaseURL := os.Getenv("TUSKRAG_DATABASE_URL")
	tenant := os.Getenv("TUSKRAG_BOOTSTRAP_TENANT")
	login := os.Getenv("TUSKRAG_BOOTSTRAP_LOGIN")
	displayName := os.Getenv("TUSKRAG_BOOTSTRAP_DISPLAY_NAME")
	passwordFile := os.Getenv("TUSKRAG_BOOTSTRAP_PASSWORD_FILE")
	if strings.TrimSpace(databaseURL) == "" || tenant == "" || login == "" || displayName == "" || !filepath.IsAbs(passwordFile) {
		return errors.New("database URL, bootstrap tenant, login, display name and absolute password-file path are required")
	}
	password, err := readPasswordFile(passwordFile)
	if err != nil {
		clear(password)
		return err
	}
	defer clear(password)
	startupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	pool, err := db.Open(startupCtx, databaseURL)
	cancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	hasher, err := identity.NewPasswordHasher(1)
	if err != nil {
		return err
	}
	service, err := identity.NewService(pool, hasher, identity.SessionConfig{IdleTimeout: 8 * time.Hour, AbsoluteTimeout: 7 * 24 * time.Hour})
	if err != nil {
		return err
	}
	principal, err := service.Bootstrap(ctx, identity.BootstrapRequest{
		TenantName: tenant, DisplayName: displayName, Login: login, Password: password,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		TenantID int64 `json:"tenant_id"`
		UserID   int64 `json:"user_id"`
	}{TenantID: principal.TenantID, UserID: principal.UserID})
}

func readPasswordFile(path string) ([]byte, error) {
	const maxSize = 1026 // Password plus an optional CRLF.
	password, err := secrets.ReadPrivateFile(path, maxSize)
	if err != nil {
		clear(password)
		return nil, fmt.Errorf("read bootstrap password file: %w", err)
	}
	if bytes.HasSuffix(password, []byte("\n")) {
		password = bytes.TrimSuffix(password, []byte("\n"))
		password = bytes.TrimSuffix(password, []byte("\r"))
	}
	return password, nil
}
