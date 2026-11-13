package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var (
	ErrInvalidAPIKeyRequest = errors.New("invalid api key request")
	ErrAPIKeyNotFound       = errors.New("api key not found")
	ErrAPIKeyForbidden      = errors.New("api key forbidden")
	ErrAPIKeyRateLimited    = errors.New("api key rate limit exceeded")
	ErrAPIKeyQuotaExceeded  = errors.New("api key monthly token quota exceeded")
)

var validAPIKeyActions = map[string]struct{}{
	"search": {},
	"read":   {},
	"answer": {},
	"mcp":    {},
	"ingest": {},
}

type CreateAPIKeyRequest struct {
	Name              string
	KBIDs             []int64
	Actions           []string
	IPAllowlist       []string
	RateLimitRPM      int32
	MonthlyTokenQuota int64
	ExpiresAt         *time.Time
}

type APIKey struct {
	ID                int64
	TenantID          int64
	UserID            int64
	Name              string
	Prefix            string
	KBIDs             []int64
	Actions           []string
	IPAllowlist       []string
	RateLimitRPM      int32
	MonthlyTokenQuota int64
	MonthTokensUsed   int64
	ExpiresAt         *time.Time
	RevokedAt         *time.Time
	LastUsedAt        *time.Time
	CreatedAt         time.Time
}

type CreatedAPIKey struct {
	Key       APIKey
	Plaintext string
}

type AuthenticatedAPIKey struct {
	ID        int64
	Principal Principal
	KBIDs     []int64
	Actions   []string
}

type keyBucket struct {
	windowStart time.Time
	count       int32
}

var (
	rateMu      sync.Mutex
	rateBuckets = make(map[int64]*keyBucket)
)

func allowKeyRequest(keyID int64, rpm int32, now time.Time) bool {
	if rpm <= 0 {
		return true
	}
	rateMu.Lock()
	defer rateMu.Unlock()
	b, ok := rateBuckets[keyID]
	if !ok || now.Sub(b.windowStart) >= time.Minute {
		rateBuckets[keyID] = &keyBucket{windowStart: now, count: 1}
		return true
	}
	if b.count >= rpm {
		return false
	}
	b.count++
	return true
}

func randomBase62(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = base62Alphabet[int(b)%len(base62Alphabet)]
	}
	return string(out), nil
}

func currentMonthKey(t time.Time) string {
	return t.UTC().Format("2006-01")
}

func (s *Service) CreateAPIKey(ctx context.Context, principal Principal, req CreateAPIKeyRequest) (_ CreatedAPIKey, resultErr error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 120 || len(req.Actions) == 0 {
		return CreatedAPIKey{}, ErrInvalidAPIKeyRequest
	}
	actions := make([]string, 0, len(req.Actions))
	for _, act := range req.Actions {
		if _, ok := validAPIKeyActions[act]; !ok {
			return CreatedAPIKey{}, ErrInvalidAPIKeyRequest
		}
		if !slices.Contains(actions, act) {
			actions = append(actions, act)
		}
	}
	for _, kbID := range req.KBIDs {
		if kbID <= 0 {
			return CreatedAPIKey{}, ErrInvalidAPIKeyRequest
		}
	}
	for _, entry := range req.IPAllowlist {
		if _, err := netip.ParsePrefix(entry); err != nil {
			if _, err2 := netip.ParseAddr(entry); err2 != nil {
				return CreatedAPIKey{}, ErrInvalidAPIKeyRequest
			}
		}
	}
	rpm := req.RateLimitRPM
	if rpm <= 0 {
		rpm = 60
	}
	if req.MonthlyTokenQuota < 0 {
		return CreatedAPIKey{}, ErrInvalidAPIKeyRequest
	}

	prefix, err := randomBase62(8)
	if err != nil {
		return CreatedAPIKey{}, fmt.Errorf("generate api key prefix: %w", err)
	}
	secret, err := randomBase62(32)
	if err != nil {
		return CreatedAPIKey{}, fmt.Errorf("generate api key secret: %w", err)
	}
	plaintext := "tsk_" + prefix + "_" + secret
	digest := sha256.Sum256([]byte(plaintext))

	tx, queries, err := s.begin(ctx, principal.TenantID)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	defer rollback(tx, &resultErr)

	var expires pgtype.Timestamptz
	if req.ExpiresAt != nil {
		expires = pgtype.Timestamptz{Time: req.ExpiresAt.UTC(), Valid: true}
	}
	kbIDs := req.KBIDs
	if kbIDs == nil {
		kbIDs = []int64{}
	}
	ips := req.IPAllowlist
	if ips == nil {
		ips = []string{}
	}

	row, err := queries.CreateAPIKey(ctx, dbquery.CreateAPIKeyParams{
		TenantID:          principal.TenantID,
		UserID:            principal.UserID,
		Name:              req.Name,
		Prefix:            prefix,
		SecretHash:        digest[:],
		KbIds:             kbIDs,
		Actions:           actions,
		IpAllowlist:       ips,
		RateLimitRpm:      rpm,
		MonthlyTokenQuota: req.MonthlyTokenQuota,
		ExpiresAt:         expires,
	})
	if err != nil {
		return CreatedAPIKey{}, fmt.Errorf("create api key: %w", err)
	}
	if err := audit(ctx, queries, principal, "api_key.create", row.ID, "allow"); err != nil {
		return CreatedAPIKey{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreatedAPIKey{}, fmt.Errorf("commit api key creation: %w", err)
	}
	return CreatedAPIKey{
		Key: APIKey{
			ID:                row.ID,
			TenantID:          row.TenantID,
			UserID:            row.UserID,
			Name:              row.Name,
			Prefix:            row.Prefix,
			KBIDs:             row.KbIds,
			Actions:           row.Actions,
			IPAllowlist:       row.IpAllowlist,
			RateLimitRPM:      row.RateLimitRpm,
			MonthlyTokenQuota: row.MonthlyTokenQuota,
			ExpiresAt:         optTime(row.ExpiresAt),
			RevokedAt:         optTime(row.RevokedAt),
			LastUsedAt:        optTime(row.LastUsedAt),
			CreatedAt:         row.CreatedAt.Time,
		},
		Plaintext: plaintext,
	}, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, principal Principal) (_ []APIKey, resultErr error) {
	tx, queries, err := s.begin(ctx, principal.TenantID)
	if err != nil {
		return nil, err
	}
	defer rollback(tx, &resultErr)
	rows, err := queries.ListAPIKeys(ctx, dbquery.ListAPIKeysParams{
		TenantID: principal.TenantID,
		UserID:   principal.UserID,
		MonthKey: currentMonthKey(time.Now()),
	})
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	out := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, APIKey{
			ID:                row.ID,
			TenantID:          row.TenantID,
			UserID:            row.UserID,
			Name:              row.Name,
			Prefix:            row.Prefix,
			KBIDs:             row.KbIds,
			Actions:           row.Actions,
			IPAllowlist:       row.IpAllowlist,
			RateLimitRPM:      row.RateLimitRpm,
			MonthlyTokenQuota: row.MonthlyTokenQuota,
			MonthTokensUsed:   row.MonthTokensUsed,
			ExpiresAt:         optTime(row.ExpiresAt),
			RevokedAt:         optTime(row.RevokedAt),
			LastUsedAt:        optTime(row.LastUsedAt),
			CreatedAt:         row.CreatedAt.Time,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit api key list: %w", err)
	}
	return out, nil
}

func (s *Service) RevokeAPIKey(ctx context.Context, principal Principal, keyID int64) (resultErr error) {
	if keyID <= 0 {
		return ErrInvalidAPIKeyRequest
	}
	tx, queries, err := s.begin(ctx, principal.TenantID)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	affected, err := queries.RevokeAPIKey(ctx, dbquery.RevokeAPIKeyParams{
		TenantID: principal.TenantID,
		UserID:   principal.UserID,
		KeyID:    keyID,
	})
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if affected == 0 {
		return ErrAPIKeyNotFound
	}
	if err := audit(ctx, queries, principal, "api_key.revoke", keyID, "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit api key revocation: %w", err)
	}
	return nil
}

func (s *Service) AuthenticateAPIKey(ctx context.Context, rawKey, clientIP string) (_ AuthenticatedAPIKey, resultErr error) {
	prefix, ok := parseAPIKeyPrefix(rawKey)
	if !ok {
		return AuthenticatedAPIKey{}, ErrUnauthenticated
	}
	tenantID, err := dbquery.New(s.pool).ResolveAPIKeyTenant(ctx, prefix)
	if err != nil {
		return AuthenticatedAPIKey{}, fmt.Errorf("resolve api key tenant: %w", err)
	}
	if tenantID <= 0 {
		return AuthenticatedAPIKey{}, ErrUnauthenticated
	}

	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return AuthenticatedAPIKey{}, err
	}
	defer rollback(tx, &resultErr)

	now := time.Now().UTC()
	row, err := queries.GetAPIKeyByPrefix(ctx, dbquery.GetAPIKeyByPrefixParams{
		TenantID: tenantID,
		Prefix:   prefix,
		MonthKey: currentMonthKey(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthenticatedAPIKey{}, ErrUnauthenticated
	}
	if err != nil {
		return AuthenticatedAPIKey{}, fmt.Errorf("lookup api key: %w", err)
	}

	digest := sha256.Sum256([]byte(rawKey))
	if subtle.ConstantTimeCompare(digest[:], row.SecretHash) != 1 {
		return AuthenticatedAPIKey{}, ErrUnauthenticated
	}
	if row.RevokedAt.Valid || row.UserDisabled {
		return AuthenticatedAPIKey{}, ErrUnauthenticated
	}
	if row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(now) {
		return AuthenticatedAPIKey{}, ErrUnauthenticated
	}
	if len(row.IpAllowlist) > 0 && !ipAllowed(clientIP, row.IpAllowlist) {
		_ = audit(ctx, queries, Principal{TenantID: tenantID, UserID: row.UserID}, "api_key.ip_denied", row.ID, "deny")
		_ = tx.Commit(ctx)
		return AuthenticatedAPIKey{}, ErrAPIKeyForbidden
	}
	if !allowKeyRequest(row.ID, row.RateLimitRpm, now) {
		return AuthenticatedAPIKey{}, ErrAPIKeyRateLimited
	}
	if row.MonthlyTokenQuota > 0 && row.MonthTokensUsed >= row.MonthlyTokenQuota {
		return AuthenticatedAPIKey{}, ErrAPIKeyQuotaExceeded
	}
	if err := queries.TouchAPIKeyLastUsed(ctx, dbquery.TouchAPIKeyLastUsedParams{
		TenantID: tenantID,
		KeyID:    row.ID,
	}); err != nil {
		return AuthenticatedAPIKey{}, fmt.Errorf("touch api key: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AuthenticatedAPIKey{}, fmt.Errorf("commit api key auth: %w", err)
	}
	return AuthenticatedAPIKey{
		ID:        row.ID,
		Principal: Principal{TenantID: tenantID, UserID: row.UserID},
		KBIDs:     row.KbIds,
		Actions:   row.Actions,
	}, nil
}

func (s *Service) RecordAPIKeyTokenUsage(ctx context.Context, tenantID, keyID, tokens int64) (resultErr error) {
	if tenantID <= 0 || keyID <= 0 || tokens <= 0 {
		return nil
	}
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	if err := queries.AddAPIKeyTokenUsage(ctx, dbquery.AddAPIKeyTokenUsageParams{
		TenantID: tenantID,
		ApiKeyID: keyID,
		MonthKey: currentMonthKey(time.Now()),
		Tokens:   tokens,
	}); err != nil {
		return fmt.Errorf("record api key token usage: %w", err)
	}
	return tx.Commit(ctx)
}

func AuthorizeAPIKeyScope(key AuthenticatedAPIKey, kbID int64, action string) bool {
	if !slices.Contains(key.Actions, action) {
		return false
	}
	if kbID > 0 && len(key.KBIDs) > 0 && !slices.Contains(key.KBIDs, kbID) {
		return false
	}
	return true
}

func parseAPIKeyPrefix(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "tsk_") {
		return "", false
	}
	rest := strings.TrimPrefix(raw, "tsk_")
	parts := strings.Split(rest, "_")
	if len(parts) != 2 || len(parts[0]) != 8 || len(parts[1]) != 32 {
		return "", false
	}
	return parts[0], true
}

func ipAllowed(rawIP string, allowlist []string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return false
	}
	for _, item := range allowlist {
		if prefix, err := netip.ParsePrefix(item); err == nil {
			if prefix.Contains(addr) {
				return true
			}
			continue
		}
		if allowed, err := netip.ParseAddr(item); err == nil && allowed == addr {
			return true
		}
	}
	return false
}

func optTime(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}
