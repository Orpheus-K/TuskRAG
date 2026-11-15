package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
)

var (
	ErrMCPOAuthInvalidToken      = errors.New("invalid mcp oauth bearer token")
	ErrMCPOAuthInsufficientScope = errors.New("insufficient mcp oauth scope")
	ErrMCPOAuthUserDisabled      = errors.New("mcp oauth user disabled")
)

// ListPublicOIDCIssuers returns enabled OIDC issuer URLs for RFC 9728 Protected Resource Metadata.
func (s *Service) ListPublicOIDCIssuers(ctx context.Context, tenantHint *int64) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("identity service unavailable")
	}
	var hint pgtype.Int8
	if tenantHint != nil && *tenantHint > 0 {
		hint = pgtype.Int8{Int64: *tenantHint, Valid: true}
	}
	var issuers []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbquery.New(tx)
		rows, err := q.ListPublicOIDCIssuers(ctx, hint)
		if err != nil {
			return err
		}
		issuers = rows
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list public oidc issuers: %w", err)
	}
	if issuers == nil {
		issuers = []string{}
	}
	return issuers, nil
}

// AuthenticateMCPBearer validates an OIDC/OAuth JWT Bearer token for /mcp,
// enforces RFC 8707 resource/audience matching, syncs OIDC groups, and returns
// the delegated user's Principal.
func (s *Service) AuthenticateMCPBearer(ctx context.Context, rawToken, expectedResource string, tenantHint *int64) (Principal, error) {
	if s == nil || s.pool == nil {
		return Principal{}, errors.New("identity service unavailable")
	}
	unverifiedIss, err := peekJWTIssuer(rawToken)
	if err != nil || unverifiedIss == "" {
		return Principal{}, ErrMCPOAuthInvalidToken
	}

	var hint pgtype.Int8
	if tenantHint != nil && *tenantHint > 0 {
		hint = pgtype.Int8{Int64: *tenantHint, Valid: true}
	}
	var tenantID int64
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbquery.New(tx)
		tid, err := q.ResolveOIDCProviderTenantByIssuer(ctx, dbquery.ResolveOIDCProviderTenantByIssuerParams{
			Issuer:   unverifiedIss,
			TenantID: hint,
		})
		if err != nil {
			return err
		}
		tenantID = tid
		return nil
	}); err != nil {
		return Principal{}, fmt.Errorf("resolve oidc issuer tenant: %w", err)
	}
	if tenantID <= 0 {
		return Principal{}, ErrMCPOAuthInvalidToken
	}

	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return Principal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	provider, err := queries.GetEnabledOIDCProviderByIssuer(ctx, dbquery.GetEnabledOIDCProviderByIssuerParams{
		TenantID: tenantID,
		Issuer:   unverifiedIss,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrMCPOAuthInvalidToken
	} else if err != nil {
		return Principal{}, fmt.Errorf("load oidc provider: %w", err)
	}

	disc, err := fetchOIDCDiscovery(ctx, provider.IssuerUrl)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: discovery failed", ErrMCPOAuthInvalidToken)
	}
	audiences := buildAllowedMCPAudiences(expectedResource, provider.ClientID)
	claims, err := verifyJWTWithAudiences(ctx, disc.JWKSURI, rawToken, provider.IssuerUrl, audiences, "")
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrMCPOAuthInvalidToken, err)
	}
	if !hasMCPTokenScope(claims) {
		return Principal{}, ErrMCPOAuthInsufficientScope
	}
	sub, _ := claims["sub"].(string)
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return Principal{}, ErrMCPOAuthInvalidToken
	}

	var userID int64
	row, err := queries.GetOIDCIdentityUser(ctx, dbquery.GetOIDCIdentityUserParams{
		TenantID: tenantID,
		Issuer:   provider.IssuerUrl,
		Subject:  sub,
	})
	if err == nil {
		if row.Disabled {
			return Principal{}, ErrMCPOAuthUserDisabled
		}
		userID = row.UserID
		if err := queries.TouchOIDCIdentity(ctx, dbquery.TouchOIDCIdentityParams{
			TenantID: tenantID,
			Issuer:   provider.IssuerUrl,
			Subject:  sub,
		}); err != nil {
			return Principal{}, fmt.Errorf("touch oidc identity: %w", err)
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		userID, err = queries.CreateOIDCUser(ctx, dbquery.CreateOIDCUserParams{
			TenantID:    tenantID,
			DisplayName: claimDisplayName(claims, sub),
		})
		if err != nil {
			return Principal{}, fmt.Errorf("create oidc user: %w", err)
		}
		if err := queries.InsertOIDCIdentity(ctx, dbquery.InsertOIDCIdentityParams{
			TenantID:   tenantID,
			ProviderID: provider.ID,
			Issuer:     provider.IssuerUrl,
			Subject:    sub,
			UserID:     userID,
		}); err != nil {
			return Principal{}, fmt.Errorf("insert oidc identity: %w", err)
		}
	} else {
		return Principal{}, fmt.Errorf("lookup oidc identity: %w", err)
	}

	if provider.GroupClaim != "" {
		if _, hasGroups := claims[provider.GroupClaim]; hasGroups {
			if err := syncOIDCGroups(ctx, queries, tx, tenantID, userID, extractStringSliceClaim(claims[provider.GroupClaim])); err != nil {
				return Principal{}, err
			}
		}
	}

	principal := Principal{TenantID: tenantID, UserID: userID}
	if err := audit(ctx, queries, principal, "mcp.auth.oauth", userID, "allow"); err != nil {
		return Principal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Principal{}, fmt.Errorf("commit mcp oauth auth: %w", err)
	}
	return principal, nil
}

func syncOIDCGroups(ctx context.Context, queries *dbquery.Queries, tx pgx.Tx, tenantID, userID int64, groupNames []string) error {
	keepGroupIDs := make([]int64, 0, len(groupNames))
	for _, gName := range groupNames {
		gName = strings.TrimSpace(gName)
		if !validText(gName, 120) {
			continue
		}
		grp, err := queries.UpsertOIDCGroup(ctx, dbquery.UpsertOIDCGroupParams{
			TenantID: tenantID,
			Name:     gName,
		})
		if err != nil {
			return fmt.Errorf("upsert oidc group: %w", err)
		}
		if grp.Source == "oidc" {
			keepGroupIDs = append(keepGroupIDs, grp.ID)
			if err := queries.AddUserToOIDCGroup(ctx, dbquery.AddUserToOIDCGroupParams{
				TenantID: tenantID,
				GroupID:  grp.ID,
				UserID:   userID,
			}); err != nil {
				return fmt.Errorf("add oidc group member: %w", err)
			}
		}
	}
	if err := queries.RemoveStaleOIDCGroupMemberships(ctx, dbquery.RemoveStaleOIDCGroupMembershipsParams{
		TenantID:     tenantID,
		UserID:       userID,
		KeepGroupIds: keepGroupIDs,
	}); err != nil {
		return fmt.Errorf("remove stale oidc groups: %w", err)
	}
	_, _ = tx.Exec(ctx, "SELECT pg_notify('acl_changed', $1)", strconv.FormatInt(tenantID, 10))
	return nil
}

func peekJWTIssuer(rawToken string) (string, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	return strings.TrimSpace(claims.Iss), nil
}

func buildAllowedMCPAudiences(expectedResource, clientID string) []string {
	out := make([]string, 0, 3)
	if trimmed := strings.TrimRight(strings.TrimSpace(expectedResource), "/"); trimmed != "" {
		out = append(out, trimmed, trimmed+"/")
	}
	if clientID != "" {
		out = append(out, clientID)
	}
	return out
}

func hasMCPTokenScope(claims map[string]any) bool {
	var scopes []string
	if s, ok := claims["scope"].(string); ok && strings.TrimSpace(s) != "" {
		scopes = append(scopes, strings.Fields(s)...)
	}
	if arr := extractStringSliceClaim(claims["scp"]); len(arr) > 0 {
		scopes = append(scopes, arr...)
	}
	if len(scopes) == 0 {
		return true
	}
	for _, sc := range scopes {
		switch sc {
		case "mcp", "mcp:read", "mcp:tools", "openid":
			return true
		}
	}
	return false
}
