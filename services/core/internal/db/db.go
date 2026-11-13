package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

const SchemaVersion int64 = 29

func Migrate(ctx context.Context, databaseURL string) (result error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("invalid migration database configuration")
	}
	database := sql.OpenDB(stdlib.GetConnector(*config))
	defer func() {
		if err := database.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close migration database: %w", err))
		}
	}()
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.UpContext(ctx, database, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return open(ctx, databaseURL, 0)
}

// OpenWithLimit reserves one additional connection for a worker listener.
func OpenWithLimit(ctx context.Context, databaseURL string, maxConnections int32) (*pgxpool.Pool, error) {
	if maxConnections <= 0 {
		return nil, errors.New("positive database pool limit is required")
	}
	return open(ctx, databaseURL, maxConnections)
}

func open(ctx context.Context, databaseURL string, maxConnections int32) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	// pg_search 0.22.6 can retain a tenant-specific RLS term in a cached
	// generic plan. Every application connection must use custom plans.
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"
	if maxConnections > 0 {
		if config.MinConns > maxConnections {
			return nil, errors.New("database pool minimum exceeds worker limit")
		}
		config.MaxConns = maxConnections
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := Check(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func Check(ctx context.Context, pool *pgxpool.Pool) error {
	var major int
	var superuser, bypassRLS bool
	var vector, search, migrated, isolated, customPlan bool
	err := pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000,
		rolsuper, rolbypassrls, current_setting('plan_cache_mode') = 'force_custom_plan',
		EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector' AND string_to_array(extversion, '.')::int[] >= ARRAY[0,8,0]),
		EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_search'),
		(SELECT version_id = $1 AND is_applied FROM goose_db_version ORDER BY id DESC LIMIT 1),
		(SELECT count(*) = 29 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relname = ANY(ARRAY['tenant','app_user','model_provider','embedding_space','knowledge_base','job','kb_membership','audit_log','local_account','login_session','idempotency_record','document','document_version','parse_artifact','section','chunk','chunk_vector','ingest_plan','kb_retrieval_config_revision','eval_report','eval_set','eval_set_revision','eval_run','eval_run_result','embedding_rebuild','chat_model','conversation','answer_message','answer_feedback'])
		 AND c.relrowsecurity AND c.relforcerowsecurity AND pg_get_userbyid(c.relowner) <> current_user)
		FROM pg_roles WHERE rolname = current_user`, SchemaVersion).Scan(&major, &superuser, &bypassRLS, &customPlan, &vector, &search, &migrated, &isolated)
	if err != nil {
		return fmt.Errorf("check database prerequisites: %w", err)
	}
	if major != 17 || superuser || bypassRLS || !isolated || !vector || !search || !migrated || !customPlan {
		return fmt.Errorf("database prerequisites unmet (PG 17: %t, non-superuser: %t, RLS enforced: %t, custom plans: %t, vector >= 0.8: %t, pg_search: %t, schema: %t)", major == 17, !superuser, !bypassRLS && isolated, customPlan, vector, search, migrated)
	}
	return CheckVersionGuards(ctx, pool)
}
