package postgres

import (
	"context"

	"github.com/mfow/llm-temporal-worker/golang/admission"
)

// AttemptRepository is the explicit route-attempt view of an operation
// repository. Keeping it separate makes it possible for conformance tests
// and operational tooling to inspect all attempts without exposing SQL.
type AttemptRepository struct{ Operations OperationRepository }

func (r AttemptRepository) List(ctx context.Context, operationID string) ([]admission.AttemptFacts, error) {
	return r.Operations.Attempts(ctx, operationID)
}
