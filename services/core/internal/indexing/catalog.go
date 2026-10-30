package indexing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
)

var (
	ErrIndexMissing        = errors.New("required search index is missing")
	ErrIndexConflict       = errors.New("search index name has an unexpected definition")
	ErrIndexInvalid        = errors.New("search index is not valid and ready")
	ErrIndexBusy           = errors.New("search index provisioning is in progress")
	ErrUnsupportedDistance = errors.New("embedding distance is not supported by the index")
)

const lexicalIndexName = "tuskrag_chunk_bm25_v2"

// catalogReader is implemented by a pgx transaction or connection.
type catalogReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type definition struct {
	name string
	ddl  string
	want string
}

func indexDefinitions(spaceID int64, dimensions int32, distance string) ([]definition, error) {
	if spaceID <= 0 || dimensions < 64 || dimensions > 4000 {
		return nil, errors.New("invalid embedding space or dimensions")
	}
	if distance != "cosine" {
		return nil, ErrUnsupportedDistance
	}
	vectorName := fmt.Sprintf("tuskrag_chunk_vector_hnsw_s%d", spaceID)
	return []definition{
		{
			name: lexicalIndexName,
			ddl: "CREATE INDEX CONCURRENTLY " + lexicalIndexName + " ON public.chunk USING bm25 " +
				"(id, (search_text::pdb.jieba), tenant_id, kb_id, version_id, is_current, kb_access, access_tags, doc_tags) " +
				"WITH (key_field = 'id') WHERE is_current",
			want: "CREATE INDEX " + lexicalIndexName + " ON public.chunk USING bm25 " +
				"(id, ((search_text)::pdb.jieba), tenant_id, kb_id, version_id, is_current, kb_access, access_tags, doc_tags) " +
				"WITH (key_field=id) WHERE is_current",
		},
		{
			name: vectorName,
			ddl: fmt.Sprintf("CREATE INDEX CONCURRENTLY %s ON public.chunk_vector USING hnsw "+
				"((embedding::halfvec(%d)) halfvec_cosine_ops) WITH (m = 16, ef_construction = 64) "+
				"WHERE space_id = %d AND is_current", vectorName, dimensions, spaceID),
			want: fmt.Sprintf("CREATE INDEX %s ON public.chunk_vector USING hnsw "+
				"(((embedding)::halfvec(%d)) halfvec_cosine_ops) WITH (m='16', ef_construction='64') "+
				"WHERE ((space_id = %d) AND is_current)", vectorName, dimensions, spaceID),
		},
	}, nil
}

// Check requires an explicit transaction and holds a shared provisioning lock
// until it ends. The caller must validate tenant visibility and the profile.
func Check(ctx context.Context, catalog dbquery.DBTX, spaceID int64, dimensions int32, distance string) error {
	if ctx == nil || catalog == nil {
		return errors.New("context and index catalog are required")
	}
	definitions, err := indexDefinitions(spaceID, dimensions, distance)
	if err != nil {
		return err
	}
	if _, err := catalog.Exec(ctx, `SET LOCAL search_path = pg_catalog, public`); err != nil {
		return fmt.Errorf("set index catalog search path: %w", err)
	}
	locked, err := dbquery.New(catalog).TryLockIndexUse(ctx)
	if err != nil {
		return fmt.Errorf("lock index use: %w", err)
	}
	if !locked {
		return ErrIndexBusy
	}
	for _, item := range definitions {
		if err := checkDefinition(ctx, catalog, item); err != nil {
			return err
		}
	}
	return nil
}

func checkDefinition(ctx context.Context, catalog catalogReader, item definition) error {
	var kind, actual string
	var valid, ready, live bool
	// sqlc's offline schema does not include PostgreSQL's system catalogs.
	err := catalog.QueryRow(ctx, `SELECT c.relkind::text, COALESCE(i.indisvalid, false),
		COALESCE(i.indisready, false), COALESCE(i.indislive, false),
		COALESCE(pg_get_indexdef(i.indexrelid), '')
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_index i ON i.indexrelid = c.oid
		WHERE n.nspname = 'public' AND c.relname = $1`, item.name).Scan(&kind, &valid, &ready, &live, &actual)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", item.name, ErrIndexMissing)
	}
	if err != nil {
		return fmt.Errorf("inspect index %s: %w", item.name, err)
	}
	if kind != "i" || actual != item.want {
		return fmt.Errorf("%s: %w", item.name, ErrIndexConflict)
	}
	if !valid || !ready || !live {
		return fmt.Errorf("%s: %w", item.name, ErrIndexInvalid)
	}
	return nil
}
