package postgres

import (
	"context"
	"errors"

	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/state"
)

// ResultRepository provides the result-facing portion of the operation
// contract. Payload bytes remain in the configured blob store; PostgreSQL
// keeps the digest and an encrypted marker on the operation row.
type ResultRepository struct{ Operations OperationRepository }

func (r ResultRepository) Complete(ctx context.Context, request admission.CompleteRequest) error {
	if request.ResultRef == nil || !request.ResultRef.Valid() {
		return errors.New("result reference is required")
	}
	return r.Operations.Complete(ctx, request)
}

func ResultDigest(ref state.BlobRef) [32]byte { return ref.Digest }
