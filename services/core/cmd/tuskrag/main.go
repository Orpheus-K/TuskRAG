package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/answer"
	"tuskrag.local/core/internal/config"
	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/httpapi"
	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/mcpserver"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/secrets"
	"tuskrag.local/core/internal/storage"
	"tuskrag.local/core/internal/tokenizer"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "native-parse" {
		os.Exit(nativeParseExitCode(runNativeParse(os.Args[2:])))
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("tuskrag stopped", "error", err)
		os.Exit(1)
	}
}

func run() (resultErr error) {
	if len(os.Args) >= 2 && os.Args[1] == "ops" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runOperations(ctx, os.Args[2:])
	}
	if len(os.Args) >= 2 && os.Args[1] == "eval" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runEval(ctx, os.Args[2:])
	}
	if len(os.Args) == 2 && os.Args[1] == "openapi" {
		doc, err := httpapi.OpenAPIJSON()
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(doc)
		return err
	}
	if len(os.Args) == 2 && os.Args[1] == "mcp-tools" {
		doc, err := mcpserver.ToolsJSON()
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(doc)
		return err
	}
	if len(os.Args) == 2 && os.Args[1] == "bootstrap" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return bootstrap(ctx)
	}
	if len(os.Args) >= 2 && os.Args[1] == "provision-indexes" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return provisionIndexes(ctx, os.Args[2:])
	}
	if len(os.Args) != 1 {
		return errors.New("usage: tuskrag [openapi|bootstrap|provision-indexes <tenant-id> <space-id>|eval ...|ops verify-storage <tenant-id>]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.Role == "migrate" {
		return db.Migrate(ctx, cfg.DatabaseURL)
	}
	startupCtx, startupCancel := context.WithTimeout(ctx, 10*time.Second)
	var pool *pgxpool.Pool
	if cfg.Role == "api" {
		pool, err = db.Open(startupCtx, cfg.DatabaseURL)
	} else {
		pool, err = db.OpenWithLimit(startupCtx, cfg.DatabaseURL, 4)
	}
	startupCancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	counter, err := tokenizer.NewBGEM3(ctx, 2)
	if err != nil {
		return err
	}
	var worker *workerRuntime
	if cfg.Role == "worker" || cfg.Role == "all" {
		worker, err = newWorkerRuntime(ctx, cfg, pool, counter.Count)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, worker.Close()) }()
	}
	if cfg.Role == "worker" {
		return runWorker(ctx, worker.worker.Run)
	}
	hasher, err := identity.NewPasswordHasher(1)
	if err != nil {
		return err
	}
	identities, err := identity.NewService(pool, hasher, identity.SessionConfig{IdleTimeout: 8 * time.Hour, AbsoluteTimeout: 7 * 24 * time.Hour})
	if err != nil {
		return err
	}
	knowledgeBases, err := kb.NewService(pool)
	if err != nil {
		return err
	}
	var documents httpapi.DocumentService
	var jobControl httpapi.JobControl
	if cfg.StorageDirectory != "" {
		files, err := storage.NewLocal(ctx, cfg.StorageDirectory)
		if err != nil {
			return err
		}
		defer func() {
			if err := files.Close(); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}()
		queue, err := jobs.NewStore(pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
		if err != nil {
			return err
		}
		documentService, err := kb.NewDocumentService(knowledgeBases, files, queue)
		if err != nil {
			return err
		}
		documents, jobControl = documentService, documentService
	}
	var vault *secrets.Vault
	if cfg.MasterKeyFile != "" {
		vault, err = secrets.LoadVault(cfg.MasterKeyFile)
		if err != nil {
			return fmt.Errorf("load master key: %w", err)
		}
		identities.SetVault(vault)
	}
	catalog, err := models.NewCatalog(pool, vault)
	if err != nil {
		return err
	}
	lexicalCtx, lexicalCancel := context.WithTimeout(ctx, 5*time.Second)
	search, lexicalStatus, err := retrieval.NewServiceWithMode(lexicalCtx, pool, vault, counter.Count, cfg.LexicalDriver)
	lexicalCancel()
	if err != nil {
		return err
	}
	if lexicalStatus.Driver == "compat" {
		slog.Warn("using compatible lexical driver (pg_search unavailable)", "driver", lexicalStatus.Driver, "degraded", lexicalStatus.Degraded)
	} else {
		slog.Info("lexical driver ready", "driver", lexicalStatus.Driver)
	}
	answers, err := answer.NewService(pool, vault, search, counter.Count)
	if err != nil {
		return err
	}
	reports, err := eval.NewReportStore(pool)
	if err != nil {
		return err
	}
	answerReports, err := eval.NewAnswerReportStore(pool)
	if err != nil {
		return err
	}
	evalSets, err := eval.NewSetStore(pool)
	if err != nil {
		return err
	}
	evalQueue, err := jobs.NewStore(pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		return err
	}
	evalRuns, err := eval.NewRunStore(pool, evalQueue)
	if err != nil {
		return err
	}
	rebuilds, err := kb.NewRebuildService(pool, evalQueue)
	if err != nil {
		return err
	}
	conversations, err := answer.NewConversations(pool, answers)
	if err != nil {
		return err
	}
	app := httpapi.New(httpapi.Dependencies{
		Answers: answers, ChatModels: catalog, Conversations: conversations,
		Rebuilds:      rebuilds,
		Reports:       reports,
		AnswerReports: answerReports,
		EvalSets:      evalSets,
		EvalRuns: evalRuns,
		Identity: identities, Knowledge: knowledgeBases, Config: knowledgeBases, Documents: documents, Jobs: jobControl, Reader: knowledgeBases, Models: catalog, Search: search, InsecureLocalCookies: cfg.InsecureLocalCookies,
		Check: func(ctx context.Context) error {
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return db.Check(checkCtx, pool)
		},
	})
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           app.Handler,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	var runJobs func(context.Context) error
	if worker != nil {
		runJobs = worker.worker.Run
	}
	return serve(ctx, server, runJobs)
}

// Follow single-error wrappers only. Joined errors may contain a cleanup
// failure alongside cancellation and must remain visible.
func workerCancelled(err error) bool {
	for err != nil {
		if err == context.Canceled {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func runWorker(ctx context.Context, run func(context.Context) error) error {
	err := run(ctx)
	if workerCancelled(err) && ctx.Err() != nil {
		return nil
	}
	return err
}

func serve(ctx context.Context, server *http.Server, runJobs func(context.Context) error) error {
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	var workerDone chan error
	if runJobs != nil {
		workerDone = make(chan error, 1)
		go func() { workerDone <- runJobs(workerCtx) }()
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	var result error
	serverStopped, workerStopped := false, false
	select {
	case <-ctx.Done():
	case err := <-serverDone:
		serverStopped = true
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve HTTP: %w", err)
		}
	case err := <-workerDone:
		workerStopped = true
		if err == nil {
			result = errors.New("worker stopped unexpectedly")
		} else if !workerCancelled(err) || ctx.Err() == nil {
			result = fmt.Errorf("worker stopped: %w", err)
		}
	}
	cancelWorker()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !serverStopped {
		if err := server.Shutdown(shutdownCtx); err != nil {
			result = errors.Join(result, fmt.Errorf("shut down HTTP server: %w", err), server.Close())
		}
	}
	if workerDone != nil && !workerStopped {
		select {
		case err := <-workerDone:
			if err != nil && !workerCancelled(err) {
				result = errors.Join(result, fmt.Errorf("stop worker: %w", err))
			}
		case <-shutdownCtx.Done():
			result = errors.Join(result, errors.New("worker did not stop before shutdown deadline"))
		}
	}
	return result
}
