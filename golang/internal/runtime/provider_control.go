package runtime

import (
	"context"
	"fmt"

	"github.com/mfow/llm-temporal-worker/golang/control"
	"github.com/mfow/llm-temporal-worker/golang/engine"
	postgresstore "github.com/mfow/llm-temporal-worker/golang/storage/postgres"
)

// providerStatusRepositorySource is implemented by a PostgreSQL client set
// that owns the same pool used for durable state. Keeping this optional avoids
// changing the public PostgresFactory signature or opening a second pool.
type providerStatusRepositorySource interface {
	ProviderStatusRepository() postgresstore.ProviderStatusRepository
}

type postgresProviderStatusRecorder struct {
	repository postgresstore.ProviderStatusRepository
}

var _ engine.ProviderStatusRecorder = (*postgresProviderStatusRecorder)(nil)

func newPostgresProviderStatusRecorder(source providerStatusRepositorySource) engine.ProviderStatusRecorder {
	if source == nil {
		return nil
	}
	repository := source.ProviderStatusRepository()
	return &postgresProviderStatusRecorder{repository: repository}
}

func (recorder *postgresProviderStatusRecorder) RecordProviderStatus(ctx context.Context, observation control.StatusObservation) error {
	if recorder == nil {
		return fmt.Errorf("provider status recorder is nil")
	}
	event, err := control.NewStatusEvent(observation)
	if err != nil {
		return err
	}
	_, err = recorder.repository.PersistStatusEvent(ctx, event)
	return err
}
