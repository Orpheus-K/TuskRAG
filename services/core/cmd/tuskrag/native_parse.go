package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"tuskrag.local/core/internal/parser"
)

// runNativeParse is entered only by the one-shot sandbox process. Input and
// output paths are fixed file-descriptor mounts supplied by the parent.
func runNativeParse(args []string) (resultErr error) {
	if len(args) != 2 || (args[0] != "text/plain" && args[0] != "text/markdown") || len(args[1]) > 4096 {
		return parser.ErrUnsupportedFormat
	}
	input, err := os.Open("/input")
	if err != nil {
		return fmt.Errorf("open native parser input: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	validator, err := parser.NewValidator()
	if err != nil {
		return err
	}
	document, err := parser.ParseNativeText(context.Background(), parser.ParseInput{
		File: input, MIME: args[0], Limits: json.RawMessage(args[1]),
	}, validator)
	if err != nil {
		return err
	}
	output, err := os.OpenFile("/output/parsed.json", os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open native parser output: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, output.Close()) }()
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("write native parser output: %w", err)
	}
	return nil
}

func nativeParseExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, parser.ErrUnsupportedFormat):
		return 10
	case errors.Is(err, parser.ErrEncrypted):
		return 11
	case errors.Is(err, parser.ErrLimitExceeded):
		return 12
	case errors.Is(err, parser.ErrParse), errors.Is(err, parser.ErrInvalidOutput):
		return 13
	default:
		return 1
	}
}
