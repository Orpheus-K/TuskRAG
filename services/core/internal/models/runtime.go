package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/secrets"
)

var (
	ErrRuntimeUnavailable = errors.New("embedding configuration is unavailable")
	ErrRuntimeChanged     = errors.New("embedding configuration changed")
)

type ClientLimits struct {
	MaxBatch         int
	MaxRequestBytes  int64
	MaxResponseBytes int64
	Timeout          time.Duration
	Concurrency      int
}

// OpenEmbeddingClient is for trusted service code after its own authorization.
// Every call resolves the bound knowledge base's current egress policies.
func (c *Catalog) OpenEmbeddingClient(ctx context.Context, binding EmbeddingBinding, limits ClientLimits, resolver EmbeddingScopeResolver, auditor Auditor) (*EmbeddingClient, Space, error) {
	if c == nil || c.pool == nil || ctx == nil || binding.TenantID <= 0 || binding.KnowledgeBaseID <= 0 || binding.EmbeddingSpaceID <= 0 || binding.ConfigRevision <= 0 || resolver == nil || auditor == nil {
		return nil, Space{}, errors.New("catalog, embedding binding, scope resolver and auditor are required")
	}
	if limits.MaxBatch <= 0 || limits.MaxRequestBytes <= 0 || limits.MaxResponseBytes <= 0 || limits.Timeout <= 0 || limits.Concurrency <= 0 {
		return nil, Space{}, errors.New("valid embedding client limits are required")
	}
	scope, err := resolver.ResolveEmbeddingScope(ctx, binding)
	if err != nil {
		return nil, Space{}, fmt.Errorf("resolve embedding scope: %w", err)
	}
	if scope.TenantID != binding.TenantID || scope.KnowledgeBaseID != binding.KnowledgeBaseID {
		return nil, Space{}, ErrScopeMismatch
	}
	row, err := c.readEmbeddingRuntime(ctx, binding.TenantID, binding.EmbeddingSpaceID)
	if err != nil {
		return nil, Space{}, err
	}
	if row.ApiKeyID.Valid != (row.ApiKeyNonce != nil && row.ApiKeyCiphertext != nil) {
		return nil, Space{}, secrets.ErrInvalidSecret
	}
	var key []byte
	if row.ApiKeyID.Valid {
		if c.vault == nil {
			return nil, Space{}, secrets.ErrKeyUnavailable
		}
		key, err = c.vault.Open(ctx, binding.TenantID, row.ProviderID, secrets.Encrypted{
			KeyID: row.ApiKeyID.String, Nonce: row.ApiKeyNonce, Ciphertext: row.ApiKeyCiphertext,
		})
		if err != nil {
			return nil, Space{}, err
		}
		defer clear(key)
	}
	provider := Provider{ID: row.ProviderID, BaseURL: row.BaseUrl, Locality: Locality(row.Locality), APIKey: string(key)}
	config := EmbeddingConfig{Model: row.Model, Dimensions: int(row.Dims), Normalize: row.Normalized,
		MaxBatch: limits.MaxBatch, MaxRequestBytes: limits.MaxRequestBytes, MaxResponseBytes: limits.MaxResponseBytes,
		Timeout: limits.Timeout, Concurrency: limits.Concurrency}
	client, err := NewEmbeddingClient(provider, config, auditor)
	if err != nil {
		return nil, Space{}, fmt.Errorf("initialize embedding client: %w", err)
	}
	snapshot := row
	snapshot.ApiKeyNonce, snapshot.ApiKeyCiphertext = bytes.Clone(row.ApiKeyNonce), bytes.Clone(row.ApiKeyCiphertext)
	client.binding, client.resolver = &binding, runtimeScopeResolver{catalog: c, resolver: resolver, snapshot: snapshot}
	space := Space{ID: row.ID, ProviderID: row.ProviderID, ProviderName: row.ProviderName, BaseURL: row.BaseUrl,
		Locality: Locality(row.Locality), Model: row.Model, Dimensions: row.Dims, Normalized: row.Normalized,
		Distance: row.Distance, HasCredential: row.ApiKeyID.Valid}
	return client, space, nil
}

func (c *Catalog) readEmbeddingRuntime(ctx context.Context, tenantID, spaceID int64) (_ dbquery.LoadEmbeddingRuntimeRow, resultErr error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return dbquery.LoadEmbeddingRuntimeRow{}, fmt.Errorf("begin embedding configuration transaction: %w", err)
	}
	defer rollbackCatalog(tx, &resultErr)
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, tenantID); err != nil {
		return dbquery.LoadEmbeddingRuntimeRow{}, fmt.Errorf("set embedding configuration tenant: %w", err)
	}
	row, err := queries.LoadEmbeddingRuntime(ctx, dbquery.LoadEmbeddingRuntimeParams{TenantID: tenantID, SpaceID: spaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbquery.LoadEmbeddingRuntimeRow{}, ErrRuntimeUnavailable
	}
	if err != nil {
		return dbquery.LoadEmbeddingRuntimeRow{}, fmt.Errorf("load embedding configuration: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dbquery.LoadEmbeddingRuntimeRow{}, fmt.Errorf("commit embedding configuration read: %w", err)
	}
	return row, nil
}

type runtimeScopeResolver struct {
	catalog  *Catalog
	resolver EmbeddingScopeResolver
	snapshot dbquery.LoadEmbeddingRuntimeRow
}

func (r runtimeScopeResolver) ResolveEmbeddingScope(ctx context.Context, binding EmbeddingBinding) (EgressScope, error) {
	scope, err := r.resolver.ResolveEmbeddingScope(ctx, binding)
	if err != nil {
		return EgressScope{}, err
	}
	if scope.TenantID != binding.TenantID || scope.KnowledgeBaseID != binding.KnowledgeBaseID {
		return EgressScope{}, ErrScopeMismatch
	}
	current, err := r.catalog.readEmbeddingRuntime(ctx, binding.TenantID, binding.EmbeddingSpaceID)
	if err != nil {
		return EgressScope{}, err
	}
	if !sameEmbeddingRuntime(r.snapshot, current) {
		return EgressScope{}, ErrRuntimeChanged
	}
	return scope, nil
}

func sameEmbeddingRuntime(a, b dbquery.LoadEmbeddingRuntimeRow) bool {
	return a.ID == b.ID && a.ProviderID == b.ProviderID && a.BaseUrl == b.BaseUrl && a.Locality == b.Locality &&
		a.Model == b.Model && a.Dims == b.Dims && a.Normalized == b.Normalized && a.Distance == b.Distance &&
		a.ApiKeyID == b.ApiKeyID && bytes.Equal(a.ApiKeyNonce, b.ApiKeyNonce) && bytes.Equal(a.ApiKeyCiphertext, b.ApiKeyCiphertext)
}
