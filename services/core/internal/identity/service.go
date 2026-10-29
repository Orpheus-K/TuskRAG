package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
)

var (
	ErrUnauthenticated = errors.New("invalid credentials or session")
	ErrTenantExists    = errors.New("tenant name already exists")
)

type Principal struct {
	TenantID int64
	UserID   int64
}

type BootstrapRequest struct {
	TenantName  string
	DisplayName string
	Login       string
	Password    []byte
}

type SessionConfig struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
}

type Session struct {
	Principal Principal
	Token     string
	CSRF      string
}

type Service struct {
	pool   *pgxpool.Pool
	hasher *PasswordHasher
	config SessionConfig
	dummy  string
}

func NewService(pool *pgxpool.Pool, hasher *PasswordHasher, config SessionConfig) (*Service, error) {
	if pool == nil || hasher == nil || config.IdleTimeout <= 0 || config.AbsoluteTimeout < config.IdleTimeout {
		return nil, errors.New("database, password hasher and valid session timeouts are required")
	}
	var salt [passwordSaltSize]byte
	var hash [passwordHashSize]byte
	dummy := passwordPrefix + base64.RawStdEncoding.EncodeToString(salt[:]) + "$" + base64.RawStdEncoding.EncodeToString(hash[:])
	return &Service{pool: pool, hasher: hasher, config: config, dummy: dummy}, nil
}

// Bootstrap is an operator action; it must not be exposed as an unauthenticated API.
func (s *Service) Bootstrap(ctx context.Context, request BootstrapRequest) (_ Principal, resultErr error) {
	request.TenantName = strings.TrimSpace(request.TenantName)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.Login = strings.TrimSpace(request.Login)
	if !validText(request.TenantName, 200) || !validText(request.DisplayName, 200) || !validText(request.Login, 128) ||
		len(request.Password) < 12 || len(request.Password) > passwordMaxBytes {
		return Principal{}, errors.New("valid tenant, user, login and password of at least 12 bytes are required")
	}
	passwordHash, err := s.hasher.Hash(ctx, request.Password)
	if err != nil {
		return Principal{}, fmt.Errorf("hash local password: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Principal{}, fmt.Errorf("begin identity bootstrap: %w", err)
	}
	defer rollback(tx, &resultErr)
	queries := dbquery.New(tx)
	tenantID, err := queries.NextTenantID(ctx)
	if err != nil {
		return Principal{}, fmt.Errorf("allocate tenant ID: %w", err)
	}
	if _, err := queries.SetTenant(ctx, tenantID); err != nil {
		return Principal{}, fmt.Errorf("set bootstrap tenant: %w", err)
	}
	if err := queries.BootstrapTenant(ctx, dbquery.BootstrapTenantParams{TenantID: tenantID, Name: request.TenantName}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "tenant_name_key" {
			return Principal{}, ErrTenantExists
		}
		return Principal{}, fmt.Errorf("create bootstrap tenant: %w", err)
	}
	userID, err := queries.BootstrapUser(ctx, dbquery.BootstrapUserParams{TenantID: tenantID, DisplayName: request.DisplayName})
	if err != nil {
		return Principal{}, fmt.Errorf("create bootstrap user: %w", err)
	}
	if err := queries.BootstrapLocalAccount(ctx, dbquery.BootstrapLocalAccountParams{
		TenantID: tenantID, UserID: userID, Login: request.Login, PasswordHash: passwordHash,
	}); err != nil {
		return Principal{}, fmt.Errorf("create local account: %w", err)
	}
	if err := audit(ctx, queries, Principal{TenantID: tenantID, UserID: userID}, "identity.bootstrap", userID, "allow"); err != nil {
		return Principal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Principal{}, fmt.Errorf("commit identity bootstrap: %w", err)
	}
	return Principal{TenantID: tenantID, UserID: userID}, nil
}

func (s *Service) Login(ctx context.Context, tenantID int64, login string, password []byte) (_ Session, resultErr error) {
	login = strings.TrimSpace(login)
	if tenantID <= 0 || !validText(login, 128) || len(password) == 0 || len(password) > passwordMaxBytes {
		return Session{}, ErrUnauthenticated
	}
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return Session{}, err
	}
	defer rollback(tx, &resultErr)
	tenantExists, err := queries.IdentityTenantExists(ctx, tenantID)
	if err != nil {
		return Session{}, fmt.Errorf("check login tenant: %w", err)
	}
	row, lookupErr := queries.LookupLocalAccount(ctx, dbquery.LookupLocalAccountParams{TenantID: tenantID, Login: login})
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		return Session{}, fmt.Errorf("read local account: %w", lookupErr)
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit login lookup: %w", err)
	}
	found := lookupErr == nil
	checkedPassword := found && !row.Disabled && !row.Locked
	encoded := s.dummy
	if checkedPassword {
		encoded = row.PasswordHash
	}
	match, err := s.hasher.Verify(ctx, password, encoded)
	if err != nil {
		return Session{}, fmt.Errorf("verify local password: %w", err)
	}
	if !tenantExists {
		return Session{}, ErrUnauthenticated
	}
	var tokenBytes [32]byte
	if checkedPassword && match {
		if _, err := rand.Read(tokenBytes[:]); err != nil {
			return Session{}, fmt.Errorf("generate session token: %w", err)
		}
	}
	return s.finishLogin(ctx, tenantID, login, row, checkedPassword, checkedPassword && match, tokenBytes[:])
}

func (s *Service) finishLogin(ctx context.Context, tenantID int64, login string, observed dbquery.LookupLocalAccountRow, checkedPassword, matched bool, tokenBytes []byte) (_ Session, resultErr error) {
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return Session{}, err
	}
	defer rollback(tx, &resultErr)
	current, err := queries.LockLocalAccount(ctx, dbquery.LockLocalAccountParams{TenantID: tenantID, Login: login})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Session{}, fmt.Errorf("lock local account: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, denyLogin(ctx, tx, queries, Principal{TenantID: tenantID}, 0)
	}
	principal := Principal{TenantID: tenantID, UserID: current.UserID}
	if !matched || current.Disabled || current.Locked || current.CredentialVersion != observed.CredentialVersion || current.PasswordHash != observed.PasswordHash {
		if checkedPassword && !matched && !current.Disabled && !current.Locked && current.UserID == observed.UserID &&
			current.CredentialVersion == observed.CredentialVersion && current.PasswordHash == observed.PasswordHash {
			if err := queries.LocalLoginFailed(ctx, dbquery.LocalLoginFailedParams{TenantID: tenantID, UserID: current.UserID}); err != nil {
				return Session{}, fmt.Errorf("record failed login: %w", err)
			}
		}
		return Session{}, denyLogin(ctx, tx, queries, principal, current.UserID)
	}
	digest := sha256.Sum256(tokenBytes)
	if err := queries.LocalLoginSucceeded(ctx, dbquery.LocalLoginSucceededParams{TenantID: tenantID, UserID: current.UserID}); err != nil {
		return Session{}, fmt.Errorf("reset failed login count: %w", err)
	}
	if err := queries.CreateSession(ctx, dbquery.CreateSessionParams{
		TenantID: tenantID, UserID: current.UserID, TokenHash: digest[:], AbsoluteSeconds: s.config.AbsoluteTimeout.Seconds(),
	}); err != nil {
		return Session{}, fmt.Errorf("create login session: %w", err)
	}
	if err := audit(ctx, queries, principal, "identity.login", current.UserID, "allow"); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit login: %w", err)
	}
	token := strconv.FormatInt(tenantID, 10) + "." + base64.RawURLEncoding.EncodeToString(tokenBytes)
	csrf := csrfFor(tokenBytes)
	return Session{Principal: principal, Token: token, CSRF: csrf}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (_ Session, resultErr error) {
	tenantID, secret, err := parseToken(token)
	if err != nil {
		return Session{}, ErrUnauthenticated
	}
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return Session{}, err
	}
	defer rollback(tx, &resultErr)
	digest := sha256.Sum256(secret)
	userID, err := queries.AuthenticateSession(ctx, dbquery.AuthenticateSessionParams{TenantID: tenantID, TokenHash: digest[:], IdleSeconds: s.config.IdleTimeout.Seconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, fmt.Errorf("authenticate session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit session authentication: %w", err)
	}
	return Session{Principal: Principal{TenantID: tenantID, UserID: userID}, CSRF: csrfFor(secret)}, nil
}

func (s *Service) Logout(ctx context.Context, token string) (resultErr error) {
	tenantID, secret, err := parseToken(token)
	if err != nil {
		return ErrUnauthenticated
	}
	tx, queries, err := s.begin(ctx, tenantID)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	digest := sha256.Sum256(secret)
	userID, err := queries.RevokeSession(ctx, dbquery.RevokeSessionParams{TenantID: tenantID, TokenHash: digest[:]})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if err := audit(ctx, queries, Principal{TenantID: tenantID, UserID: userID}, "identity.logout", userID, "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit logout: %w", err)
	}
	return nil
}

func ValidCSRF(token, submitted string) bool {
	_, secret, err := parseToken(token)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(csrfFor(secret)), []byte(submitted)) == 1
}

func (s *Service) begin(ctx context.Context, tenantID int64) (pgx.Tx, *dbquery.Queries, error) {
	if tenantID <= 0 {
		return nil, nil, ErrUnauthenticated
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin identity transaction: %w", err)
	}
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, tenantID); err != nil {
		result := fmt.Errorf("set identity tenant: %w", err)
		rollback(tx, &result)
		return nil, nil, result
	}
	return tx, queries, nil
}

func rollback(tx pgx.Tx, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*result = errors.Join(*result, fmt.Errorf("rollback identity transaction: %w", err))
	}
}

func denyLogin(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, principal Principal, objectID int64) error {
	if err := audit(ctx, queries, principal, "identity.login", objectID, "deny"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit login denial: %w", err)
	}
	return ErrUnauthenticated
}

func audit(ctx context.Context, queries *dbquery.Queries, principal Principal, action string, objectID int64, result string) error {
	if err := queries.AuditIdentityAction(ctx, dbquery.AuditIdentityActionParams{
		TenantID: principal.TenantID, UserID: principal.UserID, Action: action, ObjectID: objectID, Result: result,
	}); err != nil {
		return fmt.Errorf("audit identity action: %w", err)
	}
	return nil
}

func parseToken(token string) (int64, []byte, error) {
	if len(token) > 64 {
		return 0, nil, ErrUnauthenticated
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, nil, ErrUnauthenticated
	}
	tenantID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || tenantID <= 0 || strconv.FormatInt(tenantID, 10) != parts[0] {
		return 0, nil, ErrUnauthenticated
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != parts[1] {
		return 0, nil, ErrUnauthenticated
	}
	return tenantID, secret, nil
}

func csrfFor(secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("tuskrag.session.csrf.v1")) // hash.Hash.Write never returns an error.
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
