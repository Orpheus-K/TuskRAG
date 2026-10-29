package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/storage"
)

// VerifyOriginal rechecks the immutable source outside a database transaction
// and confirms its metadata and lease again before returning.
func (s *ParseStage) VerifyOriginal(ctx context.Context, job jobs.ClaimedJob) error {
	if s == nil || s.queue == nil || s.files == nil || ctx == nil {
		return errors.New("initialized parse stage and context are required")
	}
	ids, err := parseJob(job)
	if err != nil {
		return &jobs.ProcessingFailure{Code: jobs.Internal, Cause: err}
	}
	version, err := s.originalMetadata(ctx, job, ids)
	if err != nil {
		return err
	}
	key := storage.Key{TenantID: job.Lease.TenantID, KBID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}
	path, err := key.Path()
	if err != nil || path != version.FileKey || version.ByteSize <= 0 || version.ByteSize > parser.MaxInputBytes || len(version.ContentSha256) != sha256.Size {
		return s.failure(job, ids, jobs.FileCorrupt, errCorruptOriginal)
	}
	file, err := s.files.Open(ctx, key)
	if errors.Is(err, fs.ErrNotExist) {
		return s.failure(job, ids, jobs.FileMissing, err)
	}
	if err != nil {
		return fmt.Errorf("%w: open original for verification: %w", errInfrastructure, err)
	}
	hash := sha256.New()
	size, readErr := io.Copy(hash, io.LimitReader(file, version.ByteSize+1))
	closeErr := file.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("%w: read or close original: %w", errInfrastructure, errors.Join(readErr, closeErr))
	}
	if size != version.ByteSize || !bytes.Equal(hash.Sum(nil), version.ContentSha256) {
		return s.failure(job, ids, jobs.FileCorrupt, errCorruptOriginal)
	}
	current, err := s.originalMetadata(ctx, job, ids)
	if err != nil {
		return err
	}
	if current.FileKey != version.FileKey || current.ByteSize != version.ByteSize || current.Mime != version.Mime ||
		!bytes.Equal(current.ContentSha256, version.ContentSha256) {
		return s.failure(job, ids, jobs.FileCorrupt, errCorruptOriginal)
	}
	return nil
}

func (s *ParseStage) originalMetadata(ctx context.Context, job jobs.ClaimedJob, ids documentIDs) (dbquery.LockIngestVersionRow, error) {
	var version dbquery.LockIngestVersionRow
	err := s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
			return err
		}
		var err error
		version, err = queries.LockIngestVersion(ctx, lockParams(job.Lease.TenantID, ids))
		if errors.Is(err, pgx.ErrNoRows) {
			return errTarget
		}
		if err != nil {
			return fmt.Errorf("lock original metadata: %w", err)
		}
		if version.Status != "embedding" && version.Status != "indexing" {
			return errTarget
		}
		return nil
	})
	return version, err
}
