package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/secrets"
)

type ChatModel struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

type NewChatModel struct {
	Name     string   `json:"name" minLength:"1" maxLength:"200"`
	BaseURL  string   `json:"base_url" minLength:"1" maxLength:"2048"`
	Locality Locality `json:"locality" enum:"internal,external"`
	APIKey   string   `json:"api_key,omitempty" maxLength:"4096"`
	Model    string   `json:"model" minLength:"1" maxLength:"200"`
}

func (c *Catalog) CreateChatModel(ctx context.Context, tenantID, userID int64, req NewChatModel) (_ ChatModel, resultErr error) {
	req.Name, req.Model = strings.TrimSpace(req.Name), strings.TrimSpace(req.Model)
	base, err := providerURL(req.BaseURL)
	if err != nil || !validCatalogText(req.Name, 200) || !validCatalogText(req.Model, 200) ||
		(req.Locality != Internal && req.Locality != External) || (req.Locality == External && base.Scheme != "https") ||
		len(req.APIKey) > 4096 || strings.ContainsAny(req.APIKey, "\r\n") {
		return ChatModel{}, ErrInvalidConfiguration
	}
	tx, q, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return ChatModel{}, err
	}
	defer rollbackCatalog(tx, &resultErr)
	if err := requireAdministrator(ctx, q, tenantID, userID); err != nil {
		if errors.Is(err, ErrForbidden) {
			return ChatModel{}, auditCatalogResult(ctx, tx, q, tenantID, userID, 0, "model.chat.create", "deny", err)
		}
		return ChatModel{}, err
	}
	if req.APIKey != "" && c.vault == nil {
		return ChatModel{}, secrets.ErrKeyUnavailable
	}
	provider, err := q.CreateModelProvider(ctx, dbquery.CreateModelProviderParams{TenantID: tenantID, Name: req.Name, BaseUrl: req.BaseURL, Locality: string(req.Locality)})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ChatModel{}, ErrProviderNameUsed
		}
		return ChatModel{}, fmt.Errorf("create chat provider: %w", err)
	}
	if req.APIKey != "" {
		key := []byte(req.APIKey)
		defer clear(key)
		encrypted, err := c.vault.Seal(ctx, tenantID, provider, key)
		if err != nil {
			return ChatModel{}, err
		}
		if err = q.SetModelProviderCredential(ctx, dbquery.SetModelProviderCredentialParams{TenantID: tenantID, ProviderID: provider, KeyID: pgtype.Text{String: encrypted.KeyID, Valid: true}, Nonce: encrypted.Nonce, Ciphertext: encrypted.Ciphertext}); err != nil {
			return ChatModel{}, err
		}
	}
	id, err := q.CreateChatModel(ctx, dbquery.CreateChatModelParams{TenantID: tenantID, ProviderID: provider, Model: req.Model})
	if err != nil {
		return ChatModel{}, err
	}
	if err = auditCatalog(ctx, q, tenantID, userID, provider, "model.chat.create", "allow"); err != nil {
		return ChatModel{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ChatModel{}, err
	}
	return ChatModel{ID: id, Name: req.Name, Model: req.Model}, nil
}

func (c *Catalog) ListChatModels(ctx context.Context, tenantID, userID int64) (_ []ChatModel, resultErr error) {
	tx, q, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rollbackCatalog(tx, &resultErr)
	rows, err := q.ListChatModels(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	result := make([]ChatModel, 0, len(rows))
	for _, r := range rows {
		result = append(result, ChatModel{ID: r.ID, Name: r.Name, Model: r.Model})
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Catalog) OpenChatClient(ctx context.Context, tenantID, userID, modelID int64, scope func(context.Context) (EgressScope, error), auditor Auditor) (*ChatClient, error) {
	tx, q, err := c.begin(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	model, err := q.LoadChatModel(ctx, dbquery.LoadChatModelParams{TenantID: tenantID, ID: modelID})
	rollbackCatalog(tx, &err)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRuntimeUnavailable
	}
	if err != nil {
		return nil, err
	}
	row, err := c.readRerankProvider(ctx, tenantID, model.ProviderID)
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
		key, err = c.vault.Open(ctx, tenantID, row.ID, secrets.Encrypted{KeyID: row.ApiKeyID.String, Nonce: row.ApiKeyNonce, Ciphertext: row.ApiKeyCiphertext})
		if err != nil {
			return nil, err
		}
		defer clear(key)
	}
	check := func(ctx context.Context) (EgressScope, error) {
		current, err := c.readRerankProvider(ctx, tenantID, row.ID)
		if err != nil {
			return EgressScope{}, err
		}
		if current.BaseUrl != row.BaseUrl || current.Locality != row.Locality || current.ApiKeyID != row.ApiKeyID || !bytes.Equal(current.ApiKeyNonce, row.ApiKeyNonce) || !bytes.Equal(current.ApiKeyCiphertext, row.ApiKeyCiphertext) {
			return EgressScope{}, ErrRuntimeChanged
		}
		return scope(ctx)
	}
	return NewChatClient(Provider{ID: row.ID, BaseURL: row.BaseUrl, Locality: Locality(row.Locality), APIKey: string(key)}, model.Model, 90*time.Second, check, auditor)
}
