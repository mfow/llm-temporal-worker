package activity

import (
	"context"
	"sync"
	"time"

	"github.com/mfow/llm-temporal-worker/engine"
	sdkactivity "go.temporal.io/sdk/activity"
)

type HeartbeatDetails struct {
	OperationID string    `json:"operation_id,omitempty"`
	Phase       string    `json:"phase"`
	RouteIndex  int       `json:"route_index"`
	ClassIndex  int       `json:"class_index"`
	StartedAt   time.Time `json:"started_at"`
	LastEventAt time.Time `json:"last_event_at"`
	OutputItems int       `json:"output_items"`
}

type Heartbeater interface {
	Beat(context.Context, engine.Progress) error
}

type TemporalHeartbeater struct {
	mu      sync.Mutex
	started time.Time
}

func (heartbeater *TemporalHeartbeater) Beat(ctx context.Context, progress engine.Progress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if heartbeater == nil {
		return nil
	}
	heartbeater.mu.Lock()
	if heartbeater.started.IsZero() {
		heartbeater.started = progress.At
		if heartbeater.started.IsZero() {
			heartbeater.started = time.Now().UTC()
		}
	}
	started := heartbeater.started
	heartbeater.mu.Unlock()
	last := progress.At
	if last.IsZero() {
		last = time.Now().UTC()
	}
	sdkactivity.RecordHeartbeat(ctx, HeartbeatDetails{OperationID: progress.OperationID, Phase: progress.Phase, RouteIndex: progress.RouteIndex, ClassIndex: progress.ClassIndex, StartedAt: started, LastEventAt: last, OutputItems: progress.OutputItems})
	return ctx.Err()
}
