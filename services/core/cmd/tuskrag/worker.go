package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/config"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/ingest"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/secrets"
	"tuskrag.local/core/internal/storage"
)

type workerRuntime struct {
	worker *jobs.Worker
	files  *storage.Local
}

func newWorkerRuntime(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, count ingest.TokenCounter) (_ *workerRuntime, resultErr error) {
	if ctx == nil || pool == nil || count == nil {
		return nil, errors.New("context, database pool and token counter are required")
	}
	if err := os.MkdirAll(cfg.ParserTempDir, 0700); err != nil {
		return nil, fmt.Errorf("create parser temporary directory: %w", err)
	}
	validator, err := parser.NewValidator()
	if err != nil {
		return nil, err
	}
	nativeExecutable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve native parser executable: %w", err)
	}
	sandbox, err := parser.NewSandbox(parser.SandboxConfig{Bubblewrap: cfg.Bubblewrap, RuntimeDir: cfg.ParserRuntimeDir,
		Executable: cfg.Parser, NativeExecutable: nativeExecutable, TempDir: cfg.ParserTempDir, CgroupRoot: cfg.CgroupRoot,
		MemoryBytes: parser.MaxMemoryBytes, CPUSeconds: parser.MaxCPUSeconds, WallTime: parser.MaxWallTime})
	if err != nil {
		return nil, fmt.Errorf("initialize parser isolation: %w", err)
	}
	versionCtx, cancelVersion := context.WithTimeout(ctx, 10*time.Second)
	_, err = parser.ReadVersion(versionCtx, cfg.Parser)
	cancelVersion()
	if err != nil {
		return nil, fmt.Errorf("check parser version: %w", err)
	}
	implementation, err := parser.NewParser(sandbox, validator)
	if err != nil {
		return nil, err
	}
	var vault *secrets.Vault
	if cfg.MasterKeyFile != "" {
		vault, err = secrets.LoadVault(cfg.MasterKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load master key: %w", err)
		}
	}
	files, err := storage.NewLocal(ctx, cfg.StorageDirectory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			if err := files.Close(); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
	queue, err := jobs.NewStore(pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		return nil, err
	}
	parseStage, err := ingest.NewParseStage(queue, files, implementation, validator, cfg.ParserTempDir, json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	limits := models.ClientLimits{MaxBatch: 32, MaxRequestBytes: 4 * 1024 * 1024, MaxResponseBytes: 16 * 1024 * 1024,
		Timeout: 30 * time.Second, Concurrency: 1}
	embedding, err := ingest.NewEmbeddingStage(queue, pool, vault, count, limits, 32)
	if err != nil {
		return nil, err
	}
	processor, err := ingest.NewIngestHandler(queue, parseStage, embedding, ingest.DefaultIngestSettings())
	if err != nil {
		return nil, err
	}
	purge, err := ingest.NewPurgeStage(queue, files)
	if err != nil {
		return nil, err
	}
	router, err := ingest.NewJobHandler(processor, purge)
	if err != nil {
		return nil, err
	}
	runs, err := eval.NewRunStore(pool, queue)
	if err != nil {
		return nil, err
	}
	search, err := retrieval.NewService(pool, vault, retrieval.TokenCounter(count))
	if err != nil {
		return nil, err
	}
	evaluation, err := eval.NewRunHandler(runs, search)
	if err != nil {
		return nil, err
	}
	rebuild, err := ingest.NewRebuildEmbedding(queue, pool, vault, count, limits, 32)
	if err != nil {
		return nil, err
	}
	dispatch := workerHandlers{ingest: router, evaluation: evaluation, rebuild: rebuild}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate worker identity: %w", err)
	}
	worker, err := jobs.NewWorker(queue, dispatch, jobs.WorkerConfig{TenantIDs: cfg.WorkerTenants,
		Kinds: []jobs.Kind{jobs.Ingest, jobs.Purge, jobs.EvalRun, jobs.Reembed}, Owner: fmt.Sprintf("worker-%x", nonce[:]),
		LeaseTTL: 2 * time.Minute, Poll: 2 * time.Second, OnExpired: dispatch.Expire})
	if err != nil {
		return nil, err
	}
	return &workerRuntime{worker: worker, files: files}, nil
}

type workerHandlers struct {
	rebuild    *ingest.RebuildEmbedding
	ingest     *ingest.JobHandler
	evaluation *eval.RunHandler
}

func (h workerHandlers) Prepare(ctx context.Context, job jobs.ClaimedJob) (func(context.Context, jobs.EffectsTx) error, error) {
	if job.Kind == jobs.EvalRun {
		return h.evaluation.Prepare(ctx, job)
	}
	if job.Kind == jobs.Reembed {
		return h.rebuild.Prepare(ctx, job)
	}
	return h.ingest.Prepare(ctx, job)
}

func (h workerHandlers) Expire(ctx context.Context, tx jobs.EffectsTx, job jobs.ExpiredJob) error {
	// Evaluation has no mutable domain state until atomic successful completion.
	if job.Kind == jobs.EvalRun {
		return nil
	}
	if job.Kind == jobs.Reembed {
		return h.rebuild.Expire(ctx, tx, job)
	}
	return h.ingest.Expire(ctx, tx, job)
}

func (r *workerRuntime) Close() error {
	if r == nil || r.files == nil {
		return nil
	}
	return r.files.Close()
}
