package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/retrieval"
	"tuskrag.local/core/internal/secrets"
	"tuskrag.local/core/internal/tokenizer"
)

func readEvalJSON(path string, target any) (resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("evaluation file has trailing data")
	}
	return nil
}

func runEval(ctx context.Context, args []string) error {
	if len(args) == 3 && args[0] == "compare" {
		var before, after eval.RunReport
		if err := readEvalJSON(args[1], &before); err != nil {
			return err
		}
		if err := readEvalJSON(args[2], &after); err != nil {
			return err
		}
		return eval.Compare(before, after)
	}
	if len(args) != 5 || args[0] != "run" {
		return errors.New("usage: tuskrag eval run <dataset.json> <config.json> <lexical|vector|rrf|rerank> <report.json> | eval compare <baseline.json> <current.json>")
	}
	var dataset eval.Dataset
	var config eval.RunConfig
	if err := readEvalJSON(args[1], &dataset); err != nil {
		return err
	}
	if err := readEvalJSON(args[2], &config); err != nil {
		return err
	}
	if err := eval.ValidateDataset(dataset); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pool, err := db.OpenWithLimit(ctx, os.Getenv("TUSKRAG_DATABASE_URL"), 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	var vault *secrets.Vault
	if path := os.Getenv("TUSKRAG_MASTER_KEY_FILE"); path != "" {
		vault, err = secrets.LoadVault(path)
		if err != nil {
			return err
		}
	}
	counter, err := tokenizer.NewBGEM3(ctx, 2)
	if err != nil {
		return err
	}
	service, err := retrieval.NewService(pool, vault, counter.Count)
	if err != nil {
		return err
	}
	report, runErr := eval.Run(ctx, service, dataset, config, args[3])
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return errors.Join(runErr, err)
	}
	file, err := os.OpenFile(args[4], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.Join(runErr, err)
	}
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(runErr, writeErr, closeErr)
	}
	if runErr != nil {
		return fmt.Errorf("evaluation failed; partial report saved: %w", runErr)
	}
	return nil
}
