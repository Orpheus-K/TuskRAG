package main

import (
	"context"
	"errors"
	"os"
	"strconv"

	"tuskrag.local/core/internal/indexing"
)

func provisionIndexes(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: tuskrag provision-indexes <tenant-id> <space-id>")
	}
	tenantID, tenantErr := strconv.ParseInt(args[0], 10, 64)
	spaceID, spaceErr := strconv.ParseInt(args[1], 10, 64)
	if tenantErr != nil || spaceErr != nil || tenantID <= 0 || spaceID <= 0 {
		return errors.New("tenant-id and space-id must be positive integers")
	}
	return indexing.Provision(ctx, os.Getenv("TUSKRAG_DATABASE_URL"), tenantID, spaceID)
}
