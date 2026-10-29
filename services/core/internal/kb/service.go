package kb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/models"
)

var (
	ErrNotFound            = errors.New("knowledge base or user not found")
	ErrForbidden           = errors.New("knowledge base action forbidden")
	ErrLastOwner           = errors.New("knowledge base must retain an owner")
	ErrNameTaken           = errors.New("knowledge base name already exists")
	ErrInvalidRequest      = errors.New("invalid knowledge base request")
	ErrIdempotencyConflict = errors.New("idempotency key used for a different request")
)

type Role string

const (
	Owner  Role = "owner"
	Admin  Role = "admin"
	Editor Role = "editor"
	Viewer Role = "viewer"
)

// Actor must be constructed from an authenticated identity by the caller.
type Actor struct {
	TenantID int64
	UserID   int64
}

type KnowledgeBase struct {
	ID               int64
	TenantID         int64
	Name             string
	EmbeddingSpaceID int64
	ConfigRevision   int32
	EgressPolicy     models.EgressPolicy
	Status           string
	CreatedAt        time.Time
	Role             Role
}

type CreateRequest struct {
	Name             string
	EmbeddingSpaceID int64
	EgressPolicy     models.EgressPolicy
	IdempotencyKey   string
}

type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) (*Service, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return &Service{pool: pool}, nil
}

func (s *Service) begin(ctx context.Context, actor Actor) (pgx.Tx, *dbquery.Queries, error) {
	if actor.TenantID <= 0 || actor.UserID <= 0 {
		return nil, nil, errors.New("valid tenant and user are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin knowledge base transaction: %w", err)
	}
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, actor.TenantID); err != nil {
		result := fmt.Errorf("set knowledge base tenant: %w", err)
		rollback(tx, &result)
		return nil, nil, result
	}
	return tx, queries, nil
}

func rollback(tx pgx.Tx, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*result = errors.Join(*result, fmt.Errorf("rollback knowledge base transaction: %w", err))
	}
}

func (s *Service) Create(ctx context.Context, actor Actor, request CreateRequest) (_ KnowledgeBase, resultErr error) {
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || request.EmbeddingSpaceID <= 0 || !validIdempotencyKey(request.IdempotencyKey) {
		return KnowledgeBase{}, ErrInvalidRequest
	}
	if _, err := models.PolicyWithin(models.Allow, request.EgressPolicy); err != nil {
		return KnowledgeBase{}, err
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return KnowledgeBase{}, err
	}
	defer rollback(tx, &resultErr)
	exists, err := queries.UserExists(ctx, dbquery.UserExistsParams{TenantID: actor.TenantID, UserID: actor.UserID})
	if err != nil {
		return KnowledgeBase{}, fmt.Errorf("check actor: %w", err)
	}
	if !exists {
		return KnowledgeBase{}, ErrNotFound
	}
	tenantPolicy, err := queries.TenantEgressPolicy(ctx, actor.TenantID)
	if err != nil {
		return KnowledgeBase{}, fmt.Errorf("read tenant egress policy: %w", err)
	}
	within, err := models.PolicyWithin(models.EgressPolicy(tenantPolicy), request.EgressPolicy)
	if err != nil {
		return KnowledgeBase{}, fmt.Errorf("check tenant egress policy: %w", err)
	}
	if !within {
		return KnowledgeBase{}, deny(ctx, tx, queries, actor, 0, 0, "", "kb.create", models.ErrEgressDenied)
	}
	exists, err = queries.EmbeddingSpaceExists(ctx, dbquery.EmbeddingSpaceExistsParams{TenantID: actor.TenantID, SpaceID: request.EmbeddingSpaceID})
	if err != nil {
		return KnowledgeBase{}, fmt.Errorf("check embedding space: %w", err)
	}
	if !exists {
		return KnowledgeBase{}, ErrNotFound
	}
	if request.IdempotencyKey != "" {
		encoded, err := json.Marshal(struct {
			Name             string
			EmbeddingSpaceID int64
			EgressPolicy     models.EgressPolicy
		}{request.Name, request.EmbeddingSpaceID, request.EgressPolicy})
		if err != nil {
			return KnowledgeBase{}, fmt.Errorf("encode knowledge base request: %w", err)
		}
		hash := sha256.Sum256(encoded)
		_, err = queries.ReserveIdempotency(ctx, dbquery.ReserveIdempotencyParams{
			TenantID: actor.TenantID, UserID: actor.UserID, Operation: "kb.create", RequestKey: request.IdempotencyKey, RequestHash: hash[:],
		})
		if errors.Is(err, pgx.ErrNoRows) {
			previous, findErr := queries.FindIdempotency(ctx, dbquery.FindIdempotencyParams{
				TenantID: actor.TenantID, UserID: actor.UserID, Operation: "kb.create", RequestKey: request.IdempotencyKey,
			})
			if findErr != nil {
				return KnowledgeBase{}, fmt.Errorf("find idempotency record: %w", findErr)
			}
			if !bytes.Equal(previous.RequestHash, hash[:]) {
				return KnowledgeBase{}, ErrIdempotencyConflict
			}
			if !previous.ObjectID.Valid {
				return KnowledgeBase{}, errors.New("incomplete idempotency record")
			}
			visible, findErr := queries.GetVisibleKnowledgeBase(ctx, dbquery.GetVisibleKnowledgeBaseParams{
				TenantID: actor.TenantID, KbID: previous.ObjectID.Int64, UserID: actor.UserID,
			})
			if errors.Is(findErr, pgx.ErrNoRows) {
				return KnowledgeBase{}, deny(ctx, tx, queries, actor, previous.ObjectID.Int64, 0, "", "kb.create.replay", ErrNotFound)
			}
			if findErr != nil {
				return KnowledgeBase{}, fmt.Errorf("find idempotent knowledge base: %w", findErr)
			}
			if err := audit(ctx, queries, actor, visible.ID, 0, "", "kb.create.replay", "allow"); err != nil {
				return KnowledgeBase{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return KnowledgeBase{}, fmt.Errorf("commit knowledge base replay: %w", err)
			}
			return fromVisible(visible), nil
		}
		if err != nil {
			return KnowledgeBase{}, fmt.Errorf("reserve idempotency key: %w", err)
		}
	}
	row, err := queries.CreateKnowledgeBase(ctx, dbquery.CreateKnowledgeBaseParams{
		TenantID: actor.TenantID, Name: request.Name, EmbeddingSpaceID: request.EmbeddingSpaceID, EgressPolicy: string(request.EgressPolicy),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "knowledge_base_tenant_id_name_key" {
			return KnowledgeBase{}, ErrNameTaken
		}
		return KnowledgeBase{}, fmt.Errorf("create knowledge base: %w", err)
	}
	if err := queries.AddKnowledgeBaseMember(ctx, dbquery.AddKnowledgeBaseMemberParams{
		TenantID: actor.TenantID, KbID: row.ID, UserID: actor.UserID, Role: string(Owner),
	}); err != nil {
		return KnowledgeBase{}, fmt.Errorf("add knowledge base owner: %w", err)
	}
	if request.IdempotencyKey != "" {
		updated, err := queries.CompleteIdempotency(ctx, dbquery.CompleteIdempotencyParams{
			TenantID: actor.TenantID, UserID: actor.UserID, Operation: "kb.create", RequestKey: request.IdempotencyKey,
			ObjectID: pgtype.Int8{Int64: row.ID, Valid: true},
		})
		if err != nil {
			return KnowledgeBase{}, fmt.Errorf("complete idempotency record: %w", err)
		}
		if updated != 1 {
			return KnowledgeBase{}, errors.New("idempotency record not completed")
		}
	}
	if err := audit(ctx, queries, actor, row.ID, 0, "", "kb.create", "allow"); err != nil {
		return KnowledgeBase{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return KnowledgeBase{}, fmt.Errorf("commit knowledge base creation: %w", err)
	}
	return KnowledgeBase{
		ID: row.ID, TenantID: row.TenantID, Name: row.Name, EmbeddingSpaceID: row.EmbeddingSpaceID,
		ConfigRevision: row.ConfigRevision, EgressPolicy: models.EgressPolicy(row.EgressPolicy),
		Status: row.Status, CreatedAt: row.CreatedAt.Time, Role: Owner,
	}, nil
}

func validIdempotencyKey(key string) bool {
	if len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func (s *Service) List(ctx context.Context, actor Actor) (_ []KnowledgeBase, resultErr error) {
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer rollback(tx, &resultErr)
	rows, err := queries.ListVisibleKnowledgeBases(ctx, dbquery.ListVisibleKnowledgeBasesParams{TenantID: actor.TenantID, UserID: actor.UserID})
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	result := make([]KnowledgeBase, 0, len(rows))
	for _, row := range rows {
		result = append(result, fromVisible(dbquery.GetVisibleKnowledgeBaseRow(row)))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit knowledge base listing: %w", err)
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, actor Actor, kbID int64) (_ KnowledgeBase, resultErr error) {
	if kbID <= 0 {
		return KnowledgeBase{}, errors.New("knowledge base ID must be positive")
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return KnowledgeBase{}, err
	}
	defer rollback(tx, &resultErr)
	row, err := queries.GetVisibleKnowledgeBase(ctx, dbquery.GetVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeBase{}, deny(ctx, tx, queries, actor, kbID, 0, "", "kb.read", ErrNotFound)
	}
	if err != nil {
		return KnowledgeBase{}, fmt.Errorf("get knowledge base: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return KnowledgeBase{}, fmt.Errorf("commit knowledge base read: %w", err)
	}
	return fromVisible(row), nil
}

func (s *Service) Archive(ctx context.Context, actor Actor, kbID int64) (resultErr error) {
	if kbID <= 0 {
		return errors.New("knowledge base ID must be positive")
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	if err := requireOwner(ctx, queries, actor, kbID); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return deny(ctx, tx, queries, actor, kbID, 0, "", "kb.archive", err)
		}
		return err
	}
	if err := queries.ArchiveKnowledgeBase(ctx, dbquery.ArchiveKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID}); err != nil {
		return fmt.Errorf("archive knowledge base: %w", err)
	}
	if err := audit(ctx, queries, actor, kbID, 0, "", "kb.archive", "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit knowledge base archive: %w", err)
	}
	return nil
}

func (s *Service) SetMember(ctx context.Context, actor Actor, kbID, userID int64, role Role) error {
	if !validRole(role) {
		return errors.New("invalid knowledge base role")
	}
	return s.changeMember(ctx, actor, kbID, userID, &role)
}

func (s *Service) RemoveMember(ctx context.Context, actor Actor, kbID, userID int64) error {
	return s.changeMember(ctx, actor, kbID, userID, nil)
}

func (s *Service) changeMember(ctx context.Context, actor Actor, kbID, userID int64, role *Role) (resultErr error) {
	if kbID <= 0 || userID <= 0 {
		return errors.New("knowledge base and user IDs must be positive")
	}
	action := "kb.member.remove"
	newRole := Role("")
	if role != nil {
		action, newRole = "kb.member.set", *role
	}
	tx, queries, err := s.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	if err := requireOwner(ctx, queries, actor, kbID); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return deny(ctx, tx, queries, actor, kbID, userID, newRole, action, err)
		}
		return err
	}
	exists, err := queries.UserExists(ctx, dbquery.UserExistsParams{TenantID: actor.TenantID, UserID: userID})
	if err != nil {
		return fmt.Errorf("check member: %w", err)
	}
	if !exists {
		return deny(ctx, tx, queries, actor, kbID, userID, newRole, action, ErrNotFound)
	}
	current, err := queries.GetMemberRole(ctx, dbquery.GetMemberRoleParams{TenantID: actor.TenantID, KbID: kbID, UserID: userID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read member role: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) && role == nil {
		return deny(ctx, tx, queries, actor, kbID, userID, newRole, action, ErrNotFound)
	}
	if Role(current) == Owner && (role == nil || *role != Owner) {
		owners, err := queries.CountKnowledgeBaseOwners(ctx, dbquery.CountKnowledgeBaseOwnersParams{TenantID: actor.TenantID, KbID: kbID})
		if err != nil {
			return fmt.Errorf("count knowledge base owners: %w", err)
		}
		if owners <= 1 {
			return deny(ctx, tx, queries, actor, kbID, userID, newRole, action, ErrLastOwner)
		}
	}
	if role == nil {
		if _, err := queries.RemoveKnowledgeBaseMember(ctx, dbquery.RemoveKnowledgeBaseMemberParams{TenantID: actor.TenantID, KbID: kbID, UserID: userID}); err != nil {
			return fmt.Errorf("remove knowledge base member: %w", err)
		}
	} else if err := queries.SetKnowledgeBaseMember(ctx, dbquery.SetKnowledgeBaseMemberParams{
		TenantID: actor.TenantID, KbID: kbID, UserID: userID, Role: string(*role),
	}); err != nil {
		return fmt.Errorf("set knowledge base member: %w", err)
	}
	if err := audit(ctx, queries, actor, kbID, userID, newRole, action, "allow"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit knowledge base membership: %w", err)
	}
	return nil
}

func requireOwner(ctx context.Context, queries *dbquery.Queries, actor Actor, kbID int64) error {
	if _, err := queries.LockVisibleKnowledgeBase(ctx, dbquery.LockVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID}); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock knowledge base: %w", err)
	}
	role, err := queries.LockMemberRole(ctx, dbquery.LockMemberRoleParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock actor membership: %w", err)
	}
	if Role(role) != Owner {
		return ErrForbidden
	}
	return nil
}

func audit(ctx context.Context, queries *dbquery.Queries, actor Actor, kbID, targetID int64, role Role, action, result string) error {
	if err := queries.AuditKnowledgeBaseAction(ctx, dbquery.AuditKnowledgeBaseActionParams{
		TenantID: actor.TenantID, UserID: actor.UserID, Action: action, KbID: kbID,
		TargetUserID: pgtype.Int8{Int64: targetID, Valid: targetID > 0}, NewRole: pgtype.Text{String: string(role), Valid: role != ""}, Result: result,
	}); err != nil {
		return fmt.Errorf("audit knowledge base action: %w", err)
	}
	return nil
}

func deny(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, actor Actor, kbID, targetID int64, role Role, action string, denial error) error {
	if err := audit(ctx, queries, actor, kbID, targetID, role, action, "deny"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit denial audit: %w", err)
	}
	return denial
}

func fromVisible(row dbquery.GetVisibleKnowledgeBaseRow) KnowledgeBase {
	return KnowledgeBase{
		ID: row.ID, TenantID: row.TenantID, Name: row.Name, EmbeddingSpaceID: row.EmbeddingSpaceID,
		ConfigRevision: row.ConfigRevision, EgressPolicy: models.EgressPolicy(row.EgressPolicy),
		Status: row.Status, CreatedAt: row.CreatedAt.Time, Role: Role(row.Role),
	}
}

func validRole(role Role) bool {
	return role == Owner || role == Admin || role == Editor || role == Viewer
}
