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
	"tuskrag.local/core/internal/ingest"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
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
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate worker identity: %w", err)
	}
	worker, err := jobs.NewWorker(queue, router, jobs.WorkerConfig{TenantIDs: cfg.WorkerTenants,
		Kinds: []jobs.Kind{jobs.Ingest, jobs.Purge}, Owner: fmt.Sprintf("worker-%x", nonce[:]),
		LeaseTTL: 2 * time.Minute, Poll: 2 * time.Second, OnExpired: router.Expire})
	if err != nil {
		return nil, err
	}
	return &workerRuntime{worker: worker, files: files}, nil
}

func (r *workerRuntime) Close() error {
	if r == nil || r.files == nil {
		return nil
	}
	return r.files.Close()
}
