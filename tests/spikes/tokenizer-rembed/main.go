package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/rostamlabs/rembed/tokenizer/sentencepiece"
)

type sample struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Reference []int  `json:"reference_ids"`
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: tokenizer-rembed model reference.json")
	}
	model, err := os.ReadFile(os.Args[1])
	if err != nil {
		return err
	}
	hash := sha256.Sum256(model)
	if hex.EncodeToString(hash[:]) != "cfc8146abe2a0488e9e2a0c56de7952f7c11ab059eca145a0a727afce0db2865" {
		return fmt.Errorf("unexpected model checksum")
	}
	tokenizer, err := sentencepiece.New(os.Args[1])
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
	var identical, sameCount, different int
	for _, sample := range samples {
		values, _ := tokenizer.Encode(sample.Text, 1000000)
		ids := make([]int, len(values))
		for index, value := range values {
			ids[index] = int(value)
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
	fmt.Printf("samples=%d identical=%d mismatches=%d equal-count-mismatches=%d\n", len(samples), identical, different, sameCount)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
