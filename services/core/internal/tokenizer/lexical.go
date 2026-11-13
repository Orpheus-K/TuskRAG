package tokenizer

import (
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

// LexicalTokenizer pins gse and its embedded simplified-Chinese dictionary for
// PostgreSQL tsvector/tsquery lexical search.
type LexicalTokenizer struct {
	mu        sync.Mutex
	segmenter gse.Segmenter
}

func NewLexicalTokenizer() (*LexicalTokenizer, error) {
	segmenter, err := gse.NewEmbed("zh_s")
	if err != nil {
		return nil, err
	}
	return &LexicalTokenizer{segmenter: segmenter}, nil
}

func (l *LexicalTokenizer) Tokens(text string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	words := l.segmenter.CutSearch(strings.ToLower(text), true)
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

func (l *LexicalTokenizer) TSVectorText(text string) string {
	return strings.Join(l.Tokens(text), " ")
}

func (l *LexicalTokenizer) Query(text string) string {
	words := l.Tokens(text)
	for i, w := range words {
		words[i] = "'" + w + "'"
	}
	return strings.Join(words, " | ")
}
