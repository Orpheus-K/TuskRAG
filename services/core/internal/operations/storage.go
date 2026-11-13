// Package operations contains read-only maintenance checks. Callers must stop
// writers before checking a backup or comparing a restored environment.
package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/storage"
)

type StorageReport struct {
	TenantID int64 `json:"tenant_id"`
	Versions int64 `json:"verified_versions"`
	Bytes    int64 `json:"verified_bytes"`
}

// VerifyStorage checks every retained version, including failed and superseded
// versions. A missing purging original is also an incomplete maintenance state.
// It never changes version status or silently repairs data.
func VerifyStorage(ctx context.Context, pool *pgxpool.Pool, files storage.Store, tenantID int64) (StorageReport, error) {
	report := StorageReport{TenantID: tenantID}
	if pool == nil || files == nil || tenantID <= 0 {
		return report, errors.New("database, storage and positive tenant ID are required")
	}
	var after int64
	for {
		page, err := storagePage(ctx, pool, tenantID, after)
		if err != nil {
			return report, err
		}
		for _, version := range page {
			key := storage.Key{TenantID: tenantID, KBID: version.KbID, DocumentID: version.DocumentID, VersionID: version.ID}
			canonical, err := key.Path()
			if err != nil || canonical != version.FileKey {
				return report, fmt.Errorf("version %d: noncanonical storage key", version.ID)
			}
			if err := verifyOriginal(ctx, files, key, version.ByteSize, version.ContentSha256); err != nil {
				return report, fmt.Errorf("version %d: %w", version.ID, err)
			}
			report.Versions++
			report.Bytes += version.ByteSize
			after = version.ID
		}
		if len(page) < 100 {
			return report, nil
		}
	}
}

func storagePage(ctx context.Context, pool *pgxpool.Pool, tenantID, after int64) (result []dbquery.StorageVersionsPageRow, resultErr error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin storage check: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("rollback storage check: %w", err))
		}
	}()
	q := dbquery.New(tx)
	if _, err := q.SetTenant(ctx, tenantID); err != nil {
		return nil, fmt.Errorf("set storage tenant: %w", err)
	}
	exists, err := q.IdentityTenantExists(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("check storage tenant: %w", err)
	}
	if !exists {
		return nil, errors.New("storage tenant does not exist")
	}
	result, err = q.StorageVersionsPage(ctx, dbquery.StorageVersionsPageParams{TenantID: tenantID, AfterID: after})
	if err != nil {
		return nil, fmt.Errorf("read storage versions: %w", err)
	}
	return result, tx.Commit(ctx)
}

func verifyOriginal(ctx context.Context, files storage.Store, key storage.Key, size int64, digest []byte) (resultErr error) {
	if size <= 0 || size > 200<<20 || len(digest) != sha256.Size {
		return errors.New("invalid original metadata")
	}
	file, err := files.Open(ctx, key)
	if err != nil {
		return fmt.Errorf("open original: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, size+1))
	if err != nil {
		return fmt.Errorf("read original: %w", err)
	}
	if n != size || !bytes.Equal(hash.Sum(nil), digest) {
		return errors.New("original size or SHA-256 mismatch")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
