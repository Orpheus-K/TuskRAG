package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	sp "github.com/tggo/goSentencePiece"
)

type sample struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Reference []int  `json:"reference_ids"`
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: tokenizer-spike tokenizer.json reference.json")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != "21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08" {
		return fmt.Errorf("unexpected tokenizer checksum")
	}
	model, err := sp.NewTokenizerFromJSONReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(os.Args[2])
	if err != nil {
		return err
	}
	var samples []sample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return err
	}
	type result struct {
		ID             string `json:"id"`
		ReferenceCount int    `json:"reference_count"`
		CandidateCount int    `json:"candidate_count"`
		Equal          bool   `json:"equal"`
	}
	results := make([]result, 0, len(samples))
	for _, s := range samples {
		ids := model.EncodeWithOptions(s.Text, true).IDs
		results = append(results, result{ID: s.ID, ReferenceCount: len(s.Reference), CandidateCount: len(ids), Equal: reflect.DeepEqual(s.Reference, ids)})
	}
	return json.NewEncoder(os.Stdout).Encode(results)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
