package activity

import (
	"context"
	"sync"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/engine"
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

// HeartbeatMetrics is the deliberately narrow metrics dependency needed by a
// per-Activity heartbeater. It keeps the Activity package independent of a
// particular Prometheus implementation.
type HeartbeatMetrics interface {
	SetHeartbeatAge(time.Duration)
}

type TemporalHeartbeaterOptions struct {
	Clock   func() time.Time
	Metrics HeartbeatMetrics
}

type TemporalHeartbeater struct {
	mu      sync.Mutex
	started time.Time
	clock   func() time.Time
	metrics HeartbeatMetrics
}

func NewTemporalHeartbeater(options TemporalHeartbeaterOptions) *TemporalHeartbeater {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &TemporalHeartbeater{clock: options.Clock, metrics: options.Metrics}
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
			heartbeater.started = heartbeater.now()
		}
	}
	started := heartbeater.started
	heartbeater.mu.Unlock()
	last := progress.At
	if last.IsZero() {
		last = heartbeater.now()
	}
	if heartbeater.metrics != nil {
		heartbeater.metrics.SetHeartbeatAge(last.Sub(started))
	}
	sdkactivity.RecordHeartbeat(ctx, HeartbeatDetails{OperationID: progress.OperationID, Phase: progress.Phase, RouteIndex: progress.RouteIndex, ClassIndex: progress.ClassIndex, StartedAt: started, LastEventAt: last, OutputItems: progress.OutputItems})
	return ctx.Err()
}

func (heartbeater *TemporalHeartbeater) now() time.Time {
	if heartbeater != nil && heartbeater.clock != nil {
		return heartbeater.clock().UTC()
	}
	return time.Now().UTC()
}
