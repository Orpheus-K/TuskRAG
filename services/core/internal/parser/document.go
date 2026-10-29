package parser

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:generate node ../../../../scripts/sync-parser-schema.mjs
//go:embed parsed-document.schema.json
var documentSchema []byte

type Document struct {
	SchemaVersion string        `json:"schema_version"`
	Pages         []Page        `json:"pages"`
	Outline       []OutlineItem `json:"outline,omitempty"`
	Meta          Metadata      `json:"meta"`
}

type Page struct {
	Number  int64    `json:"no"`
	Width   *float64 `json:"width,omitempty"`
	Height  *float64 `json:"height,omitempty"`
	Blocks  []Block  `json:"blocks"`
	Signals Signals  `json:"signals"`
}

type Block struct {
	Type  string     `json:"type"`
	Level *int       `json:"level,omitempty"`
	Text  *string    `json:"text,omitempty"`
	Cells [][]string `json:"cells,omitempty"`
	BBox  []float64  `json:"bbox,omitempty"`
}

type Signals struct {
	CharCount             int64    `json:"char_count"`
	GarbledRatio          *float64 `json:"garbled_ratio,omitempty"`
	MissingToUnicodeRatio *float64 `json:"missing_tounicode_ratio,omitempty"`
	ImageCoverage         *float64 `json:"image_coverage,omitempty"`
	TableSuspect          *bool    `json:"table_suspect,omitempty"`
}

type OutlineItem struct {
	Title string `json:"title"`
	Level int    `json:"level"`
	Page  *int64 `json:"page,omitempty"`
}

type Metadata struct {
	Title         *string `json:"title,omitempty"`
	Author        *string `json:"author,omitempty"`
	CreatedAt     *string `json:"created_at,omitempty"`
	ParserProfile string  `json:"parser_profile"`
}

type Validator struct{ schema *jsonschema.Schema }

type noSchemaLoader struct{}

func (noSchemaLoader) Load(_ string) (any, error) {
	return nil, errors.New("external schema references are disabled")
}

func NewValidator() (*Validator, error) {
	const schemaID = "https://tuskrag.local/contracts/parsed-document.schema.json"
	source, err := jsonschema.UnmarshalJSON(bytes.NewReader(documentSchema))
	if err != nil {
		return nil, fmt.Errorf("decode parser schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noSchemaLoader{})
	compiler.AssertFormat()
	if err := compiler.AddResource(schemaID, source); err != nil {
		return nil, fmt.Errorf("load parser schema: %w", err)
	}
	schema, err := compiler.Compile(schemaID)
	if err != nil {
		return nil, fmt.Errorf("compile parser schema: %w", err)
	}
	return &Validator{schema: schema}, nil
}

// Validate applies the shared schema and bounds before returning parser data.
func (v *Validator) Validate(data []byte, inputBytes int64) (Document, error) {
	if v == nil || v.schema == nil || inputBytes <= 0 || inputBytes > MaxInputBytes ||
		int64(len(data)) > min(inputBytes*20, MaxOutputBytes) || !utf8.Valid(data) {
		return Document{}, ErrInvalidOutput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSON(decoder, 0); err != nil {
		return Document{}, ErrInvalidOutput
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Document{}, ErrInvalidOutput
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil || v.schema.Validate(value) != nil {
		return Document{}, ErrInvalidOutput
	}
	var document Document
	if err := json.Unmarshal(data, &document); err != nil || len(document.Pages) > 2000 {
		return Document{}, ErrInvalidOutput
	}
	return document, nil
}

func scanJSON(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			name, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := name.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := scanJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

type Parser struct {
	sandbox   *Sandbox
	validator *Validator
}

// NewParser requires isolation for every document format.
func NewParser(sandbox *Sandbox, validator *Validator) (*Parser, error) {
	if validator == nil || validator.schema == nil {
		return nil, errors.New("parser validator is required")
	}
	if sandbox == nil {
		return nil, ErrSandboxUnavailable
	}
	return &Parser{sandbox: sandbox, validator: validator}, nil
}

func (p *Parser) Parse(ctx context.Context, input ParseInput) (Document, error) {
	if input.File == nil {
		return Document{}, errors.New("parser input file is required")
	}
	if p.sandbox == nil {
		return Document{}, ErrSandboxUnavailable
	}
	info, err := input.File.Stat()
	if err != nil {
		return Document{}, fmt.Errorf("stat parser input: %w", err)
	}
	data, err := p.sandbox.run(ctx, input)
	if err != nil {
		return Document{}, err
	}
	return p.validator.Validate(data, info.Size())
}
