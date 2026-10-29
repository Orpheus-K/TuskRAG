package parser

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

const validDocument = `{"schema_version":"1.0.0","pages":[{"no":1,"blocks":[{"type":"heading","level":1,"text":"标题"},{"type":"table","cells":[["列","值"]]}],"signals":{"char_count":4,"garbled_ratio":0}}],"meta":{"parser_profile":"test","created_at":"2026-10-28T00:00:00Z"}}`

func TestEmbeddedSchemaMatchesContract(t *testing.T) {
	contract, err := os.ReadFile("../../../../contracts/parsed-document.schema.json")
	if err != nil || !bytes.Equal(contract, documentSchema) {
		t.Fatalf("embedded parser schema differs from the shared contract: %v", err)
	}
}

func TestDocumentValidation(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	document, err := validator.Validate([]byte(validDocument), int64(len(validDocument)))
	if err != nil || len(document.Pages) != 1 || document.Pages[0].Blocks[0].Text == nil || *document.Pages[0].Blocks[0].Text != "标题" {
		t.Fatalf("valid parser output rejected: document=%+v err=%v", document, err)
	}
	for name, value := range map[string]string{
		"version":            strings.Replace(validDocument, `"1.0.0"`, `"2.0.0"`, 1),
		"extra field":        strings.Replace(validDocument, `"schema_version"`, `"extra":1,"schema_version"`, 1),
		"duplicate field":    strings.Replace(validDocument, `"schema_version"`, `"schema_version":"1.0.0","schema_version"`, 1),
		"trailing data":      validDocument + `{}`,
		"invalid ratio":      strings.Replace(validDocument, `"garbled_ratio":0`, `"garbled_ratio":1.1`, 1),
		"invalid date":       strings.Replace(validDocument, `2026-10-28T00:00:00Z`, `not-a-date`, 1),
		"missing block text": strings.Replace(validDocument, `,"text":"标题"`, ``, 1),
		"invalid utf8":       strings.Replace(validDocument, "标题", string([]byte{0xff}), 1),
		"too many pages":     fmt.Sprintf(`{"schema_version":"1.0.0","pages":[%s],"meta":{"parser_profile":"test"}}`, strings.Repeat(`{"no":1,"blocks":[],"signals":{"char_count":0}},`, 2000)+`{"no":1,"blocks":[],"signals":{"char_count":0}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validator.Validate([]byte(value), int64(len(value))); !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("invalid output accepted: %v", err)
			}
		})
	}
	if _, err := validator.Validate([]byte(validDocument), 1); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("output size limit ignored: %v", err)
	}
}
