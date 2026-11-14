package operations

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/secrets"
)

type JobStatusCount struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type DiagnosticReport struct {
	GeneratedAt     string           `json:"generated_at"`
	GoVersion       string           `json:"go_version"`
	ExpectedSchema  int64            `json:"expected_schema_version"`
	DatabaseSchema  int64            `json:"database_schema_version"`
	Extensions      []string         `json:"extensions"`
	LexicalDriver   string           `json:"lexical_driver"`
	TenantID        int64            `json:"tenant_id"`
	KnowledgeBases  int64            `json:"knowledge_bases"`
	Documents       int64            `json:"documents"`
	Jobs            []JobStatusCount `json:"jobs"`
}

type KeyRotationReport struct {
	TenantID              int64  `json:"tenant_id"`
	NewKeyID              string `json:"new_key_id"`
	ModelProvidersRotated int    `json:"model_providers_rotated"`
	OIDCProvidersRotated  int    `json:"oidc_providers_rotated"`
}

type ReindexReport struct {
	KnowledgeBaseID int64 `json:"knowledge_base_id"`
	TenantID        int64 `json:"tenant_id"`
	SpaceID         int64 `json:"embedding_space_id"`
	Analyzed        bool  `json:"analyzed"`
}

// Diagnose collects a sanitized operational snapshot without any document text,
// prompts, or credentials.
func Diagnose(ctx context.Context, pool *pgxpool.Pool, tenantID int64) (DiagnosticReport, error) {
	if pool == nil || tenantID <= 0 {
		return DiagnosticReport{}, errors.New("database pool and positive tenant ID are required")
	}
	report := DiagnosticReport{
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		GoVersion:      runtime.Version(),
		ExpectedSchema: db.SchemaVersion,
		TenantID:       tenantID,
		LexicalDriver:  "compat_tsvector",
	}
	extRows, err := pool.Query(ctx, `SELECT extname FROM pg_extension WHERE extname IN ('vector', 'pg_search') ORDER BY extname`)
	if err != nil {
		return DiagnosticReport{}, fmt.Errorf("query extensions: %w", err)
	}
	for extRows.Next() {
		var name string
		if scanErr := extRows.Scan(&name); scanErr != nil {
			extRows.Close()
			return DiagnosticReport{}, scanErr
		}
		report.Extensions = append(report.Extensions, name)
		if name == "pg_search" {
			report.LexicalDriver = "pg_search"
		}
	}
	extRows.Close()
	if err := extRows.Err(); err != nil {
		return DiagnosticReport{}, err
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&report.DatabaseSchema); err != nil {
		return DiagnosticReport{}, fmt.Errorf("query schema version: %w", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return DiagnosticReport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := dbquery.New(tx).SetTenant(ctx, tenantID); err != nil {
		return DiagnosticReport{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM knowledge_base`).Scan(&report.KnowledgeBases); err != nil {
		return DiagnosticReport{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM document`).Scan(&report.Documents); err != nil {
		return DiagnosticReport{}, err
	}
	jobRows, err := tx.Query(ctx, `SELECT kind, status, count(*) FROM job GROUP BY kind, status ORDER BY kind, status`)
	if err != nil {
		return DiagnosticReport{}, err
	}
	for jobRows.Next() {
		var item JobStatusCount
		if scanErr := jobRows.Scan(&item.Kind, &item.Status, &item.Count); scanErr != nil {
			jobRows.Close()
			return DiagnosticReport{}, scanErr
		}
		report.Jobs = append(report.Jobs, item)
	}
	jobRows.Close()
	if err := jobRows.Err(); err != nil {
		return DiagnosticReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DiagnosticReport{}, err
	}
	return report, nil
}

// RotateMasterKey re-encrypts model_provider and oidc_provider secrets for a
// tenant from oldVault to newVault in a single transaction.
func RotateMasterKey(ctx context.Context, pool *pgxpool.Pool, tenantID int64, oldVault, newVault *secrets.Vault) (KeyRotationReport, error) {
	if pool == nil || tenantID <= 0 || oldVault == nil || newVault == nil {
		return KeyRotationReport{}, errors.New("database pool, positive tenant ID, old vault and new vault are required")
	}
	report := KeyRotationReport{
		TenantID: tenantID,
		NewKeyID: newVault.KeyID(),
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return report, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := dbquery.New(tx).SetTenant(ctx, tenantID); err != nil {
		return report, err
	}
	type encryptedRow struct {
		id         int64
		keyID      string
		nonce      []byte
		ciphertext []byte
	}
	readRows := func(query string) ([]encryptedRow, error) {
		rows, qErr := tx.Query(ctx, query)
		if qErr != nil {
			return nil, qErr
		}
		defer rows.Close()
		var out []encryptedRow
		for rows.Next() {
			var r encryptedRow
			if sErr := rows.Scan(&r.id, &r.keyID, &r.nonce, &r.ciphertext); sErr != nil {
				return nil, sErr
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}

	modelRows, err := readRows(`SELECT id, api_key_id, api_key_nonce, api_key_ciphertext FROM model_provider WHERE api_key_ciphertext IS NOT NULL ORDER BY id FOR UPDATE`)
	if err != nil {
		return report, fmt.Errorf("load model_provider secrets: %w", err)
	}
	for _, row := range modelRows {
		if row.keyID == newVault.KeyID() {
			continue
		}
		plain, oErr := oldVault.Open(ctx, tenantID, row.id, secrets.Encrypted{
			KeyID:      row.keyID,
			Nonce:      row.nonce,
			Ciphertext: row.ciphertext,
		})
		if oErr != nil {
			return report, fmt.Errorf("decrypt model_provider %d: %w", row.id, oErr)
		}
		sealed, sErr := newVault.Seal(ctx, tenantID, row.id, plain)
		clear(plain)
		if sErr != nil {
			return report, fmt.Errorf("re-encrypt model_provider %d: %w", row.id, sErr)
		}
		if _, uErr := tx.Exec(ctx, `UPDATE model_provider SET api_key_id = $1, api_key_nonce = $2, api_key_ciphertext = $3 WHERE id = $4`, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, row.id); uErr != nil {
			return report, uErr
		}
		report.ModelProvidersRotated++
	}

	oidcRows, err := readRows(`SELECT id, client_secret_id, client_secret_nonce, client_secret_ciphertext FROM oidc_provider WHERE client_secret_ciphertext IS NOT NULL ORDER BY id FOR UPDATE`)
	if err != nil {
		return report, fmt.Errorf("load oidc_provider secrets: %w", err)
	}
	for _, row := range oidcRows {
		if row.keyID == newVault.KeyID() {
			continue
		}
		plain, oErr := oldVault.Open(ctx, tenantID, row.id, secrets.Encrypted{
			KeyID:      row.keyID,
			Nonce:      row.nonce,
			Ciphertext: row.ciphertext,
		})
		if oErr != nil {
			return report, fmt.Errorf("decrypt oidc_provider %d: %w", row.id, oErr)
		}
		sealed, sErr := newVault.Seal(ctx, tenantID, row.id, plain)
		clear(plain)
		if sErr != nil {
			return report, fmt.Errorf("re-encrypt oidc_provider %d: %w", row.id, sErr)
		}
		if _, uErr := tx.Exec(ctx, `UPDATE oidc_provider SET client_secret_id = $1, client_secret_nonce = $2, client_secret_ciphertext = $3 WHERE id = $4`, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, row.id); uErr != nil {
			return report, uErr
		}
		report.OIDCProvidersRotated++
	}

	if err := tx.Commit(ctx); err != nil {
		return report, err
	}
	return report, nil
}

// ReindexKB verifies or provisions the shared lexical index and the
// knowledge base's HNSW vector index, then refreshes planner statistics.
func ReindexKB(ctx context.Context, databaseURL string, tenantID, kbID int64) (ReindexReport, error) {
	if ctx == nil || databaseURL == "" || tenantID <= 0 || kbID <= 0 {
		return ReindexReport{}, errors.New("context, database URL, positive tenant ID, and positive knowledge base ID are required")
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return ReindexReport{}, fmt.Errorf("connect reindex: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return ReindexReport{}, err
	}
	if _, err := dbquery.New(tx).SetTenant(ctx, tenantID); err != nil {
		_ = tx.Rollback(ctx)
		return ReindexReport{}, err
	}
	var spaceID int64
	if err := tx.QueryRow(ctx, `SELECT embedding_space_id FROM knowledge_base WHERE tenant_id = $1 AND id = $2`, tenantID, kbID).Scan(&spaceID); err != nil {
		_ = tx.Rollback(ctx)
		return ReindexReport{}, fmt.Errorf("load knowledge base %d: %w", kbID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ReindexReport{}, err
	}
	if err := indexing.Provision(ctx, databaseURL, tenantID, spaceID); err != nil {
		return ReindexReport{}, err
	}
	if _, err := conn.Exec(ctx, `ANALYZE chunk`); err != nil {
		return ReindexReport{}, fmt.Errorf("analyze chunk: %w", err)
	}
	if _, err := conn.Exec(ctx, `ANALYZE chunk_vector`); err != nil {
		return ReindexReport{}, fmt.Errorf("analyze chunk_vector: %w", err)
	}
	return ReindexReport{
		KnowledgeBaseID: kbID,
		TenantID:        tenantID,
		SpaceID:         spaceID,
		Analyzed:        true,
	}, nil
}
