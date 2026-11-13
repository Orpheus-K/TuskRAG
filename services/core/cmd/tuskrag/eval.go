package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/answer"
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
	if len(args) == 2 && args[0] == "validate-set" {
		file, err := os.Open(args[1])
		if err != nil {
			return err
		}
		_, decodeErr := eval.DecodeSet(file)
		return errors.Join(decodeErr, file.Close())
	}
	if len(args) == 2 && args[0] == "validate" {
		var report eval.RunReport
		if err := readEvalJSON(args[1], &report); err != nil {
			return err
		}
		return eval.ValidateReport(report)
	}
	if len(args) == 4 && args[0] == "import" {
		return importEvalQuestions(args[1], args[2], args[3])
	}
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
	if len(args) == 2 && args[0] == "answer-validate" {
		var report eval.AnswerReport
		if err := readEvalJSON(args[1], &report); err != nil {
			return err
		}
		return eval.ValidateAnswerReport(report)
	}
	if len(args) == 3 && args[0] == "answer-compare" {
		return compareAnswerEval(args[1], args[2])
	}
	if len(args) == 4 && args[0] == "answer-run" {
		return runAnswerEval(ctx, args[1], args[2], args[3])
	}
	if len(args) != 5 || (args[0] != "run" && args[0] != "run-set") {
		return errors.New("usage: tuskrag eval import <manifest.json> <questions.jsonl> <dataset.json> | eval run <dataset.json> <config.json> <lexical|vector|rrf|rerank> <report.json> | eval run-set <binding.json> <config.json> <lexical|vector|rrf|rerank> <report.json> | eval compare <baseline.json> <current.json> | eval validate <report.json> | eval validate-set <set.json> | eval answer-run <dataset.json> <config.json> <report.json> | eval answer-compare <baseline.json> <current.json> | eval answer-validate <report.json>")
	}
	var dataset eval.Dataset
	var binding eval.SetBinding
	var config eval.RunConfig
	if args[0] == "run-set" {
		if err := readEvalJSON(args[1], &binding); err != nil {
			return err
		}
		if binding.SetID <= 0 || binding.Revision <= 0 {
			return eval.ErrInvalidCase
		}
	} else {
		if err := readEvalJSON(args[1], &dataset); err != nil {
			return err
		}
		if err := eval.ValidateDataset(dataset); err != nil {
			return err
		}
	}
	if err := readEvalJSON(args[2], &config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pool, vault, counter, err := openEvalDeps(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	service, err := retrieval.NewService(pool, vault, counter.Count)
	if err != nil {
		return err
	}
	var report eval.RunReport
	var runErr error
	if args[0] == "run-set" {
		sets, err := eval.NewSetStore(pool)
		if err != nil {
			return err
		}
		if config.Configuration == nil {
			snapshot, err := service.Snapshot(ctx, config.Actor, config.KnowledgeBaseID)
			if err != nil {
				return err
			}
			config.Configuration = &snapshot
		}
		report, runErr = eval.RunSet(ctx, service, sets, binding, config, args[3])
	} else {
		report, runErr = eval.Run(ctx, service, dataset, config, args[3])
	}
	if args[0] == "run-set" && runErr != nil && report.DatasetSHA256 == "" {
		return runErr
	}
	if writeErr := writeExclusiveEvalJSON(args[4], report); writeErr != nil {
		return errors.Join(runErr, writeErr)
	}
	if runErr != nil {
		return fmt.Errorf("evaluation failed; partial report saved: %w", runErr)
	}
	return nil
}

func openEvalDeps(ctx context.Context) (*pgxpool.Pool, *secrets.Vault, *tokenizer.BGEM3, error) {
	pool, err := db.OpenWithLimit(ctx, os.Getenv("TUSKRAG_DATABASE_URL"), 4)
	if err != nil {
		return nil, nil, nil, err
	}
	var vault *secrets.Vault
	if path := os.Getenv("TUSKRAG_MASTER_KEY_FILE"); path != "" {
		vault, err = secrets.LoadVault(path)
		if err != nil {
			pool.Close()
			return nil, nil, nil, err
		}
	}
	counter, err := tokenizer.NewBGEM3(ctx, 2)
	if err != nil {
		pool.Close()
		return nil, nil, nil, err
	}
	return pool, vault, counter, nil
}

func writeExclusiveEvalJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	return errors.Join(writeErr, file.Close())
}

func runAnswerEval(ctx context.Context, datasetPath, configPath, reportPath string) error {
	var dataset eval.AnswerDataset
	if err := readEvalJSON(datasetPath, &dataset); err != nil {
		return err
	}
	if err := eval.ValidateAnswerDataset(dataset); err != nil {
		return err
	}
	var config eval.AnswerRunConfig
	if err := readEvalJSON(configPath, &config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pool, vault, counter, err := openEvalDeps(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	search, err := retrieval.NewService(pool, vault, counter.Count)
	if err != nil {
		return err
	}
	answers, err := answer.NewService(pool, vault, search, counter.Count)
	if err != nil {
		return err
	}
	answers.Assembly = config.ResolveAssembly()
	var judge eval.CitationJudge
	if config.Judge != nil {
		judge, err = eval.NewModelCitationJudge(pool, vault, config.Actor, config.KnowledgeBaseID, config.Judge.ChatModelID)
		if err != nil {
			return err
		}
	}
	report, runErr := eval.RunAnswer(ctx, answers, judge, dataset, config)
	if report.DatasetSHA256 == "" && runErr != nil {
		return runErr
	}
	if writeErr := writeExclusiveEvalJSON(reportPath, report); writeErr != nil {
		return errors.Join(runErr, writeErr)
	}
	if runErr != nil {
		return fmt.Errorf("answer evaluation failed; partial report saved: %w", runErr)
	}
	return nil
}

func compareAnswerEval(baselinePath, currentPath string) error {
	var before, after eval.AnswerReport
	if err := readEvalJSON(baselinePath, &before); err != nil {
		return err
	}
	if err := readEvalJSON(currentPath, &after); err != nil {
		return err
	}
	comparison, err := eval.CompareAnswerReports(before, after)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(comparison, "", "  ")
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(append(data, '\n')); err != nil {
		return err
	}
	if comparison.Gate == "regressed" {
		return errors.New("answer evaluation metric regressed by more than two percentage points")
	}
	return nil
}

func importEvalQuestions(manifestPath, questionsPath, outputPath string) error {
	var manifest eval.Dataset
	if err := readEvalJSON(manifestPath, &manifest); err != nil {
		return err
	}
	input, err := os.Open(questionsPath)
	if err != nil {
		return err
	}
	dataset, importErr := eval.ImportQuestions(manifest, input)
	if err := errors.Join(importErr, input.Close()); err != nil {
		return err
	}
	return writeExclusiveEvalJSON(outputPath, dataset)
}
