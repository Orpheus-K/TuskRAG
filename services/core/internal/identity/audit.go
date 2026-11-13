package identity

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
)

var (
	ErrAdminRequired       = errors.New("administrator access required")
	ErrInvalidAuditRequest = errors.New("invalid audit request")
)

type AuditLogFilter struct {
	Action      string
	ActorUserID int64
	ObjectType  string
	Result      string
	From        *time.Time
	To          *time.Time
	BeforeID    int64
	Limit       int32
}

type AuditLogEntry struct {
	ID           int64           `json:"id"`
	TenantID     int64           `json:"tenant_id"`
	ActorUserID  int64           `json:"actor_user_id"`
	ActorName    string          `json:"actor_name"`
	Action       string          `json:"action"`
	ObjectType   string          `json:"object_type"`
	ObjectID     int64           `json:"object_id"`
	Result       string          `json:"result"`
	TargetUserID *int64          `json:"target_user_id,omitempty"`
	NewRole      *string         `json:"new_role,omitempty"`
	Details      json.RawMessage `json:"details"`
	CreatedAt    time.Time       `json:"created_at"`
}

func (s *Service) requireAdminTx(ctx context.Context, principal Principal) (pgx.Tx, *dbquery.Queries, error) {
	if s == nil || s.pool == nil {
		return nil, nil, errors.New("identity service unavailable")
	}
	if principal.TenantID <= 0 || principal.UserID <= 0 {
		return nil, nil, ErrUnauthenticated
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin admin transaction: %w", err)
	}
	queries := dbquery.New(tx)
	if _, err := queries.SetTenant(ctx, principal.TenantID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, fmt.Errorf("set tenant: %w", err)
	}
	if _, err := queries.LockLocalAdministrator(ctx, dbquery.LockLocalAdministratorParams{
		TenantID: principal.TenantID,
		UserID:   principal.UserID,
	}); errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return nil, nil, ErrAdminRequired
	} else if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, fmt.Errorf("check administrator: %w", err)
	}
	return tx, queries, nil
}

func normalizeAuditFilter(f AuditLogFilter, defaultLimit, maxLimit int32) (dbquery.ListAuditLogsParams, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if f.Result != "" && f.Result != "allow" && f.Result != "deny" && f.Result != "error" {
		return dbquery.ListAuditLogsParams{}, ErrInvalidAuditRequest
	}
	params := dbquery.ListAuditLogsParams{
		LimitCount: limit,
	}
	if a := strings.TrimSpace(f.Action); a != "" {
		params.Action = pgtype.Text{String: a, Valid: true}
	}
	if f.ActorUserID > 0 {
		params.ActorUserID = pgtype.Int8{Int64: f.ActorUserID, Valid: true}
	}
	if ot := strings.TrimSpace(f.ObjectType); ot != "" {
		params.ObjectType = pgtype.Text{String: ot, Valid: true}
	}
	if f.Result != "" {
		params.Result = pgtype.Text{String: f.Result, Valid: true}
	}
	if f.From != nil {
		params.FromTime = pgtype.Timestamptz{Time: f.From.UTC(), Valid: true}
	}
	if f.To != nil {
		params.ToTime = pgtype.Timestamptz{Time: f.To.UTC(), Valid: true}
	}
	if f.BeforeID > 0 {
		params.BeforeID = pgtype.Int8{Int64: f.BeforeID, Valid: true}
	}
	return params, nil
}

func mapAuditRow(r dbquery.ListAuditLogsRow) AuditLogEntry {
	entry := AuditLogEntry{
		ID:          r.ID,
		TenantID:    r.TenantID,
		ActorUserID: r.ActorUserID,
		ActorName:   r.ActorName,
		Action:      r.Action,
		ObjectType:  r.ObjectType,
		ObjectID:    r.ObjectID,
		Result:      r.Result,
		Details:     r.Details,
		CreatedAt:   r.CreatedAt.Time.UTC(),
	}
	if len(entry.Details) == 0 {
		entry.Details = json.RawMessage("{}")
	}
	if r.TargetUserID.Valid {
		v := r.TargetUserID.Int64
		entry.TargetUserID = &v
	}
	if r.NewRole.Valid {
		v := r.NewRole.String
		entry.NewRole = &v
	}
	return entry
}

func (s *Service) ListAuditLogs(ctx context.Context, principal Principal, filter AuditLogFilter) ([]AuditLogEntry, error) {
	params, err := normalizeAuditFilter(filter, 50, 200)
	if err != nil {
		return nil, err
	}
	params.TenantID = principal.TenantID
	tx, queries, err := s.requireAdminTx(ctx, principal)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := queries.ListAuditLogs(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	items := make([]AuditLogEntry, 0, len(rows))
	for _, r := range rows {
		items = append(items, mapAuditRow(r))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit list audit logs: %w", err)
	}
	return items, nil
}

func (s *Service) ExportAuditLogs(ctx context.Context, principal Principal, filter AuditLogFilter, format string) ([]byte, string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "jsonl"
	}
	if format != "jsonl" && format != "csv" {
		return nil, "", ErrInvalidAuditRequest
	}
	params, err := normalizeAuditFilter(filter, 5000, 5000)
	if err != nil {
		return nil, "", err
	}
	params.TenantID = principal.TenantID
	tx, queries, err := s.requireAdminTx(ctx, principal)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := queries.ListAuditLogs(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("export audit logs: %w", err)
	}
	items := make([]AuditLogEntry, 0, len(rows))
	for _, r := range rows {
		items = append(items, mapAuditRow(r))
	}

	details, _ := json.Marshal(map[string]any{
		"format": format,
		"count":  len(items),
	})
	if err := queries.InsertAuditEvent(ctx, dbquery.InsertAuditEventParams{
		TenantID:    principal.TenantID,
		ActorUserID: principal.UserID,
		Action:      "audit.export",
		ObjectType:  "tenant",
		ObjectID:    principal.TenantID,
		Result:      "allow",
		Details:     details,
	}); err != nil {
		return nil, "", fmt.Errorf("write self audit for export: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", fmt.Errorf("commit export audit logs: %w", err)
	}

	if format == "csv" {
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"id", "created_at", "actor_user_id", "actor_name", "action", "object_type", "object_id", "result", "target_user_id", "new_role", "details"})
		for _, item := range items {
			target := ""
			if item.TargetUserID != nil {
				target = strconv.FormatInt(*item.TargetUserID, 10)
			}
			role := ""
			if item.NewRole != nil {
				role = *item.NewRole
			}
			_ = w.Write([]string{
				strconv.FormatInt(item.ID, 10),
				item.CreatedAt.Format(time.RFC3339Nano),
				strconv.FormatInt(item.ActorUserID, 10),
				item.ActorName,
				item.Action,
				item.ObjectType,
				strconv.FormatInt(item.ObjectID, 10),
				item.Result,
				target,
				role,
				string(item.Details),
			})
		}
		w.Flush()
		return buf.Bytes(), "text/csv; charset=utf-8", nil
	}

	var buf bytes.Buffer
	for _, item := range items {
		line, _ := json.Marshal(item)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), "application/x-ndjson; charset=utf-8", nil
}

func (s *Service) GetTenantEgressPolicy(ctx context.Context, principal Principal) (string, error) {
	tx, queries, err := s.requireAdminTx(ctx, principal)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	policy, err := queries.GetTenantEgressPolicy(ctx, principal.TenantID)
	if err != nil {
		return "", fmt.Errorf("get tenant egress policy: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit get tenant egress policy: %w", err)
	}
	return policy, nil
}

func (s *Service) UpdateTenantEgressPolicy(ctx context.Context, principal Principal, policy string) (string, error) {
	policy = strings.TrimSpace(policy)
	if policy != "deny" && policy != "internal_only" && policy != "allow" {
		return "", ErrInvalidAuditRequest
	}
	tx, queries, err := s.requireAdminTx(ctx, principal)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	affected, err := queries.UpdateTenantEgressPolicy(ctx, dbquery.UpdateTenantEgressPolicyParams{
		TenantID:        principal.TenantID,
		EgressPolicyMax: policy,
	})
	if err != nil {
		return "", fmt.Errorf("update tenant egress policy: %w", err)
	}
	if affected == 0 {
		return "", errors.New("tenant not found")
	}
	details, _ := json.Marshal(map[string]string{"egress_policy_max": policy})
	if err := queries.InsertAuditEvent(ctx, dbquery.InsertAuditEventParams{
		TenantID:    principal.TenantID,
		ActorUserID: principal.UserID,
		Action:      "tenant.egress_policy.update",
		ObjectType:  "tenant",
		ObjectID:    principal.TenantID,
		Result:      "allow",
		Details:     details,
	}); err != nil {
		return "", fmt.Errorf("audit tenant egress policy update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit update tenant egress policy: %w", err)
	}
	return policy, nil
}
