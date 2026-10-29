package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

type sample struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Reference []int  `json:"reference_ids"`
}

func encode(model *tokenizer.Tokenizer, text string) (ids []int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	encoding, err := model.EncodeSingle(text, true)
	if err != nil {
		return nil, err
	}
	return encoding.Ids, nil
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
	model, err := pretrained.FromFile(os.Args[1])
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
	for _, s := range samples {
		ids, err := encode(model, s.Text)
		if err != nil {
			fmt.Printf("%s reference=%d error=%q\n", s.ID, len(s.Reference), err)
			continue
		}
		fmt.Printf("%s reference=%d actual=%d equal=%t\n", s.ID, len(s.Reference), len(ids), reflect.DeepEqual(s.Reference, ids))
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
