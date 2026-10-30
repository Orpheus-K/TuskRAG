package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ImportQuestions combines a document manifest with strictly validated JSONL.
// No partial dataset is returned when any record is invalid.
func ImportQuestions(manifest Dataset, input io.Reader) (Dataset, error) {
	if input == nil || len(manifest.Questions) != 0 {
		return Dataset{}, errors.New("question import requires a manifest without questions")
	}
	limited := &io.LimitedReader{R: input, N: 16*1024*1024 + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		var question Question
		if err := decoder.Decode(&question); err != nil {
			return Dataset{}, fmt.Errorf("question line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return Dataset{}, fmt.Errorf("question line %d contains trailing data", line)
		}
		manifest.Questions = append(manifest.Questions, question)
	}
	if err := scanner.Err(); err != nil {
		return Dataset{}, fmt.Errorf("read question JSONL: %w", err)
	}
	if limited.N == 0 {
		return Dataset{}, errors.New("question JSONL exceeds 16 MiB")
	}
	if err := ValidateDataset(manifest); err != nil {
		return Dataset{}, fmt.Errorf("validate imported questions: %w", err)
	}
	return manifest, nil
}
