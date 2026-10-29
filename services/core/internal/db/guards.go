package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrVersionGuards = errors.New("document version guards are unavailable")

type guardReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// CheckVersionGuards verifies the database protections needed before an
// immutable original can be deleted or a new version can be published.
func CheckVersionGuards(ctx context.Context, reader guardReader) error {
	if ctx == nil || reader == nil {
		return errors.New("context and database guard reader are required")
	}
	var ready bool
	err := reader.QueryRow(ctx, `SELECT current_setting('session_replication_role') = 'origin' AND count(*) = 3
		FROM pg_catalog.pg_trigger t
		JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
		JOIN pg_catalog.pg_namespace pn ON pn.oid = p.pronamespace
		WHERE n.nspname = 'public' AND pn.nspname = 'public' AND NOT t.tgisinternal
		  AND t.tgenabled IN ('O', 'A') AND NOT p.prosecdef
		  AND p.proconfig @> ARRAY['search_path=pg_catalog, public, pg_temp']::text[]
		  AND ((c.relname = 'document' AND t.tgname = 'document_current_version_guard'
		        AND t.tgtype = 23 AND p.proname = 'tuskrag_guard_current_version')
		    OR (c.relname = 'document_version' AND t.tgname = 'document_version_status_guard'
		        AND t.tgtype = 19 AND p.proname = 'tuskrag_guard_version_status')
		    OR (c.relname = 'document_version' AND t.tgname = 'document_header_confirmation_guard'
		        AND t.tgtype = 19 AND p.proname = 'tuskrag_guard_header_confirmation'
		        AND t.tgattr::text = (SELECT a.attnum::text FROM pg_catalog.pg_attribute a
		          WHERE a.attrelid = c.oid AND a.attname = 'confirm_first_row_headers' AND NOT a.attisdropped)))`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("inspect document version guards: %w", err)
	}
	if !ready {
		return ErrVersionGuards
	}
	return nil
}
