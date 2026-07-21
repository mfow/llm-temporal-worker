package postgres

import (
	"context"
	"errors"
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
}

func TestCheckpointRepositoryErrorsAreStable(t *testing.T) {
	if !errors.Is(ErrCheckpointNotFound, ErrCheckpointNotFound) {
		t.Fatal("not-found sentinel is not comparable")
	}
	if !errors.Is(ErrCheckpointConflict, ErrCheckpointConflict) {
		t.Fatal("conflict sentinel is not comparable")
	}
}
