// Package tokenizer contains bounded token counters for explicitly selected models.
package tokenizer

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	sp "github.com/tggo/goSentencePiece"
	grapheme16 "tuskrag.local/core/internal/tokenizer/grapheme16"
)

const (
	maxInputBytes      = 64 * 1024
	maxNormalizedBytes = 1024 * 1024
	maxTokenizerBytes  = 18 * 1024 * 1024
	bgeM3SHA256        = "21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08"
)

var (
	ErrInvalidInput  = errors.New("tokenizer input must be valid UTF-8 without NUL")
	ErrInputTooLarge = errors.New("tokenizer input exceeds size limit")
)

//go:embed assets/bge-m3-tokenizer.json.gz
var bgeM3Asset []byte

type BGEM3 struct {
	model      *unigram
	normalizer *precompiledNormalizer
	slots      chan struct{}
}

// NewBGEM3 loads the pinned BGE-M3 vocabulary, not model weights. The caller
// must verify that its embedding service uses the same tokenizer profile.
func NewBGEM3(ctx context.Context, concurrency int) (*BGEM3, error) {
	if ctx == nil || concurrency < 1 || concurrency > 16 {
		return nil, errors.New("context and tokenizer concurrency between 1 and 16 are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := readAsset(bgeM3Asset)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != bgeM3SHA256 {
		return nil, errors.New("BGE-M3 tokenizer checksum mismatch")
	}
	var spec struct {
		Normalizer struct {
			Type        string            `json:"type"`
			Normalizers []json.RawMessage `json:"normalizers"`
		} `json:"normalizer"`
		Model json.RawMessage `json:"model"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("decode tokenizer: %w", err)
	}
	if spec.Normalizer.Type != "Sequence" || len(spec.Normalizer.Normalizers) != 2 {
		return nil, errors.New("unexpected tokenizer normalizer")
	}
	norm, err := newPrecompiledNormalizer(spec.Normalizer.Normalizers[0])
	if err != nil {
		return nil, err
	}
	var replacement struct {
		Type    string `json:"type"`
		Pattern struct {
			Regex string `json:"Regex"`
		} `json:"pattern"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(spec.Normalizer.Normalizers[1], &replacement); err != nil || replacement.Type != "Replace" || replacement.Pattern.Regex != " {2,}" || replacement.Content != " " {
		return nil, errors.New("unexpected tokenizer space normalization")
	}
	model, err := loadUnigram(ctx, spec.Model)
	if err != nil {
		return nil, err
	}
	return &BGEM3{model: model, normalizer: norm, slots: make(chan struct{}, concurrency)}, nil
}

func readAsset(compressed []byte) (raw []byte, resultErr error) {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("open tokenizer asset: %w", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close tokenizer asset: %w", err))
		}
	}()
	raw, err = io.ReadAll(io.LimitReader(reader, maxTokenizerBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read tokenizer asset: %w", err)
	}
	if len(raw) > maxTokenizerBytes {
		return nil, errors.New("tokenizer asset exceeds size limit")
	}
	return raw, nil
}

// Count includes the model's beginning and end tokens.
func (t *BGEM3) Count(ctx context.Context, input string) (int, error) {
	ids, err := t.tokenIDs(ctx, input)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

type specialToken struct {
	text      string
	id        int
	stripLeft bool
}

var bgeSpecials = [...]specialToken{{"<s>", 0, false}, {"<pad>", 1, false}, {"</s>", 2, false}, {"<unk>", 3, false}, {"<mask>", 250001, true}}

func (t *BGEM3) tokenIDs(ctx context.Context, input string) ([]int, error) {
	if t == nil || t.model == nil || t.normalizer == nil || t.slots == nil || ctx == nil {
		return nil, errors.New("initialized tokenizer and context are required")
	}
	if len(input) > maxInputBytes {
		return nil, ErrInputTooLarge
	}
	if !utf8.ValidString(input) || strings.ContainsRune(input, '\x00') {
		return nil, ErrInvalidInput
	}
	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	result := []int{0}
	appendText := func(text string) error {
		if text == "" {
			return nil
		}
		normalized, err := t.normalizer.normalize(ctx, text)
		if err != nil {
			return err
		}
		ids, err := t.model.tokenize(ctx, normalized)
		if err != nil {
			return err
		}
		result = append(result, ids...)
		return nil
	}
	start, cursor := 0, 0
	for cursor < len(input) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		next := strings.IndexByte(input[cursor:], '<')
		if next < 0 {
			break
		}
		at := cursor + next
		var match *specialToken
		for index := range bgeSpecials {
			if strings.HasPrefix(input[at:], bgeSpecials[index].text) {
				match = &bgeSpecials[index]
				break
			}
		}
		if match == nil {
			cursor = at + 1
			continue
		}
		part := input[start:at]
		if match.stripLeft {
			part = strings.TrimRightFunc(part, unicode.IsSpace)
		}
		if err := appendText(part); err != nil {
			return nil, err
		}
		result = append(result, match.id)
		cursor = at + len(match.text)
		start = cursor
	}
	if err := appendText(input[start:]); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append(result, 2), nil
}

type precompiledNormalizer struct {
	trie         *sp.DartsDoubleArray
	replacements []byte
}

func newPrecompiledNormalizer(raw json.RawMessage) (*precompiledNormalizer, error) {
	var spec struct {
		Type string `json:"type"`
		Map  string `json:"precompiled_charsmap"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil || spec.Type != "Precompiled" {
		return nil, errors.New("unexpected tokenizer character map")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(spec.Map)
	if err != nil || len(data) < 4 {
		return nil, errors.New("invalid tokenizer character map")
	}
	size := int(binary.LittleEndian.Uint32(data[:4]))
	if size <= 0 || size%4 != 0 || size > len(data)-4 {
		return nil, errors.New("invalid tokenizer character map size")
	}
	return &precompiledNormalizer{trie: sp.NewDartsDoubleArray(data[4 : 4+size]), replacements: data[4+size:]}, nil
}

func (n *precompiledNormalizer) normalize(ctx context.Context, text string) (string, error) {
	var output strings.Builder
	transform := func(part string) (bool, error) {
		matches := n.trie.CommonPrefixSearchBytes([]byte(part))
		if len(matches) == 0 {
			return false, nil
		}
		offset := matches[0].Value
		if offset < 0 || offset >= len(n.replacements) {
			return false, errors.New("invalid tokenizer character map offset")
		}
		end := offset
		for end < len(n.replacements) && n.replacements[end] != 0 {
			end++
		}
		if end == len(n.replacements) {
			return false, errors.New("unterminated tokenizer character map entry")
		}
		if output.Len()+end-offset > maxNormalizedBytes {
			return false, ErrInputTooLarge
		}
		_, _ = output.Write(n.replacements[offset:end])
		return true, nil
	}
	remaining := text
	for index := 0; remaining != ""; index++ {
		if index%512 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		part, rest, err := grapheme16.Next(remaining)
		if err != nil {
			return "", fmt.Errorf("segment tokenizer input: %w", err)
		}
		remaining = rest
		if len(part) < 6 {
			matched, err := transform(part)
			if err != nil {
				return "", err
			}
			if matched {
				continue
			}
		}
		for _, char := range part {
			piece := string(char)
			matched, err := transform(piece)
			if err != nil {
				return "", err
			}
			if !matched {
				if output.Len()+len(piece) > maxNormalizedBytes {
					return "", ErrInputTooLarge
				}
				output.WriteString(piece)
			}
		}
	}
	value := output.String()
	if !strings.Contains(value, "  ") {
		return value, nil
	}
	var compact strings.Builder
	compact.Grow(len(value))
	previousSpace := false
	for index := 0; index < len(value); index++ {
		space := value[index] == ' '
		if !space || !previousSpace {
			compact.WriteByte(value[index])
		}
		previousSpace = space
	}
	return compact.String(), nil
}
