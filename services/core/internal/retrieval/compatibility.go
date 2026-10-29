package retrieval

import (
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
	"github.com/jackc/pgx/v5/pgxpool"
	"tuskrag.local/core/internal/secrets"
)

// CompatibilityTokenizer pins gse and its embedded simplified-Chinese dictionary.
// A mutex covers the upstream segmenter's internal scratch state.
type CompatibilityTokenizer struct {
	mu        sync.Mutex
	segmenter gse.Segmenter
}

func NewCompatibilityTokenizer() (*CompatibilityTokenizer, error) {
	segmenter, err := gse.NewEmbed("zh_s")
	if err != nil {
		return nil, err
	}
	return &CompatibilityTokenizer{segmenter: segmenter}, nil
}

func (c *CompatibilityTokenizer) Tokens(text string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	words := c.segmenter.CutSearch(strings.ToLower(text), true)
	result := make([]string, 0, len(words))
	for _, word := range words {
		// Keep only alphanumeric tokens: punctuation cannot become tsquery syntax.
		for _, part := range strings.FieldsFunc(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
			if part != "" {
				result = append(result, part)
			}
		}
	}
	return result
}

func (c *CompatibilityTokenizer) Query(text string) string {
	words := c.Tokens(text)
	for i, w := range words {
		words[i] = "'" + w + "'"
	}
	return strings.Join(words, " | ")
}

// NewCompatibilityEvaluationService selects the tsvector ablation explicitly.
// The caller must populate lexical_tokens with the same tokenizer and provision
// its GIN index. This is not a silent fallback or a standalone RDS deployment.
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
