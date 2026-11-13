package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"tuskrag.local/core/internal/retrieval"
)

type SetReader interface {
	Get(context.Context, retrieval.Actor, int64, int64, int32) (StoredSet, error)
}

type SetBinding struct {
	SetID    int64 `json:"set_id"`
	Revision int32 `json:"revision"`
}

// RunSet reads an explicitly pinned revision, executes outside storage
// transactions and rechecks that revision's current access before success.
// Config.Documents is only for synthetic Dataset key bindings and is rejected.
func RunSet(ctx context.Context, service Searcher, sets SetReader, binding SetBinding, config RunConfig, mode string) (RunReport, error) {
	if ctx == nil || service == nil || sets == nil || binding.SetID <= 0 || binding.Revision <= 0 || len(config.Documents) != 0 {
		return RunReport{}, ErrInvalidCase
	}
	if err := ctx.Err(); err != nil {
		return RunReport{}, err
	}
	stored, err := sets.Get(ctx, config.Actor, config.KnowledgeBaseID, binding.SetID, binding.Revision)
	if err != nil {
		return RunReport{}, fmt.Errorf("read pinned evaluation set: %w", err)
	}
	if err := validateRunSet(stored, binding); err != nil {
		return RunReport{}, err
	}
	questions := make([]preparedQuestion, 0, len(stored.Definition.Cases))
	for _, item := range stored.Definition.Cases {
		q := preparedQuestion{ID: item.ID, Query: item.Question, Answerable: item.Answerable}
		for _, target := range item.Expected {
			q.Expected = append(q.Expected, Target(target))
		}
		questions = append(questions, q)
	}
	version := fmt.Sprintf("set:%d:%d", binding.SetID, binding.Revision)
	report, err := runQuestions(ctx, service, version, stored.Digest, questions, config, mode)
	if err == nil {
		var latest StoredSet
		latest, err = sets.Get(ctx, config.Actor, config.KnowledgeBaseID, binding.SetID, binding.Revision)
		if err == nil {
			err = validateRunSet(latest, binding)
		}
		if err == nil && latest.Digest != stored.Digest {
			err = fmt.Errorf("%w: pinned evaluation definition changed", ErrInvalidCase)
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		// An error may be revocation; do not publish previously collected IDs as
		// a usable partial set result after losing access to the pinned source.
		report.Status = "failed"
		report.Rows = []RunRow{}
		report.Summary = Summary{}
		report.LatencyMS = map[string][2]float64{}
		return report, fmt.Errorf("run pinned evaluation set: %w", err)
	}
	return report, nil
}

func validateRunSet(stored StoredSet, binding SetBinding) error {
	if stored.ID != binding.SetID || stored.Revision != binding.Revision {
		return fmt.Errorf("%w: evaluation revision mismatch", ErrInvalidCase)
	}
	if err := ValidateSet(stored.Definition); err != nil {
		return err
	}
	data, err := json.Marshal(stored.Definition)
	if err != nil {
		return fmt.Errorf("encode pinned definition: %w", err)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != stored.Digest {
		return fmt.Errorf("%w: evaluation definition digest mismatch", ErrInvalidCase)
	}
	return nil
}
