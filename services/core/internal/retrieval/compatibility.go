package retrieval

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/secrets"
	"tuskrag.local/core/internal/tokenizer"
)

// CompatibilityTokenizer pins gse and its embedded simplified-Chinese dictionary.
type CompatibilityTokenizer struct {
	*tokenizer.LexicalTokenizer
}

func NewCompatibilityTokenizer() (*CompatibilityTokenizer, error) {
	lex, err := tokenizer.NewLexicalTokenizer()
	if err != nil {
		return nil, err
	}
	return &CompatibilityTokenizer{LexicalTokenizer: lex}, nil
}

// NewCompatibilityEvaluationService selects the tsvector ablation explicitly.
// The caller must populate lexical_tokens with the same tokenizer and provision
// its GIN index.
func NewCompatibilityEvaluationService(pool *pgxpool.Pool, vault *secrets.Vault, count TokenCounter, lexical *CompatibilityTokenizer) (*Service, error) {
	service, err := NewService(pool, vault, count)
	if err != nil {
		return nil, err
	}
	if lexical == nil {
		return nil, ErrInvalidQuery
	}
	service.driver.compatibility = lexical
	return service, nil
}
