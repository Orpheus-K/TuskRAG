package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/secrets"
)

var (
	ErrForbidden            = errors.New("model configuration requires a local administrator")
	ErrProviderNameUsed     = errors.New("model provider name already exists")
	ErrInvalidConfiguration = errors.New("invalid model configuration")
)

type Space struct {
	ID            int64
	ProviderID    int64
	ProviderName  string
	BaseURL       string
	Locality      Locality
	Model         string
	Dimensions    int32
	Normalized    bool
	Distance      string
	HasCredential bool
}

type NewSpace struct {
	ProviderName string
	BaseURL      string
	Locality     Locality
	APIKey       []byte
	Model        string
	Dimensions   int32
	Normalized   bool
	Distance     string
}

type Catalog struct {
	pool  *pgxpool.Pool
	vault *secrets.Vault
}

func NewCatalog(pool *pgxpool.Pool, vault *secrets.Vault) (*Catalog, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &Catalog{pool: pool, vault: vault}, nil
}

func (c *Catalog) CreateSpace(ctx context.Context, tenantID, userID int64, request NewSpace) (_ Space, resultErr error) {
	request.ProviderName = strings.TrimSpace(request.ProviderName)
	request.Model = strings.TrimSpace(request.Model)
	if !validCatalogText(request.ProviderName, 200) || !validCatalogText(request.Model, 200) ||
		(request.Locality != Internal && request.Locality != External) || request.Dimensions < 64 || request.Dimensions > 4000 ||
		(request.Distance != "cosine" && request.Distance != "ip") || len(request.APIKey) > 4096 || bytes.ContainsAny(request.APIKey, "\r\n") {
		return Space{}, ErrInvalidConfiguration
	}
	base, err := providerURL(request.BaseURL)
	if err != nil {
		return Space{}, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
	}
	if request.Locality == External && base.Scheme != "https" {
		return Space{}, fmt.Errorf("%w: external model provider requires HTTPS", ErrInvalidConfiguration)
	}
	tx, queries, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return Space{}, err
	}
	defer rollbackCatalog(tx, &resultErr)
	if err := requireAdministrator(ctx, queries, tenantID, userID); errors.Is(err, ErrForbidden) {
		return Space{}, auditCatalogResult(ctx, tx, queries, tenantID, userID, 0, "model.create", "deny", ErrForbidden)
	} else if err != nil {
		return Space{}, err
	}
	if len(request.APIKey) > 0 && c.vault == nil {
		return Space{}, auditCatalogResult(ctx, tx, queries, tenantID, userID, 0, "model.create", "error", secrets.ErrKeyUnavailable)
	}
	providerID, err := queries.CreateModelProvider(ctx, dbquery.CreateModelProviderParams{
		TenantID: tenantID, Name: request.ProviderName, BaseUrl: request.BaseURL, Locality: string(request.Locality),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "model_provider_tenant_id_name_key" {
			return Space{}, ErrProviderNameUsed
		}
		return Space{}, fmt.Errorf("create model provider: %w", err)
	}
	if len(request.APIKey) > 0 {
		encrypted, err := c.vault.Seal(ctx, tenantID, providerID, request.APIKey)
		if err != nil {
			return Space{}, fmt.Errorf("encrypt model credential: %w", err)
		}
		if err := queries.SetModelProviderCredential(ctx, dbquery.SetModelProviderCredentialParams{
			TenantID: tenantID, ProviderID: providerID, KeyID: pgtype.Text{String: encrypted.KeyID, Valid: true},
			Nonce: encrypted.Nonce, Ciphertext: encrypted.Ciphertext,
		}); err != nil {
			return Space{}, fmt.Errorf("store model credential: %w", err)
		}
	}
	spaceID, err := queries.CreateEmbeddingSpace(ctx, dbquery.CreateEmbeddingSpaceParams{
		TenantID: tenantID, ProviderID: providerID, Model: request.Model, Dims: request.Dimensions,
		Normalized: request.Normalized, Distance: request.Distance,
	})
	if err != nil {
		return Space{}, fmt.Errorf("create embedding space: %w", err)
	}
	if err := auditCatalog(ctx, queries, tenantID, userID, providerID, "model.create", "allow"); err != nil {
		return Space{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Space{}, fmt.Errorf("commit model configuration: %w", err)
	}
	return Space{ID: spaceID, ProviderID: providerID, ProviderName: request.ProviderName, BaseURL: request.BaseURL,
		Locality: request.Locality, Model: request.Model, Dimensions: request.Dimensions, Normalized: request.Normalized,
		Distance: request.Distance, HasCredential: len(request.APIKey) > 0}, nil
}

func (c *Catalog) ListSpaces(ctx context.Context, tenantID, userID int64) (_ []Space, resultErr error) {
	tx, queries, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rollbackCatalog(tx, &resultErr)
	if err := requireAdministrator(ctx, queries, tenantID, userID); errors.Is(err, ErrForbidden) {
		return nil, auditCatalogResult(ctx, tx, queries, tenantID, userID, 0, "model.list", "deny", ErrForbidden)
	} else if err != nil {
		return nil, err
	}
	rows, err := queries.ListEmbeddingSpaces(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list embedding spaces: %w", err)
	}
	spaces := make([]Space, 0, len(rows))
	for _, row := range rows {
		spaces = append(spaces, Space{ID: row.ID, ProviderID: row.ProviderID, ProviderName: row.ProviderName,
			BaseURL: row.BaseUrl, Locality: Locality(row.Locality), Model: row.Model, Dimensions: row.Dims,
			Normalized: row.Normalized, Distance: row.Distance, HasCredential: row.HasCredential})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit model listing: %w", err)
	}
	return spaces, nil
}

func (c *Catalog) begin(ctx context.Context, tenantID, userID int64) (pgx.Tx, *dbquery.Queries, error) {
	if tenantID <= 0 || userID <= 0 {
		return nil, nil, errors.New("valid tenant and user are required")
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin model configuration transaction: %w", err)
	}
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, tenantID); err != nil {
		result := fmt.Errorf("set model configuration tenant: %w", err)
		rollbackCatalog(tx, &result)
		return nil, nil, result
	}
	return tx, queries, nil
}

func rollbackCatalog(tx pgx.Tx, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*result = errors.Join(*result, fmt.Errorf("rollback model configuration transaction: %w", err))
	}
}

func requireAdministrator(ctx context.Context, queries *dbquery.Queries, tenantID, userID int64) error {
	if _, err := queries.LockLocalAdministrator(ctx, dbquery.LockLocalAdministratorParams{TenantID: tenantID, UserID: userID}); errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	} else if err != nil {
		return fmt.Errorf("check model administrator: %w", err)
	}
	return nil
}

func auditCatalog(ctx context.Context, queries *dbquery.Queries, tenantID, userID, providerID int64, action, result string) error {
	if err := queries.AuditModelAction(ctx, dbquery.AuditModelActionParams{
		TenantID: tenantID, UserID: userID, ProviderID: providerID, Action: action, Result: result,
	}); err != nil {
		return fmt.Errorf("audit model configuration: %w", err)
	}
	return nil
}

func auditCatalogResult(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, tenantID, userID, providerID int64, action, result string, outcome error) error {
	if err := auditCatalog(ctx, queries, tenantID, userID, providerID, action, result); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit model configuration audit: %w", err)
	}
	return outcome
}

func validCatalogText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
