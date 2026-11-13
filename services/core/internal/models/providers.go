package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/secrets"
)

var (
	ErrProviderNotFound = errors.New("model provider not found")
	ErrProviderInUse    = errors.New("model provider is still referenced by embedding spaces or chat models")
)

type ProviderRecord struct {
	ID            int64
	Name          string
	BaseURL       string
	Locality      Locality
	HasCredential bool
	CreatedAt     time.Time
}

type ProviderInput struct {
	Name     string
	BaseURL  string
	Locality Locality
	APIKey   []byte
}

type ProviderConnectivityResult struct {
	Reachable  bool
	StatusCode int
	LatencyMS  int64
	Message    string
}

func (c *Catalog) ListProviders(ctx context.Context, tenantID, userID int64) (_ []ProviderRecord, resultErr error) {
	tx, queries, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rollbackCatalog(tx, &resultErr)
	if err := requireAdministrator(ctx, queries, tenantID, userID); errors.Is(err, ErrForbidden) {
		return nil, auditCatalogResult(ctx, tx, queries, tenantID, userID, 0, "model_provider.list", "deny", ErrForbidden)
	} else if err != nil {
		return nil, err
	}
	rows, err := queries.ListModelProviders(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list model providers: %w", err)
	}
	out := make([]ProviderRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProviderRecord{
			ID:            r.ID,
			Name:          r.Name,
			BaseURL:       r.BaseUrl,
			Locality:      Locality(r.Locality),
			HasCredential: r.HasCredential,
			CreatedAt:     r.CreatedAt.Time.UTC(),
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit list model providers: %w", err)
	}
	return out, nil
}

func (c *Catalog) CreateProvider(ctx context.Context, tenantID, userID int64, req ProviderInput) (_ ProviderRecord, resultErr error) {
	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if !validCatalogText(req.Name, 200) || (req.Locality != Internal && req.Locality != External) ||
		len(req.APIKey) > 4096 || bytes.ContainsAny(req.APIKey, "\r\n") {
		return ProviderRecord{}, ErrInvalidConfiguration
	}
	base, err := providerURL(req.BaseURL)
	if err != nil {
		return ProviderRecord{}, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
	}
	if req.Locality == External && base.Scheme != "https" {
		return ProviderRecord{}, fmt.Errorf("%w: external model provider requires HTTPS", ErrInvalidConfiguration)
	}
	tx, queries, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return ProviderRecord{}, err
	}
	defer rollbackCatalog(tx, &resultErr)
	if err := requireAdministrator(ctx, queries, tenantID, userID); errors.Is(err, ErrForbidden) {
		return ProviderRecord{}, auditCatalogResult(ctx, tx, queries, tenantID, userID, 0, "model_provider.create", "deny", ErrForbidden)
	} else if err != nil {
		return ProviderRecord{}, err
	}
	if len(req.APIKey) > 0 && c.vault == nil {
		return ProviderRecord{}, auditCatalogResult(ctx, tx, queries, tenantID, userID, 0, "model_provider.create", "error", secrets.ErrKeyUnavailable)
	}
	providerID, err := queries.CreateModelProvider(ctx, dbquery.CreateModelProviderParams{
		TenantID: tenantID,
		Name:     req.Name,
		BaseUrl:  req.BaseURL,
		Locality: string(req.Locality),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "model_provider_tenant_id_name_key" {
			return ProviderRecord{}, ErrProviderNameUsed
		}
		return ProviderRecord{}, fmt.Errorf("create model provider: %w", err)
	}
	if len(req.APIKey) > 0 {
		encrypted, err := c.vault.Seal(ctx, tenantID, providerID, req.APIKey)
		if err != nil {
			return ProviderRecord{}, fmt.Errorf("encrypt provider credential: %w", err)
		}
		if err := queries.SetModelProviderCredential(ctx, dbquery.SetModelProviderCredentialParams{
			TenantID:   tenantID,
			ProviderID: providerID,
			KeyID:      pgtype.Text{String: encrypted.KeyID, Valid: true},
			Nonce:      encrypted.Nonce,
			Ciphertext: encrypted.Ciphertext,
		}); err != nil {
			return ProviderRecord{}, fmt.Errorf("store provider credential: %w", err)
		}
	}
	if err := auditCatalog(ctx, queries, tenantID, userID, providerID, "model_provider.create", "allow"); err != nil {
		return ProviderRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderRecord{}, fmt.Errorf("commit create model provider: %w", err)
	}
	return ProviderRecord{
		ID:            providerID,
		Name:          req.Name,
		BaseURL:       req.BaseURL,
		Locality:      req.Locality,
		HasCredential: len(req.APIKey) > 0,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

func (c *Catalog) UpdateProvider(ctx context.Context, tenantID, userID, providerID int64, req ProviderInput) (_ ProviderRecord, resultErr error) {
	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if providerID <= 0 || !validCatalogText(req.Name, 200) || (req.Locality != Internal && req.Locality != External) ||
		len(req.APIKey) > 4096 || bytes.ContainsAny(req.APIKey, "\r\n") {
		return ProviderRecord{}, ErrInvalidConfiguration
	}
	base, err := providerURL(req.BaseURL)
	if err != nil {
		return ProviderRecord{}, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
	}
	if req.Locality == External && base.Scheme != "https" {
		return ProviderRecord{}, fmt.Errorf("%w: external model provider requires HTTPS", ErrInvalidConfiguration)
	}
	tx, queries, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return ProviderRecord{}, err
	}
	defer rollbackCatalog(tx, &resultErr)
	if err := requireAdministrator(ctx, queries, tenantID, userID); errors.Is(err, ErrForbidden) {
		return ProviderRecord{}, auditCatalogResult(ctx, tx, queries, tenantID, userID, providerID, "model_provider.update", "deny", ErrForbidden)
	} else if err != nil {
		return ProviderRecord{}, err
	}
	if len(req.APIKey) > 0 && c.vault == nil {
		return ProviderRecord{}, auditCatalogResult(ctx, tx, queries, tenantID, userID, providerID, "model_provider.update", "error", secrets.ErrKeyUnavailable)
	}
	existing, err := queries.GetModelProvider(ctx, dbquery.GetModelProviderParams{
		TenantID:   tenantID,
		ProviderID: providerID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderRecord{}, ErrProviderNotFound
	} else if err != nil {
		return ProviderRecord{}, fmt.Errorf("load model provider: %w", err)
	}
	if _, err := queries.UpdateModelProvider(ctx, dbquery.UpdateModelProviderParams{
		TenantID:   tenantID,
		ProviderID: providerID,
		Name:       req.Name,
		BaseUrl:    req.BaseURL,
		Locality:   string(req.Locality),
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "model_provider_tenant_id_name_key" {
			return ProviderRecord{}, ErrProviderNameUsed
		}
		return ProviderRecord{}, fmt.Errorf("update model provider: %w", err)
	}
	hasCred := len(existing.ApiKeyCiphertext) > 0
	if len(req.APIKey) > 0 {
		encrypted, err := c.vault.Seal(ctx, tenantID, providerID, req.APIKey)
		if err != nil {
			return ProviderRecord{}, fmt.Errorf("encrypt provider credential: %w", err)
		}
		if err := queries.SetModelProviderCredential(ctx, dbquery.SetModelProviderCredentialParams{
			TenantID:   tenantID,
			ProviderID: providerID,
			KeyID:      pgtype.Text{String: encrypted.KeyID, Valid: true},
			Nonce:      encrypted.Nonce,
			Ciphertext: encrypted.Ciphertext,
		}); err != nil {
			return ProviderRecord{}, fmt.Errorf("store provider credential: %w", err)
		}
		hasCred = true
	}
	if err := auditCatalog(ctx, queries, tenantID, userID, providerID, "model_provider.update", "allow"); err != nil {
		return ProviderRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderRecord{}, fmt.Errorf("commit update model provider: %w", err)
	}
	return ProviderRecord{
		ID:            providerID,
		Name:          req.Name,
		BaseURL:       req.BaseURL,
		Locality:      req.Locality,
		HasCredential: hasCred,
		CreatedAt:     existing.CreatedAt.Time.UTC(),
	}, nil
}

func (c *Catalog) DeleteProvider(ctx context.Context, tenantID, userID, providerID int64) (resultErr error) {
	if providerID <= 0 {
		return ErrInvalidConfiguration
	}
	tx, queries, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return err
	}
	defer rollbackCatalog(tx, &resultErr)
	if err := requireAdministrator(ctx, queries, tenantID, userID); errors.Is(err, ErrForbidden) {
		return auditCatalogResult(ctx, tx, queries, tenantID, userID, providerID, "model_provider.delete", "deny", ErrForbidden)
	} else if err != nil {
		return err
	}
	refs, err := queries.CountModelProviderReferences(ctx, dbquery.CountModelProviderReferencesParams{
		TenantID:   tenantID,
		ProviderID: providerID,
	})
	if err != nil {
		return fmt.Errorf("count provider references: %w", err)
	}
	if refs > 0 {
		return ErrProviderInUse
	}
	affected, err := queries.DeleteModelProvider(ctx, dbquery.DeleteModelProviderParams{
		TenantID:   tenantID,
		ProviderID: providerID,
	})
	if err != nil {
		return fmt.Errorf("delete model provider: %w", err)
	}
	if affected == 0 {
		return ErrProviderNotFound
	}
	if err := auditCatalog(ctx, queries, tenantID, userID, providerID, "model_provider.delete", "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete model provider: %w", err)
	}
	return nil
}

func (c *Catalog) TestProviderConnectivity(ctx context.Context, tenantID, userID, providerID int64) (ProviderConnectivityResult, error) {
	if providerID <= 0 {
		return ProviderConnectivityResult{}, ErrInvalidConfiguration
	}
	tx, queries, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return ProviderConnectivityResult{}, err
	}
	var commitErr error
	defer rollbackCatalog(tx, &commitErr)
	if err := requireAdministrator(ctx, queries, tenantID, userID); errors.Is(err, ErrForbidden) {
		return ProviderConnectivityResult{}, auditCatalogResult(ctx, tx, queries, tenantID, userID, providerID, "model_provider.test", "deny", ErrForbidden)
	} else if err != nil {
		return ProviderConnectivityResult{}, err
	}
	row, err := queries.GetModelProvider(ctx, dbquery.GetModelProviderParams{
		TenantID:   tenantID,
		ProviderID: providerID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderConnectivityResult{}, ErrProviderNotFound
	} else if err != nil {
		return ProviderConnectivityResult{}, fmt.Errorf("load model provider: %w", err)
	}
	var apiKey []byte
	if len(row.ApiKeyCiphertext) > 0 {
		if c.vault == nil {
			return ProviderConnectivityResult{}, secrets.ErrKeyUnavailable
		}
		apiKey, err = c.vault.Open(ctx, tenantID, providerID, secrets.Encrypted{
			KeyID:      row.ApiKeyID.String,
			Nonce:      row.ApiKeyNonce,
			Ciphertext: row.ApiKeyCiphertext,
		})
		if err != nil {
			return ProviderConnectivityResult{}, fmt.Errorf("decrypt provider credential: %w", err)
		}
		defer clear(apiKey)
	}

	probeURL := strings.TrimRight(row.BaseUrl, "/") + "/models"
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
	if err != nil {
		return ProviderConnectivityResult{}, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
	}
	if len(apiKey) > 0 {
		req.Header.Set("Authorization", "Bearer "+string(apiKey))
	}

	start := time.Now()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, probeErr := client.Do(req)
	latency := time.Since(start).Milliseconds()

	res := ProviderConnectivityResult{LatencyMS: latency}
	auditOutcome := "allow"
	if probeErr != nil {
		res.Reachable = false
		res.Message = "connection failed"
		auditOutcome = "error"
	} else {
		_ = resp.Body.Close()
		res.StatusCode = resp.StatusCode
		res.Reachable = resp.StatusCode >= 200 && resp.StatusCode < 300
		if res.Reachable {
			res.Message = fmt.Sprintf("HTTP %d OK", resp.StatusCode)
		} else {
			res.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
			auditOutcome = "error"
		}
	}

	if err := auditCatalog(ctx, queries, tenantID, userID, providerID, "model_provider.test", auditOutcome); err != nil {
		return ProviderConnectivityResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		commitErr = err
		return ProviderConnectivityResult{}, fmt.Errorf("commit provider connectivity test: %w", err)
	}
	return res, nil
}
