package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/secrets"
)

func (c *Catalog) OpenRerankClient(ctx context.Context, binding EmbeddingBinding, providerID int64, config RerankConfig, resolver EmbeddingScopeResolver, auditor Auditor) (*RerankClient, error) {
	if c == nil || c.pool == nil || ctx == nil || resolver == nil || auditor == nil || providerID <= 0 {
		return nil, ErrInvalidConfiguration
	}
	scope, err := resolver.ResolveEmbeddingScope(ctx, binding)
	if err != nil {
		return nil, err
	}
	if scope.TenantID != binding.TenantID || scope.KnowledgeBaseID != binding.KnowledgeBaseID {
		return nil, ErrScopeMismatch
	}
	row, err := c.readRerankProvider(ctx, binding.TenantID, providerID)
	if err != nil {
		return nil, err
	}
	if row.ApiKeyID.Valid != (row.ApiKeyNonce != nil && row.ApiKeyCiphertext != nil) {
		return nil, secrets.ErrInvalidSecret
	}
	var key []byte
	if row.ApiKeyID.Valid {
		if c.vault == nil {
			return nil, secrets.ErrKeyUnavailable
		}
		key, err = c.vault.Open(ctx, binding.TenantID, providerID, secrets.Encrypted{KeyID: row.ApiKeyID.String, Nonce: row.ApiKeyNonce, Ciphertext: row.ApiKeyCiphertext})
		if err != nil {
			return nil, err
		}
		defer clear(key)
	}
	client, err := NewRerankClient(Provider{ID: providerID, BaseURL: row.BaseUrl, Locality: Locality(row.Locality), APIKey: string(key)}, config, auditor)
	if err != nil {
		return nil, err
	}
	client.client.binding = &binding
	client.client.resolver = rerankScopeResolver{catalog: c, resolver: resolver, snapshot: row}
	return client, nil
}

type rerankScopeResolver struct {
	catalog  *Catalog
	resolver EmbeddingScopeResolver
	snapshot dbquery.LoadRerankProviderRow
}

func (r rerankScopeResolver) ResolveEmbeddingScope(ctx context.Context, binding EmbeddingBinding) (EgressScope, error) {
	scope, err := r.resolver.ResolveEmbeddingScope(ctx, binding)
	if err != nil {
		return EgressScope{}, err
	}
	current, err := r.catalog.readRerankProvider(ctx, binding.TenantID, r.snapshot.ID)
	if err != nil {
		return EgressScope{}, err
	}
	a, b := r.snapshot, current
	if a.ID != b.ID || a.BaseUrl != b.BaseUrl || a.Locality != b.Locality || a.ApiKeyID != b.ApiKeyID || !bytes.Equal(a.ApiKeyNonce, b.ApiKeyNonce) || !bytes.Equal(a.ApiKeyCiphertext, b.ApiKeyCiphertext) {
		return EgressScope{}, ErrRuntimeChanged
	}
	return scope, nil
}
func (c *Catalog) readRerankProvider(ctx context.Context, tenantID, providerID int64) (_ dbquery.LoadRerankProviderRow, resultErr error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return dbquery.LoadRerankProviderRow{}, fmt.Errorf("begin rerank configuration transaction: %w", err)
	}
	defer rollbackCatalog(tx, &resultErr)
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, tenantID); err != nil {
		return dbquery.LoadRerankProviderRow{}, fmt.Errorf("set rerank configuration tenant: %w", err)
	}
	row, err := queries.LoadRerankProvider(ctx, dbquery.LoadRerankProviderParams{TenantID: tenantID, ProviderID: providerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbquery.LoadRerankProviderRow{}, ErrRuntimeUnavailable
	}
	if err != nil {
		return dbquery.LoadRerankProviderRow{}, fmt.Errorf("load rerank configuration: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dbquery.LoadRerankProviderRow{}, fmt.Errorf("commit rerank configuration read: %w", err)
	}
	return row, nil
}
