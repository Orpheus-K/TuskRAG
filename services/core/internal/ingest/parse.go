package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/parsereport"
	"tuskrag.local/core/internal/storage"
)

var (
	ErrJobMismatch     = errors.New("claimed ingest job does not match stored job")
	errTarget          = errors.New("ingest target is unavailable")
	errCorruptOriginal = errors.New("document original failed integrity verification")
	errCleanup         = errors.New("parser temporary file cleanup failed")
	errInfrastructure  = errors.New("parser input infrastructure failed")
)

type DocumentParser interface {
	Parse(context.Context, parser.ParseInput) (parser.Document, error)
}

// SaveReport checkpoints bounded parse metadata under the current lease.
func (s *ParseStage) SaveReport(ctx context.Context, job jobs.ClaimedJob, report []byte) error {
	ids, err := parseJob(job)
	if err != nil {
		return err
	}
	if _, err := parsereport.Decode(report); err != nil {
		return fmt.Errorf("invalid parse report: %w", err)
	}
	return s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
			return err
		}
		version, err := queries.LockIngestVersion(ctx, lockParams(job.Lease.TenantID, ids))
		if errors.Is(err, pgx.ErrNoRows) {
			return errTarget
		}
		if err != nil {
			return fmt.Errorf("lock report version: %w", err)
		}
		if version.Status != "embedding" && version.Status != "indexing" {
			return errTarget
		}
		updated, err := queries.SaveParseReport(ctx, dbquery.SaveParseReportParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
			ContentSha256: version.ContentSha256, ParseReport: report,
		})
		if err != nil {
			return fmt.Errorf("save parse report: %w", err)
		}
		if updated != 1 {
			return errors.New("parse report checkpoint has no matching artifact")
		}
		return nil
	})
}

type ParseStage struct {
	queue     *jobs.Store
	files     storage.Store
	parser    DocumentParser
	validator *parser.Validator
	tempDir   string
	limits    json.RawMessage
}

type documentIDs struct {
	KBID       int64 `json:"kb_id"`
	DocumentID int64 `json:"document_id"`
	VersionID  int64 `json:"version_id"`
}

func NewParseStage(queue *jobs.Store, files storage.Store, documentParser DocumentParser, validator *parser.Validator, tempDir string, limits json.RawMessage) (*ParseStage, error) {
	var parsedLimits map[string]json.RawMessage
	if queue == nil || files == nil || documentParser == nil || validator == nil || !filepath.IsAbs(tempDir) ||
		len(limits) > 4096 || json.Unmarshal(limits, &parsedLimits) != nil || parsedLimits == nil {
		return nil, errors.New("valid parse stage dependencies, temporary directory and limits are required")
	}
	return &ParseStage{queue: queue, files: files, parser: documentParser, validator: validator, tempDir: tempDir, limits: bytes.Clone(limits)}, nil
}

// Parse checkpoints validated output under the current job lease, without completing the job.
func (s *ParseStage) Parse(ctx context.Context, job jobs.ClaimedJob) (parser.Document, error) {
	ids, err := parseJob(job)
	if err != nil {
		return parser.Document{}, &jobs.ProcessingFailure{Code: jobs.Internal, Cause: err}
	}
	var version dbquery.LockIngestVersionRow
	var artifact []byte
	err = s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
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
			return fmt.Errorf("lock ingest version: %w", err)
		}
		if version.Status != "queued" && version.Status != "parsing" && version.Status != "embedding" && version.Status != "indexing" {
			return errTarget
		}
		stored, err := queries.LoadParseArtifact(ctx, artifactParams(job.Lease.TenantID, ids))
		if err == nil {
			if !bytes.Equal(stored.ContentSha256, version.ContentSha256) {
				return errCorruptOriginal
			}
			artifact = stored.ParsedDocument
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load parse checkpoint: %w", err)
		}
		if version.Status != "queued" && version.Status != "parsing" {
			return errTarget
		}
		updated, err := queries.MarkVersionParsing(ctx, dbquery.MarkVersionParsingParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		})
		if err != nil {
			return fmt.Errorf("mark parsing: %w", err)
		}
		if updated != 1 {
			return errTarget
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errTarget) {
			return parser.Document{}, &jobs.ProcessingFailure{Code: jobs.Internal, Cause: err}
		}
		if errors.Is(err, errCorruptOriginal) {
			return parser.Document{}, s.failure(job, ids, jobs.FileCorrupt, err)
		}
		return parser.Document{}, err
	}
	if len(artifact) != 0 {
		document, err := s.validator.Validate(artifact, version.ByteSize)
		if err != nil || len(document.Pages) == 0 {
			return parser.Document{}, s.failure(job, ids, jobs.ParseError, parser.ErrInvalidOutput)
		}
		if version.Status == "queued" || version.Status == "parsing" {
			if err := s.markEmbedding(ctx, job, ids, version.ContentSha256, document, nil); err != nil {
				return parser.Document{}, err
			}
		}
		return document, nil
	}
	document, err := s.parseOriginal(ctx, job, ids, version)
	if err != nil {
		if ctx.Err() != nil {
			return parser.Document{}, ctx.Err()
		}
		if errors.Is(err, errCleanup) || errors.Is(err, errInfrastructure) || errors.Is(err, parser.ErrSandboxUnavailable) {
			return parser.Document{}, err
		}
		code := jobs.Internal
		switch {
		case errors.Is(err, fs.ErrNotExist):
			code = jobs.FileMissing
		case errors.Is(err, errCorruptOriginal):
			code = jobs.FileCorrupt
		case errors.Is(err, parser.ErrUnsupportedFormat):
			code = jobs.Unsupported
		case errors.Is(err, parser.ErrEncrypted):
			code = jobs.Encrypted
		case errors.Is(err, parser.ErrLimitExceeded):
			code = jobs.LimitExceeded
		case errors.Is(err, parser.ErrParse), errors.Is(err, parser.ErrInvalidOutput):
			code = jobs.ParseError
		case errors.Is(err, parser.ErrTimeout):
			code = jobs.Timeout
		}
		return parser.Document{}, s.failure(job, ids, code, err)
	}
	if len(document.Pages) == 0 {
		return parser.Document{}, s.failure(job, ids, jobs.ParseError, parser.ErrInvalidOutput)
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return parser.Document{}, fmt.Errorf("encode parse checkpoint: %w", err)
	}
	if _, err := s.validator.Validate(buffer.Bytes(), version.ByteSize); err != nil {
		return parser.Document{}, s.failure(job, ids, jobs.ParseError, err)
	}
	if err := s.markEmbedding(ctx, job, ids, version.ContentSha256, document, buffer.Bytes()); err != nil {
		return parser.Document{}, err
	}
	return document, nil
}

func parseJob(job jobs.ClaimedJob) (documentIDs, error) {
	if job.Kind != jobs.Ingest {
		return documentIDs{}, errors.New("invalid ingest job")
	}
	return parseDocumentIDs(job.Payload)
}

func parseDocumentIDs(payload []byte) (documentIDs, error) {
	if len(payload) > 4096 {
		return documentIDs{}, errors.New("invalid document job payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var ids documentIDs
	if err := decoder.Decode(&ids); err != nil {
		return documentIDs{}, errors.New("invalid document job payload")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || ids.KBID <= 0 || ids.DocumentID <= 0 || ids.VersionID <= 0 {
		return documentIDs{}, errors.New("invalid document job payload")
	}
	return ids, nil
}

func (s *ParseStage) parseOriginal(ctx context.Context, job jobs.ClaimedJob, ids documentIDs, version dbquery.LockIngestVersionRow) (_ parser.Document, resultErr error) {
	key := storage.Key{TenantID: job.Lease.TenantID, KBID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}
	fileKey, err := key.Path()
	if err != nil || fileKey != version.FileKey {
		return parser.Document{}, errCorruptOriginal
	}
	original, err := s.files.Open(ctx, key)
	if errors.Is(err, fs.ErrNotExist) {
		return parser.Document{}, fmt.Errorf("open document original: %w", err)
	}
	if err != nil {
		return parser.Document{}, fmt.Errorf("%w: open document original: %w", errInfrastructure, err)
	}
	defer func() {
		if err := original.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: close document original: %w", errCleanup, err))
		}
	}()
	temporary, err := os.CreateTemp(s.tempDir, "tuskrag-parser-input-*")
	if err != nil {
		return parser.Document{}, fmt.Errorf("%w: create parser input: %w", errInfrastructure, err)
	}
	defer func() {
		if err := temporary.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: close input: %w", errCleanup, err))
		}
		if err := os.Remove(temporary.Name()); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w: remove input: %w", errCleanup, err))
		}
	}()
	if version.ByteSize <= 0 || version.ByteSize > parser.MaxInputBytes || len(version.ContentSha256) != sha256.Size {
		return parser.Document{}, errCorruptOriginal
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(original, version.ByteSize+1))
	if err != nil {
		return parser.Document{}, fmt.Errorf("%w: copy document original: %w", errInfrastructure, err)
	}
	if size != version.ByteSize || !bytes.Equal(hash.Sum(nil), version.ContentSha256) {
		return parser.Document{}, errCorruptOriginal
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return parser.Document{}, fmt.Errorf("%w: seek parser input: %w", errInfrastructure, err)
	}
	return s.parser.Parse(ctx, parser.ParseInput{File: temporary, MIME: version.Mime, Limits: s.limits})
}

func (s *ParseStage) markEmbedding(ctx context.Context, job jobs.ClaimedJob, ids documentIDs, sourceHash []byte, document parser.Document, artifact []byte) error {
	return s.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
			return err
		}
		version, err := queries.LockIngestVersion(ctx, lockParams(job.Lease.TenantID, ids))
		if err != nil {
			return fmt.Errorf("lock parsed version: %w", err)
		}
		if !bytes.Equal(version.ContentSha256, sourceHash) {
			return errCorruptOriginal
		}
		if artifact != nil {
			if err := queries.InsertParseArtifact(ctx, dbquery.InsertParseArtifactParams{
				TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
				ContentSha256: sourceHash, ParsedDocument: artifact,
			}); err != nil {
				return fmt.Errorf("save parse checkpoint: %w", err)
			}
		}
		updated, err := queries.MarkVersionEmbedding(ctx, dbquery.MarkVersionEmbeddingParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
			ParserProfile: pgtype.Text{String: document.Meta.ParserProfile, Valid: true}, PageCount: pgtype.Int4{Int32: int32(len(document.Pages)), Valid: true},
		})
		if err != nil {
			return fmt.Errorf("mark embedding: %w", err)
		}
		if updated != 1 {
			return errTarget
		}
		return nil
	})
}

func (s *ParseStage) failure(job jobs.ClaimedJob, ids documentIDs, code jobs.FailureCode, cause error) error {
	return &jobs.ProcessingFailure{Code: code, Cause: cause, Apply: func(ctx context.Context, tx jobs.EffectsTx, status jobs.Status) error {
		if err := s.queue.CheckFailure(tx, job.Lease, status); err != nil {
			return err
		}
		queries := dbquery.New(tx)
		if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
			return err
		}
		if _, err := queries.LockIngestVersion(ctx, lockParams(job.Lease.TenantID, ids)); err != nil {
			return fmt.Errorf("lock failed version: %w", err)
		}
		updated, err := queries.MarkVersionFailure(ctx, dbquery.MarkVersionFailureParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
			Retryable: status == jobs.Queued, FailureCode: pgtype.Text{String: string(code), Valid: true},
		})
		if err != nil {
			return fmt.Errorf("mark version failure: %w", err)
		}
		if updated != 1 {
			return errTarget
		}
		return nil
	}}
}

func verifyClaimedIngestJob(ctx context.Context, queries *dbquery.Queries, job jobs.ClaimedJob) error {
	if job.Kind != jobs.Ingest {
		return ErrJobMismatch
	}
	match, err := queries.ClaimedIngestJobMatches(ctx, dbquery.ClaimedIngestJobMatchesParams{
		TenantID: job.Lease.TenantID, JobID: job.Lease.JobID, Payload: job.Payload,
	})
	if err != nil {
		return fmt.Errorf("verify claimed ingest job: %w", err)
	}
	if !match {
		return ErrJobMismatch
	}
	return nil
}

// Expire synchronizes a document version when its ingest exhausts its lease attempts.
func (s *ParseStage) Expire(ctx context.Context, tx jobs.EffectsTx, job jobs.ExpiredJob) error {
	ids, err := parseJob(jobs.ClaimedJob{Kind: job.Kind, Payload: job.Payload})
	if err != nil || job.TenantID <= 0 {
		return errors.New("invalid exhausted ingest job")
	}
	updated, err := dbquery.New(tx).MarkVersionFailure(ctx, dbquery.MarkVersionFailureParams{
		TenantID: job.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		Retryable: false, FailureCode: pgtype.Text{String: string(jobs.LeaseExpired), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mark exhausted version failed: %w", err)
	}
	if updated != 1 {
		return errTarget
	}
	return nil
}

func lockParams(tenantID int64, ids documentIDs) dbquery.LockIngestVersionParams {
	return dbquery.LockIngestVersionParams{TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}
}

func artifactParams(tenantID int64, ids documentIDs) dbquery.LoadParseArtifactParams {
	return dbquery.LoadParseArtifactParams{TenantID: tenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}
}
