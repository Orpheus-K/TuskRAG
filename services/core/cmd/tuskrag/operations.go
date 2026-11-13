package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/operations"
	"tuskrag.local/core/internal/storage"
)

func runOperations(ctx context.Context, args []string) (resultErr error) {
	if len(args) != 2 || args[0] != "verify-storage" {
		return errors.New("usage: tuskrag ops verify-storage <tenant-id>")
	}
	tenantID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || tenantID <= 0 {
		return errors.New("tenant-id must be a positive integer")
	}
	path := os.Getenv("TUSKRAG_STORAGE_DIR")
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errors.New("TUSKRAG_STORAGE_DIR must name an existing directory")
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := db.OpenWithLimit(startup, os.Getenv("TUSKRAG_DATABASE_URL"), 1)
	if err != nil {
		return err
	}
	defer pool.Close()
	files, err := storage.NewLocal(ctx, path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, files.Close()) }()
	report, err := operations.VerifyStorage(ctx, pool, files, tenantID)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
