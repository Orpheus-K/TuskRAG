package identity

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/secrets"
)

var (
	ErrInvalidOIDCRequest = errors.New("invalid oidc provider request")
	ErrOIDCProviderUsed   = errors.New("oidc provider name or issuer already exists")
	ErrOIDCNotFound       = errors.New("oidc provider not found")
	ErrOIDCCannotDisable  = errors.New("cannot disable local login without an enabled OIDC provider")
)

type OIDCProvider struct {
	ID              int64     `json:"id"`
	TenantID        int64     `json:"tenant_id"`
	Name            string    `json:"name"`
	IssuerURL       string    `json:"issuer_url"`
	ClientID        string    `json:"client_id"`
	HasClientSecret bool      `json:"has_client_secret"`
	Scopes          string    `json:"scopes"`
	GroupClaim      string    `json:"group_claim"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
}

type PublicOIDCProvider struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TenantAuthPolicy struct {
	DisableLocalLogin bool                 `json:"disable_local_login"`
	Providers         []PublicOIDCProvider `json:"providers"`
}

type CreateOIDCProviderRequest struct {
	Name         string
	IssuerURL    string
	ClientID     string
	ClientSecret string
	Scopes       string
	GroupClaim   string
	Enabled      bool
}

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type jwkKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwksDoc struct {
	Keys []jwkKey `json:"keys"`
}

func (s *Service) SetVault(v *secrets.Vault) {
	if s != nil {
		s.vault = v
	}
}

func (s *Service) GetTenantAuthPolicy(ctx context.Context, tenantID int64) (TenantAuthPolicy, error) {
	if tenantID <= 0 {
		return TenantAuthPolicy{}, ErrInvalidOIDCRequest
	}
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return TenantAuthPolicy{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := queries.GetTenantAuthPolicy(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantAuthPolicy{}, ErrOIDCNotFound
	} else if err != nil {
		return TenantAuthPolicy{}, fmt.Errorf("get tenant auth policy: %w", err)
	}
	providers, err := queries.ListPublicOIDCProviders(ctx, tenantID)
	if err != nil {
		return TenantAuthPolicy{}, fmt.Errorf("list public oidc providers: %w", err)
	}
	out := TenantAuthPolicy{
		DisableLocalLogin: row.DisableLocalLogin,
		Providers:         make([]PublicOIDCProvider, 0, len(providers)),
	}
	for _, p := range providers {
		out.Providers = append(out.Providers, PublicOIDCProvider{ID: p.ID, Name: p.Name})
	}
	if err := tx.Commit(ctx); err != nil {
		return TenantAuthPolicy{}, fmt.Errorf("commit tenant auth policy: %w", err)
	}
	return out, nil
}

func (s *Service) SetDisableLocalLogin(ctx context.Context, principal Principal, disable bool) (bool, error) {
	tx, queries, err := s.requireAdminTx(ctx, principal)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if disable {
		policy, err := queries.GetTenantAuthPolicy(ctx, principal.TenantID)
		if err != nil {
			return false, fmt.Errorf("check oidc providers: %w", err)
		}
		if !policy.HasEnabledOidc {
			return false, ErrOIDCCannotDisable
		}
	}
	if _, err := queries.UpdateTenantDisableLocalLogin(ctx, dbquery.UpdateTenantDisableLocalLoginParams{
		TenantID:          principal.TenantID,
		DisableLocalLogin: disable,
	}); err != nil {
		return false, fmt.Errorf("update disable_local_login: %w", err)
	}
	if err := audit(ctx, queries, principal, "auth.local_login.toggle", principal.TenantID, "allow"); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit disable_local_login: %w", err)
	}
	return disable, nil
}

func (s *Service) ListOIDCProviders(ctx context.Context, principal Principal) ([]OIDCProvider, error) {
	tx, queries, err := s.requireAdminTx(ctx, principal)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := queries.ListOIDCProviders(ctx, principal.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list oidc providers: %w", err)
	}
	out := make([]OIDCProvider, 0, len(rows))
	for _, r := range rows {
		out = append(out, OIDCProvider{
			ID:              r.ID,
			TenantID:        r.TenantID,
			Name:            r.Name,
			IssuerURL:       r.IssuerUrl,
			ClientID:        r.ClientID,
			HasClientSecret: r.HasClientSecret,
			Scopes:          r.Scopes,
			GroupClaim:      r.GroupClaim,
			Enabled:         r.Enabled,
			CreatedAt:       r.CreatedAt.Time.UTC(),
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit list oidc providers: %w", err)
	}
	return out, nil
}

func (s *Service) CreateOIDCProvider(ctx context.Context, principal Principal, req CreateOIDCProviderRequest) (OIDCProvider, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.IssuerURL = strings.TrimRight(strings.TrimSpace(req.IssuerURL), "/")
	req.ClientID = strings.TrimSpace(req.ClientID)
	req.Scopes = strings.TrimSpace(req.Scopes)
	req.GroupClaim = strings.TrimSpace(req.GroupClaim)
	if req.Scopes == "" {
		req.Scopes = "openid profile email"
	}
	if req.GroupClaim == "" {
		req.GroupClaim = "groups"
	}
	if !validText(req.Name, 120) || !validText(req.ClientID, 256) || !validText(req.GroupClaim, 120) {
		return OIDCProvider{}, ErrInvalidOIDCRequest
	}
	u, err := url.Parse(req.IssuerURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return OIDCProvider{}, ErrInvalidOIDCRequest
	}
	if req.ClientSecret != "" && s.vault == nil {
		return OIDCProvider{}, secrets.ErrKeyUnavailable
	}

	tx, queries, err := s.requireAdminTx(ctx, principal)
	if err != nil {
		return OIDCProvider{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, err := queries.CreateOIDCProvider(ctx, dbquery.CreateOIDCProviderParams{
		TenantID:   principal.TenantID,
		Name:       req.Name,
		IssuerUrl:  req.IssuerURL,
		ClientID:   req.ClientID,
		Scopes:     req.Scopes,
		GroupClaim: req.GroupClaim,
		Enabled:    req.Enabled,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return OIDCProvider{}, ErrOIDCProviderUsed
		}
		return OIDCProvider{}, fmt.Errorf("create oidc provider: %w", err)
	}
	if req.ClientSecret != "" {
		enc, err := s.vault.Seal(ctx, principal.TenantID, id, []byte(req.ClientSecret))
		if err != nil {
			return OIDCProvider{}, fmt.Errorf("seal oidc secret: %w", err)
		}
		if err := queries.SetOIDCProviderSecret(ctx, dbquery.SetOIDCProviderSecretParams{
			TenantID:   principal.TenantID,
			ProviderID: id,
			KeyID:      pgtype.Text{String: enc.KeyID, Valid: true},
			Nonce:      enc.Nonce,
			Ciphertext: enc.Ciphertext,
		}); err != nil {
			return OIDCProvider{}, fmt.Errorf("save oidc secret: %w", err)
		}
	}
	if err := audit(ctx, queries, principal, "oidc_provider.create", id, "allow"); err != nil {
		return OIDCProvider{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OIDCProvider{}, fmt.Errorf("commit create oidc provider: %w", err)
	}
	return OIDCProvider{
		ID:              id,
		TenantID:        principal.TenantID,
		Name:            req.Name,
		IssuerURL:       req.IssuerURL,
		ClientID:        req.ClientID,
		HasClientSecret: req.ClientSecret != "",
		Scopes:          req.Scopes,
		GroupClaim:      req.GroupClaim,
		Enabled:         req.Enabled,
		CreatedAt:       time.Now().UTC(),
	}, nil
}

func (s *Service) DeleteOIDCProvider(ctx context.Context, principal Principal, providerID int64) error {
	if providerID <= 0 {
		return ErrInvalidOIDCRequest
	}
	tx, queries, err := s.requireAdminTx(ctx, principal)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	affected, err := queries.DeleteOIDCProvider(ctx, dbquery.DeleteOIDCProviderParams{
		TenantID:   principal.TenantID,
		ProviderID: providerID,
	})
	if err != nil {
		return fmt.Errorf("delete oidc provider: %w", err)
	}
	if affected == 0 {
		return ErrOIDCNotFound
	}
	if err := audit(ctx, queries, principal, "oidc_provider.delete", providerID, "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete oidc provider: %w", err)
	}
	return nil
}

func (s *Service) StartOIDCLogin(ctx context.Context, tenantID, providerID int64, redirectURI string) (string, error) {
	if tenantID <= 0 || providerID <= 0 || strings.TrimSpace(redirectURI) == "" {
		return "", ErrInvalidOIDCRequest
	}
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	provider, err := queries.GetOIDCProvider(ctx, dbquery.GetOIDCProviderParams{
		TenantID:   tenantID,
		ProviderID: providerID,
	})
	if errors.Is(err, pgx.ErrNoRows) || !provider.Enabled {
		return "", ErrOIDCNotFound
	} else if err != nil {
		return "", fmt.Errorf("load oidc provider: %w", err)
	}

	disc, err := fetchOIDCDiscovery(ctx, provider.IssuerUrl)
	if err != nil {
		return "", err
	}

	var rawState, rawNonce, rawVerifier [32]byte
	if _, err := rand.Read(rawState[:]); err != nil {
		return "", err
	}
	if _, err := rand.Read(rawNonce[:]); err != nil {
		return "", err
	}
	if _, err := rand.Read(rawVerifier[:]); err != nil {
		return "", err
	}
	state := base64.RawURLEncoding.EncodeToString(rawState[:])
	nonce := base64.RawURLEncoding.EncodeToString(rawNonce[:])
	verifier := base64.RawURLEncoding.EncodeToString(rawVerifier[:])
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeSum[:])

	if err := queries.InsertOIDCAuthState(ctx, dbquery.InsertOIDCAuthStateParams{
		State:        state,
		TenantID:     tenantID,
		ProviderID:   providerID,
		CodeVerifier: verifier,
		Nonce:        nonce,
		RedirectUri:  redirectURI,
		ExpiresAt:    pgtype.Timestamptz{Time: time.Now().UTC().Add(10 * time.Minute), Valid: true},
	}); err != nil {
		return "", fmt.Errorf("insert oidc auth state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit oidc auth state: %w", err)
	}

	authURL, err := url.Parse(disc.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("invalid authorization_endpoint: %w", err)
	}
	q := authURL.Query()
	q.Set("response_type", "code")
	q.Set("client_id", provider.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", provider.Scopes)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authURL.RawQuery = q.Encode()
	return authURL.String(), nil
}

func (s *Service) CompleteOIDCLogin(ctx context.Context, state, code string) (Session, error) {
	state = strings.TrimSpace(state)
	code = strings.TrimSpace(code)
	if state == "" || code == "" {
		return Session{}, ErrUnauthenticated
	}

	rowTenant, err := dbquery.New(s.pool).ResolveOIDCAuthStateTenant(ctx, state)
	if err != nil || rowTenant <= 0 {
		return Session{}, ErrUnauthenticated
	}
	tenantID := rowTenant

	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	authState, err := queries.ConsumeOIDCAuthState(ctx, dbquery.ConsumeOIDCAuthStateParams{
		State:    state,
		TenantID: tenantID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrUnauthenticated
	} else if err != nil {
		return Session{}, fmt.Errorf("consume oidc auth state: %w", err)
	}

	provider, err := queries.GetOIDCProvider(ctx, dbquery.GetOIDCProviderParams{
		TenantID:   tenantID,
		ProviderID: authState.ProviderID,
	})
	if errors.Is(err, pgx.ErrNoRows) || !provider.Enabled {
		return Session{}, ErrUnauthenticated
	} else if err != nil {
		return Session{}, fmt.Errorf("load oidc provider: %w", err)
	}

	var clientSecret string
	if len(provider.ClientSecretCiphertext) > 0 && s.vault != nil {
		plain, err := s.vault.Open(ctx, tenantID, provider.ID, secrets.Encrypted{
			KeyID:      provider.ClientSecretID.String,
			Nonce:      provider.ClientSecretNonce,
			Ciphertext: provider.ClientSecretCiphertext,
		})
		if err != nil {
			return Session{}, fmt.Errorf("open oidc secret: %w", err)
		}
		clientSecret = string(plain)
		clear(plain)
	}

	disc, err := fetchOIDCDiscovery(ctx, provider.IssuerUrl)
	if err != nil {
		return Session{}, err
	}
	idToken, err := exchangeOIDCCode(ctx, disc.TokenEndpoint, provider.ClientID, clientSecret, code, authState.RedirectUri, authState.CodeVerifier)
	if err != nil {
		return Session{}, ErrUnauthenticated
	}
	claims, err := verifyIDToken(ctx, disc.JWKSURI, idToken, provider.IssuerUrl, provider.ClientID, authState.Nonce)
	if err != nil {
		return Session{}, ErrUnauthenticated
	}

	sub, _ := claims["sub"].(string)
	if strings.TrimSpace(sub) == "" {
		return Session{}, ErrUnauthenticated
	}
	iss, _ := claims["iss"].(string)
	if strings.TrimSpace(iss) == "" {
		iss = provider.IssuerUrl
	}

	var userID int64
	existing, err := queries.GetOIDCIdentityUser(ctx, dbquery.GetOIDCIdentityUserParams{
		TenantID: tenantID,
		Issuer:   iss,
		Subject:  sub,
	})
	if err == nil {
		if existing.Disabled {
			return Session{}, ErrUnauthenticated
		}
		userID = existing.UserID
		if err := queries.TouchOIDCIdentity(ctx, dbquery.TouchOIDCIdentityParams{
			TenantID: tenantID,
			Issuer:   iss,
			Subject:  sub,
		}); err != nil {
			return Session{}, fmt.Errorf("touch oidc identity: %w", err)
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		displayName := claimDisplayName(claims, sub)
		userID, err = queries.CreateOIDCUser(ctx, dbquery.CreateOIDCUserParams{
			TenantID:    tenantID,
			DisplayName: displayName,
		})
		if err != nil {
			return Session{}, fmt.Errorf("create oidc user: %w", err)
		}
		if err := queries.InsertOIDCIdentity(ctx, dbquery.InsertOIDCIdentityParams{
			TenantID:   tenantID,
			ProviderID: provider.ID,
			Issuer:     iss,
			Subject:    sub,
			UserID:     userID,
		}); err != nil {
			return Session{}, fmt.Errorf("insert oidc identity: %w", err)
		}
	} else {
		return Session{}, fmt.Errorf("lookup oidc identity: %w", err)
	}

	// Sync OIDC group claims (preserve manual groups)
	groupNames := extractStringSliceClaim(claims[provider.GroupClaim])
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
			return Session{}, fmt.Errorf("upsert oidc group: %w", err)
		}
		if grp.Source == "oidc" {
			keepGroupIDs = append(keepGroupIDs, grp.ID)
			if err := queries.AddUserToOIDCGroup(ctx, dbquery.AddUserToOIDCGroupParams{
				TenantID: tenantID,
				GroupID:  grp.ID,
				UserID:   userID,
			}); err != nil {
				return Session{}, fmt.Errorf("add oidc group member: %w", err)
			}
		}
	}
	if err := queries.RemoveStaleOIDCGroupMemberships(ctx, dbquery.RemoveStaleOIDCGroupMembershipsParams{
		TenantID:     tenantID,
		UserID:       userID,
		KeepGroupIds: keepGroupIDs,
	}); err != nil {
		return Session{}, fmt.Errorf("remove stale oidc groups: %w", err)
	}
	_, _ = tx.Exec(ctx, "SELECT pg_notify('acl_changed', $1)", strconv.FormatInt(tenantID, 10))

	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	digest := sha256.Sum256(tokenBytes[:])
	if err := queries.CreateSession(ctx, dbquery.CreateSessionParams{
		TenantID:        tenantID,
		UserID:          userID,
		TokenHash:       digest[:],
		AbsoluteSeconds: s.config.AbsoluteTimeout.Seconds(),
	}); err != nil {
		return Session{}, fmt.Errorf("create oidc session: %w", err)
	}
	principal := Principal{TenantID: tenantID, UserID: userID}
	if err := audit(ctx, queries, principal, "auth.login.oidc", userID, "allow"); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit oidc login: %w", err)
	}

	token := strconv.FormatInt(tenantID, 10) + "." + base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	return Session{Principal: principal, Token: token, CSRF: csrfFor(tokenBytes[:])}, nil
}

func claimDisplayName(claims map[string]any, fallback string) string {
	for _, k := range []string{"name", "preferred_username", "email"} {
		if v, ok := claims[k].(string); ok && validText(strings.TrimSpace(v), 200) {
			return strings.TrimSpace(v)
		}
	}
	if len(fallback) > 200 {
		return fallback[:200]
	}
	return fallback
}

func extractStringSliceClaim(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func fetchOIDCDiscovery(ctx context.Context, issuerURL string) (oidcDiscovery, error) {
	discURL := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discURL, nil)
	if err != nil {
		return oidcDiscovery{}, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return oidcDiscovery{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oidcDiscovery{}, fmt.Errorf("oidc discovery HTTP %d", resp.StatusCode)
	}
	var disc oidcDiscovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&disc); err != nil {
		return oidcDiscovery{}, err
	}
	if disc.AuthorizationEndpoint == "" || disc.TokenEndpoint == "" || disc.JWKSURI == "" {
		return oidcDiscovery{}, errors.New("incomplete oidc discovery document")
	}
	return disc, nil
}

func exchangeOIDCCode(ctx context.Context, tokenEndpoint, clientID, clientSecret, code, redirectURI, verifier string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc token endpoint HTTP %d", resp.StatusCode)
	}
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.IDToken == "" {
		return "", errors.New("missing id_token")
	}
	return body.IDToken, nil
}

func verifyIDToken(ctx context.Context, jwksURI, idToken, expectedIssuer, expectedClientID, expectedNonce string) (map[string]any, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid jwt format")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return nil, err
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks HTTP %d", resp.StatusCode)
	}
	var jwks jwksDoc
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&jwks); err != nil {
		return nil, err
	}

	signingInput := []byte(parts[0] + "." + parts[1])
	verified := false
	for _, k := range jwks.Keys {
		if hdr.Kid != "" && k.Kid != "" && hdr.Kid != k.Kid {
			continue
		}
		if verifyJWTSig(hdr.Alg, signingInput, sig, k) {
			verified = true
			break
		}
	}
	if !verified {
		return nil, errors.New("jwt signature verification failed")
	}

	iss, _ := claims["iss"].(string)
	if strings.TrimRight(iss, "/") != strings.TrimRight(expectedIssuer, "/") {
		return nil, errors.New("issuer mismatch")
	}
	if !audContains(claims["aud"], expectedClientID) {
		return nil, errors.New("audience mismatch")
	}
	exp, ok := claims["exp"].(float64)
	if !ok || time.Now().UTC().Unix() >= int64(exp) {
		return nil, errors.New("token expired")
	}
	nonce, _ := claims["nonce"].(string)
	if expectedNonce != "" && nonce != expectedNonce {
		return nil, errors.New("nonce mismatch")
	}
	return claims, nil
}

func audContains(aud any, target string) bool {
	switch v := aud.(type) {
	case string:
		return v == target
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == target {
				return true
			}
		}
	}
	return false
}

func verifyJWTSig(alg string, input, sig []byte, k jwkKey) bool {
	sum := sha256.Sum256(input)
	switch alg {
	case "RS256":
		if k.Kty != "RSA" || k.N == "" || k.E == "" {
			return false
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return false
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return false
		}
		eInt := 0
		for _, b := range eb {
			eInt = eInt<<8 + int(b)
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: eInt}
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) == nil
	case "ES256":
		if k.Kty != "EC" || k.Crv != "P-256" || len(sig) != 64 {
			return false
		}
		xb, err1 := base64.RawURLEncoding.DecodeString(k.X)
		yb, err2 := base64.RawURLEncoding.DecodeString(k.Y)
		if err1 != nil || err2 != nil {
			return false
		}
		pub := &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(xb),
			Y:     new(big.Int).SetBytes(yb),
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		return ecdsa.Verify(pub, sum[:], r, s)
	default:
		return false
	}
}
