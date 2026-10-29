package tokenizer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

type trieNode struct {
	first, count uint32
	id           int32
}
type trieEdge struct {
	child uint32
	label byte
}
type buildNode struct {
	head, count uint32
	id          int32
}
type buildEdge struct {
	child, next uint32
	label       byte
}

// A compact byte trie avoids one 256-pointer array per vocabulary prefix.
type unigram struct {
	nodes        []trieNode
	edges        []trieEdge
	scores       []float64
	unknownScore float64
}

func loadUnigram(ctx context.Context, raw json.RawMessage) (*unigram, error) {
	var spec struct {
		Type         string          `json:"type"`
		Vocab        json.RawMessage `json:"vocab"`
		UnkID        int             `json:"unk_id"`
		ByteFallback bool            `json:"byte_fallback"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("decode unigram model: %w", err)
	}
	if spec.Type != "Unigram" || spec.UnkID != 3 || spec.ByteFallback {
		return nil, errors.New("unexpected BGE-M3 unigram model")
	}
	decoder := json.NewDecoder(bytes.NewReader(spec.Vocab))
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return nil, errors.New("invalid unigram vocabulary")
	}
	nodes := []buildNode{{id: -1}}
	edges := []buildEdge{{}}
	scores := make([]float64, 0, 250002)
	minimum := math.Inf(1)
	for decoder.More() {
		if len(scores)%512 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
			return nil, errors.New("invalid unigram vocabulary entry")
		}
		var piece string
		var score float64
		if err := decoder.Decode(&piece); err != nil {
			return nil, fmt.Errorf("decode unigram piece: %w", err)
		}
		if err := decoder.Decode(&score); err != nil {
			return nil, fmt.Errorf("decode unigram score: %w", err)
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return nil, errors.New("invalid unigram vocabulary entry end")
		}
		if piece == "" || !utf8.ValidString(piece) || math.IsNaN(score) || math.IsInf(score, 0) || len(scores) >= math.MaxInt32 {
			return nil, errors.New("invalid unigram vocabulary value")
		}
		id := int32(len(scores))
		scores = append(scores, score)
		if score < minimum {
			minimum = score
		}
		current := uint32(0)
		for index := 0; index < len(piece); index++ {
			label := piece[index]
			found := uint32(0)
			for edge := nodes[current].head; edge != 0; edge = edges[edge].next {
				if edges[edge].label == label {
					found = edges[edge].child
					break
				}
			}
			if found == 0 {
				if uint64(len(nodes)) >= math.MaxUint32 || uint64(len(edges)) >= math.MaxUint32 {
					return nil, errors.New("unigram trie exceeds size limit")
				}
				found = uint32(len(nodes))
				nodes = append(nodes, buildNode{id: -1})
				edges = append(edges, buildEdge{child: found, next: nodes[current].head, label: label})
				nodes[current].head = uint32(len(edges) - 1)
				nodes[current].count++
			}
			current = found
		}
		nodes[current].id = id
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') || len(scores) != 250002 {
		return nil, errors.New("unexpected unigram vocabulary size")
	}
	model := &unigram{nodes: make([]trieNode, len(nodes)), edges: make([]trieEdge, len(edges)-1), scores: scores, unknownScore: minimum - 10}
	var offset uint32
	for index, node := range nodes {
		if index%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		model.nodes[index] = trieNode{first: offset, count: node.count, id: node.id}
		begin := offset
		for edge := node.head; edge != 0; edge = edges[edge].next {
			model.edges[offset] = trieEdge{child: edges[edge].child, label: edges[edge].label}
			offset++
		}
		slices.SortFunc(model.edges[begin:offset], func(a, b trieEdge) int { return int(a.label) - int(b.label) })
	}
	return model, nil
}

func (m *unigram) child(node uint32, label byte) (uint32, bool) {
	n := m.nodes[node]
	lo, hi := int(n.first), int(n.first+n.count)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if m.edges[mid].label < label {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < int(n.first+n.count) && m.edges[lo].label == label {
		return m.edges[lo].child, true
	}
	return 0, false
}

type bestNode struct {
	score float64
	from  int
	id    int32
	set   bool
}

// encode uses the reference unigram Viterbi score and fuses adjacent unknowns.
func (m *unigram) encode(ctx context.Context, text string) ([]int, error) {
	if text == "" {
		return nil, nil
	}
	dp := make([]bestNode, len(text)+1)
	steps := 0
	for pos := 0; pos < len(text); {
		if steps%512 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		steps++
		if pos > 0 && !dp[pos].set {
			return nil, errors.New("unreachable unigram position")
		}
		_, size := utf8.DecodeRuneInString(text[pos:])
		node := uint32(0)
		hasSingle := false
		for end := pos; end < len(text); end++ {
			var ok bool
			node, ok = m.child(node, text[end])
			if !ok {
				break
			}
			id := m.nodes[node].id
			if id < 0 {
				continue
			}
			score := dp[pos].score + m.scores[id]
			target := &dp[end+1]
			if !target.set || score > target.score {
				*target = bestNode{score: score, from: pos, id: id, set: true}
			}
			if end+1-pos == size {
				hasSingle = true
			}
		}
		if !hasSingle {
			target := &dp[pos+size]
			score := dp[pos].score + m.unknownScore
			if !target.set || score > target.score {
				*target = bestNode{score: score, from: pos, id: 3, set: true}
			}
		}
		pos += size
	}
	var result []int
	previousUnknown := false
	steps = 0
	for end := len(text); end > 0; {
		if steps%512 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		steps++
		item := dp[end]
		if !item.set || item.from >= end {
			return nil, errors.New("invalid unigram path")
		}
		unknown := item.id == 3
		if !unknown || !previousUnknown {
			result = append(result, int(item.id))
		}
		previousUnknown = unknown
		end = item.from
	}
	slices.Reverse(result)
	return result, nil
}

func (m *unigram) tokenize(ctx context.Context, text string) ([]int, error) {
	if text == "" {
		return nil, nil
	}
	text = strings.ReplaceAll(text, " ", "▁")
	if !strings.HasPrefix(text, "▁") {
		text = "▁" + text
	}
	var result []int
	start, segments := 0, 0
	appendSegment := func(end int) error {
		ids, err := m.encode(ctx, text[start:end])
		if err != nil {
			return err
		}
		result = append(result, ids...)
		start = end
		return nil
	}
	for index, char := range text {
		if char == '▁' && index > start {
			if segments%512 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			segments++
			if err := appendSegment(index); err != nil {
				return nil, err
			}
		}
	}
	if err := appendSegment(len(text)); err != nil {
		return nil, err
	}
	return result, nil
}
