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
	"tuskrag.local/core/internal/secrets"
	"tuskrag.local/core/internal/storage"
)

func runOperations(ctx context.Context, args []string) (resultErr error) {
	if len(args) != 2 {
		return errors.New("usage: tuskrag ops [verify-storage|diagnose|rotate-master-key] <tenant-id>")
	}
	subcommand := args[0]
	tenantID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || tenantID <= 0 {
		return errors.New("tenant-id must be a positive integer")
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := db.OpenWithLimit(startup, os.Getenv("TUSKRAG_DATABASE_URL"), 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch subcommand {
	case "verify-storage":
		path := os.Getenv("TUSKRAG_STORAGE_DIR")
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			return errors.New("TUSKRAG_STORAGE_DIR must name an existing directory")
		}
		files, fErr := storage.NewLocal(ctx, path)
		if fErr != nil {
			return fErr
		}
		defer func() { resultErr = errors.Join(resultErr, files.Close()) }()
		report, vErr := operations.VerifyStorage(ctx, pool, files, tenantID)
		if vErr != nil {
			return vErr
		}
		return json.NewEncoder(os.Stdout).Encode(report)

	case "diagnose":
		report, dErr := operations.Diagnose(ctx, pool, tenantID)
		if dErr != nil {
			return dErr
		}
		return json.NewEncoder(os.Stdout).Encode(report)

	case "rotate-master-key":
		oldPath := os.Getenv("TUSKRAG_OLD_MASTER_KEY_FILE")
		newPath := os.Getenv("TUSKRAG_MASTER_KEY_FILE")
		if oldPath == "" || newPath == "" {
			return errors.New("TUSKRAG_OLD_MASTER_KEY_FILE and TUSKRAG_MASTER_KEY_FILE are required")
		}
		oldVault, oErr := secrets.LoadVault(oldPath)
		if oErr != nil {
			return oErr
		}
		newVault, nErr := secrets.LoadVault(newPath)
		if nErr != nil {
			return nErr
		}
		report, rErr := operations.RotateMasterKey(ctx, pool, tenantID, oldVault, newVault)
		if rErr != nil {
			return rErr
		}
		return json.NewEncoder(os.Stdout).Encode(report)

	default:
		return errors.New("usage: tuskrag ops [verify-storage|diagnose|rotate-master-key] <tenant-id>")
	}
}
