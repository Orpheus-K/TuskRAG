package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// SetDefinition is an authored knowledge-base evaluation set. Unlike the
// synthetic Dataset it references existing documents and contains no corpus.
type SetDefinition struct {
	Name  string           `json:"name"`
	Cases []CaseDefinition `json:"cases"`
}

type ExpectedLocation struct {
	DocumentID int64 `json:"document_id"`
	SectionID  int64 `json:"section_id,omitempty"`
	Page       int32 `json:"page,omitempty"`
}

type CaseDefinition struct {
	ID              string             `json:"id"`
	Question        string             `json:"question"`
	Expected        []ExpectedLocation `json:"expected"`
	Answerable      bool               `json:"answerable"`
	ReferenceAnswer string             `json:"reference_answer,omitempty"`
	Tags            []string           `json:"tags"`
}

// Require an explicit answerability label; omission must not silently turn an
// unlabeled question into a negative example.
func (item *CaseDefinition) UnmarshalJSON(data []byte) error {
	type definition CaseDefinition
	wire := struct {
		*definition
		Answerable *bool `json:"answerable"`
	}{definition: (*definition)(item)}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.Answerable == nil {
		return fmt.Errorf("%w: explicit answerable boolean required", ErrInvalidCase)
	}
	item.Answerable = *wire.Answerable
	return nil
}

const MaxSetBytes = 8 * 1024 * 1024

// DecodeSet validates the entire input before returning any authored content.
// Existence, membership and location ownership require a separate DB check.
func DecodeSet(input io.Reader) (SetDefinition, error) {
	if input == nil {
		return SetDefinition{}, ErrInvalidCase
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxSetBytes+1))
	if err != nil {
		return SetDefinition{}, fmt.Errorf("read evaluation set: %w", err)
	}
	if len(data) > MaxSetBytes || !utf8.Valid(data) {
		return SetDefinition{}, fmt.Errorf("%w: evaluation set size or encoding", ErrInvalidCase)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result SetDefinition
	if err := decoder.Decode(&result); err != nil {
		return SetDefinition{}, fmt.Errorf("%w: evaluation set JSON: %w", ErrInvalidCase, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return SetDefinition{}, fmt.Errorf("%w: evaluation set trailing data", ErrInvalidCase)
	}
	if err := ValidateSet(result); err != nil {
		return SetDefinition{}, err
	}
	return result, nil
}

func ValidateSet(set SetDefinition) error {
	if !setText(set.Name, 200) || len(set.Cases) == 0 || len(set.Cases) > 10000 {
		return fmt.Errorf("%w: evaluation set name or case count", ErrInvalidCase)
	}
	ids := make(map[string]bool, len(set.Cases))
	for index, item := range set.Cases {
		if err := validateCaseDefinition(item); err != nil {
			return fmt.Errorf("case %d: %w", index+1, err)
		}
		if ids[item.ID] {
			return fmt.Errorf("%w: duplicate case ID at position %d", ErrInvalidCase, index+1)
		}
		ids[item.ID] = true
	}
	data, err := json.Marshal(set)
	if err != nil || len(data) > MaxSetBytes {
		return fmt.Errorf("%w: evaluation set exceeds size limit", ErrInvalidCase)
	}
	return nil
}

func validateCaseDefinition(item CaseDefinition) error {
	if !setText(item.ID, 128) || item.ID != strings.TrimSpace(item.ID) || !setText(item.Question, 4096) ||
		len(item.Expected) > 100 || item.Answerable != (len(item.Expected) > 0) || len(item.Tags) > 32 {
		return fmt.Errorf("%w: case identity, query, targets or tags", ErrInvalidCase)
	}
	if item.ReferenceAnswer != "" && !setText(item.ReferenceAnswer, 32768) {
		return fmt.Errorf("%w: reference answer", ErrInvalidCase)
	}
	locations := make(map[ExpectedLocation]bool, len(item.Expected))
	for _, location := range item.Expected {
		if location.DocumentID <= 0 || location.SectionID < 0 || location.Page < 0 || locations[location] {
			return fmt.Errorf("%w: invalid or duplicate expected location", ErrInvalidCase)
		}
		locations[location] = true
	}
	tags := make(map[string]bool, len(item.Tags))
	for _, tag := range item.Tags {
		if !setText(tag, 128) || tag != strings.TrimSpace(tag) || tags[tag] {
			return fmt.Errorf("%w: invalid or duplicate tag", ErrInvalidCase)
		}
		tags[tag] = true
	}
	return nil
}

func setText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
