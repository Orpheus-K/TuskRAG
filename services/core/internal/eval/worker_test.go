package eval

import (
	"context"
	"errors"
	"testing"

	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

func TestEvaluationFailureClassification(t *testing.T) {
	for _, item := range []struct {
		cause error
		code  jobs.FailureCode
	}{
		{ErrSetNotFound, jobs.PermissionRevoked}, {ErrReportForbidden, jobs.PermissionRevoked},
		{retrieval.ErrConfigurationChanged, jobs.ConfigurationChanged}, {ErrSetArchived, jobs.ConfigurationChanged},
		{context.DeadlineExceeded, jobs.Timeout}, {models.ErrEgressDenied, jobs.EgressDenied},
		{models.ErrInvalidResponse, jobs.ModelInvalid}, {&models.UpstreamError{Status: 429, Retryable: true}, jobs.ModelUnavailable},
		{&models.UpstreamError{Status: 400}, jobs.ModelRejected}, {ErrReportInvalid, jobs.Internal},
	} {
		var failure *jobs.ProcessingFailure
		err := executionFailure(item.cause)
		if !errors.As(err, &failure) || failure.Code != item.code || !errors.Is(err, item.cause) {
			t.Fatalf("classification %v: %v", item.cause, err)
		}
	}
	for _, err := range []error{nil, context.Canceled, jobs.ErrLeaseLost} {
		if executionFailure(err) != err {
			t.Fatalf("control error wrapped: %v", err)
		}
	}
}
