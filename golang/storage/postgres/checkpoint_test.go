package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mfow/llm-temporal-worker/golang/state"
)

var _ state.CheckpointRepository = DurableCheckpointRepository{}

func TestCheckpointRepositoryValidationDoesNotOpenPool(t *testing.T) {
	repository := DurableCheckpointRepository{}
	if _, err := repository.Get(context.Background(), uuid.NewString(), state.CheckpointID(uuid.NewString())); err == nil {
		t.Fatal("Get accepted a nil pool")
	}
	if _, err := repository.BeginCheckpoint(context.Background()); err == nil {
		t.Fatal("BeginCheckpoint accepted a nil pool")
	}
}

func TestCheckpointRepositoryIdentifiersRequireUUIDs(t *testing.T) {
	for _, test := range []struct {
		name string
		fn   func() error
	}{
		{"checkpoint", func() error { _, err := parseCheckpointUUID(state.CheckpointID("legacy-id"), "ID"); return err }},
		{"blob", func() error { _, err := parseBlobUUID(state.BlobID("legacy-blob"), "delta"); return err }},
		{"scope", func() error { _, err := parseScopeUUID("tenant/default"); return err }},
		{"origin operation", func() error { _, err := parseOperationUUID(state.OperationID("legacy-operation")); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.fn(); err == nil {
				t.Fatal("identifier was accepted")
			}
		})
	}
	valid := uuid.New()
	if got, err := parseCheckpointUUID(state.CheckpointID(valid.String()), "ID"); err != nil || got != valid {
		t.Fatalf("valid checkpoint UUID rejected: %v", err)
	}
	if got, err := parseOperationUUID(state.OperationID(valid.String())); err != nil || got != valid {
		t.Fatalf("valid origin operation UUID rejected: got=%s err=%v", got, err)
	}
	for _, nonCanonical := range []string{" " + valid.String(), strings.ToUpper(valid.String())} {
		if _, err := parseOperationUUID(state.OperationID(nonCanonical)); err == nil {
			t.Fatalf("non-canonical origin operation ID %q was accepted", nonCanonical)
		}
	}
}

func TestCheckpointRepositoryErrorsAreStable(t *testing.T) {
	if !errors.Is(ErrCheckpointNotFound, ErrCheckpointNotFound) {
		t.Fatal("not-found sentinel is not comparable")
	}
	if !errors.Is(ErrCheckpointConflict, ErrCheckpointConflict) {
		t.Fatal("conflict sentinel is not comparable")
	}
}
