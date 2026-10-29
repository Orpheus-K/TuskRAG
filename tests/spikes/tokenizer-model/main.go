package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	sp "github.com/tggo/goSentencePiece"
)

const modelSHA256 = "cfc8146abe2a0488e9e2a0c56de7952f7c11ab059eca145a0a727afce0db2865"

type sample struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Reference []int  `json:"reference_ids"`
}

type special struct {
	text   string
	id     int
	lstrip bool
}

var specials = []special{{"<s>", 0, false}, {"<pad>", 1, false}, {"</s>", 2, false}, {"<unk>", 3, false}, {"<mask>", 250001, true}}

func mappedIDs(tokenizer *sp.Tokenizer, value string) ([]int, error) {
	ids, err := tokenizer.Encode(value)
	if err != nil {
		return nil, err
	}
	for index, id := range ids {
		switch id {
		case 0:
			ids[index] = 3
		case 1:
			ids[index] = 0
		case 2:
			ids[index] = 2
		default:
			ids[index] = id + 1
		}
	}
	return ids, nil
}

// This approximate adapter is an experiment; its Unicode behavior is not equivalent.
func approximateIDs(tokenizer *sp.Tokenizer, input string) ([]int, error) {
	result := []int{0}
	appendChunk := func(value string) error {
		if value == "" {
			return nil
		}
		ids, err := mappedIDs(tokenizer, value)
		if err != nil {
			return err
		}
		result = append(result, ids...)
		last, _ := utf8.DecodeLastRuneInString(value)
		if unicode.IsSpace(last) {
			result = append(result, 6)
		}
		return nil
	}
	for len(input) > 0 {
		at := -1
		var found special
		for _, item := range specials {
			index := strings.Index(input, item.text)
			if index >= 0 && (at < 0 || index < at) {
				at, found = index, item
			}
		}
		if at < 0 {
			if err := appendChunk(input); err != nil {
				return nil, err
			}
			break
		}
		chunk := input[:at]
		if found.lstrip {
			chunk = strings.TrimRightFunc(chunk, unicode.IsSpace)
		}
		if err := appendChunk(chunk); err != nil {
			return nil, err
		}
		result = append(result, found.id)
		input = input[at+len(found.text):]
	}
	return append(result, 2), nil
}

func run() error {
	if len(os.Args) != 4 || (os.Args[3] != "raw" && os.Args[3] != "approximate") {
		return fmt.Errorf("usage: tokenizer-model model reference.json raw|approximate")
	}
	model, err := os.ReadFile(os.Args[1])
	if err != nil {
		return err
	}
	hash := sha256.Sum256(model)
	if hex.EncodeToString(hash[:]) != modelSHA256 {
		return fmt.Errorf("unexpected model checksum")
	}
	tokenizer, err := sp.NewTokenizerFromReader(bytes.NewReader(model))
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(os.Args[2])
	if err != nil {
		return err
	}
	var samples []sample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return fmt.Errorf("decode reference samples: %w", err)
	}
	if len(samples) == 0 {
		return fmt.Errorf("empty reference samples")
	}
	var identical, sameCount, different int
	for _, sample := range samples {
		var ids []int
		if os.Args[3] == "raw" {
			ids, err = mappedIDs(tokenizer, sample.Text)
			ids = append(append([]int{0}, ids...), 2)
		} else {
			ids, err = approximateIDs(tokenizer, sample.Text)
		}
		if err != nil {
			return err
		}
		if reflect.DeepEqual(ids, sample.Reference) {
			identical++
		} else {
			different++
			if len(ids) == len(sample.Reference) {
				sameCount++
			}
		}
	}
	referenceHash := sha256.Sum256(raw)
	fmt.Printf("mode=%s samples=%d identical=%d mismatches=%d equal-count-mismatches=%d reference-sha256=%x\n", os.Args[3], len(samples), identical, different, sameCount, referenceHash)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
