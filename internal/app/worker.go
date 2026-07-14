package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mfow/llm-temporal-worker/activity"
	"github.com/mfow/llm-temporal-worker/internal/httpserver"
	"github.com/mfow/llm-temporal-worker/internal/observability"
	"go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
)

var ErrWorkerStopping = errors.New("worker is stopping")

type WorkerController interface {
	Start() error
	Stop()
}

type WorkerFactory func(client.Client, string, sdkworker.Options) (WorkerController, sdkworker.ActivityRegistry, error)

type WorkerOptions struct {
	Client                         client.Client
	TaskQueue                      string
	Identity                       string
	MaxConcurrentActivities        int
	MaxConcurrentActivityTaskPolls int
	GracefulStopTimeout            time.Duration
	Activities                     *activity.Activities
	Health                         *httpserver.HealthState
	Metrics                        *observability.Metrics
	Factory                        WorkerFactory
}

type TemporalWorker struct {
	controller WorkerController
	build      func() (WorkerController, error)
	health     *httpserver.HealthState
	metrics    *observability.Metrics

	mu       sync.Mutex
	started  bool
	starting bool
	paused   bool
	stopping bool
}

func NewWorker(options WorkerOptions) (*TemporalWorker, error) {
	if options.TaskQueue == "" {
		return nil, fmt.Errorf("Temporal task queue is required")
	}
	if options.Activities == nil {
		return nil, fmt.Errorf("Activity implementation is required")
	}
	if options.MaxConcurrentActivities <= 0 || options.MaxConcurrentActivityTaskPolls <= 0 {
		return nil, fmt.Errorf("Temporal worker concurrency must be positive")
	}
	if options.GracefulStopTimeout <= 0 {
		return nil, fmt.Errorf("Temporal graceful stop timeout must be positive")
	}
	if options.Health == nil {
		options.Health = httpserver.NewHealthState()
	}
	if options.Factory == nil {
		options.Factory = defaultWorkerFactory
	}
	build := func() (WorkerController, error) {
		controller, registry, err := options.Factory(options.Client, options.TaskQueue, sdkworker.Options{
			Identity:                           options.Identity,
			MaxConcurrentActivityExecutionSize: options.MaxConcurrentActivities,
			MaxConcurrentActivityTaskPollers:   options.MaxConcurrentActivityTaskPolls,
			WorkerStopTimeout:                  options.GracefulStopTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("construct Temporal worker: %w", err)
		}
		if controller == nil || registry == nil {
			return nil, fmt.Errorf("Temporal worker factory returned incomplete worker")
		}
		options.Activities.Register(registry)
		return controller, nil
	}
	controller, err := build()
	if err != nil {
		return nil, err
	}
	return &TemporalWorker{controller: controller, build: build, health: options.Health, metrics: options.Metrics}, nil
}

func defaultWorkerFactory(workflowClient client.Client, taskQueue string, options sdkworker.Options) (WorkerController, sdkworker.ActivityRegistry, error) {
	if workflowClient == nil {
		return nil, nil, fmt.Errorf("Temporal client is required")
	}
	worker := sdkworker.New(workflowClient, taskQueue, options)
	return worker, worker, nil
}

func (worker *TemporalWorker) Start() error {
	if worker == nil {
		return fmt.Errorf("Temporal worker is not initialized")
	}
	worker.mu.Lock()
	if worker.stopping {
		worker.mu.Unlock()
		return ErrWorkerStopping
	}
	if worker.started || worker.starting {
		worker.mu.Unlock()
		return fmt.Errorf("Temporal worker is already started")
	}
	worker.paused = false
	worker.starting = true
	controller := worker.controller
	worker.mu.Unlock()
	return worker.startController(controller)
}

// Pause stops Temporal polling without making the worker permanently
// unavailable. Resume creates and registers a fresh controller because a
// stopped Temporal SDK worker is not reusable.
func (worker *TemporalWorker) Pause() {
	if worker == nil {
		return
	}
	worker.health.SetReady(false)
	if worker.metrics != nil {
		worker.metrics.SetWorkerPolling(false)
	}
	worker.mu.Lock()
	if worker.stopping {
		worker.mu.Unlock()
		return
	}
	worker.paused = true
	if !worker.started {
		worker.mu.Unlock()
		return
	}
	controller := worker.controller
	worker.controller = nil
	worker.started = false
	worker.mu.Unlock()
	if controller != nil {
		controller.Stop()
	}
}

// Resume restarts Temporal polling after a dependency monitor has proved that
// every required dependency is healthy again.
func (worker *TemporalWorker) Resume() error {
	if worker == nil {
		return fmt.Errorf("Temporal worker is not initialized")
	}
	worker.mu.Lock()
	if worker.stopping {
		worker.mu.Unlock()
		return ErrWorkerStopping
	}
	if worker.started || worker.starting {
		worker.mu.Unlock()
		return fmt.Errorf("Temporal worker is already started")
	}
	worker.paused = false
	worker.starting = true
	controller := worker.controller
	worker.mu.Unlock()
	return worker.startController(controller)
}

func (worker *TemporalWorker) startController(controller WorkerController) error {
	if controller == nil {
		if worker.build == nil {
			worker.finishStartFailure()
			return fmt.Errorf("Temporal worker is not initialized")
		}
		var err error
		controller, err = worker.build()
		if err != nil {
			worker.finishStartFailure()
			return err
		}
		worker.mu.Lock()
		if worker.stopping {
			worker.starting = false
			worker.mu.Unlock()
			return ErrWorkerStopping
		}
		worker.controller = controller
		worker.mu.Unlock()
	}
	if err := controller.Start(); err != nil {
		worker.finishStartFailure()
		return fmt.Errorf("start Temporal worker: %w", err)
	}
	worker.mu.Lock()
	worker.starting = false
	if worker.stopping || worker.paused {
		paused := worker.paused
		worker.controller = nil
		worker.mu.Unlock()
		controller.Stop()
		if paused {
			return nil
		}
		return ErrWorkerStopping
	}
	worker.started = true
	worker.mu.Unlock()
	worker.health.SetReady(true)
	if worker.metrics != nil {
		worker.metrics.SetWorkerPolling(true)
	}
	return nil
}

func (worker *TemporalWorker) finishStartFailure() {
	worker.health.SetReady(false)
	if worker.metrics != nil {
		worker.metrics.SetWorkerPolling(false)
	}
	worker.mu.Lock()
	worker.starting = false
	worker.mu.Unlock()
}

// Stop turns readiness off before stopping pollers. The Temporal SDK owns the
// configured graceful wait for in-flight Activity calls.
func (worker *TemporalWorker) Stop() {
	if worker == nil {
		return
	}
	worker.health.SetReady(false)
	if worker.metrics != nil {
		worker.metrics.SetWorkerPolling(false)
	}
	worker.mu.Lock()
	if worker.stopping {
		worker.mu.Unlock()
		return
	}
	worker.stopping = true
	started := worker.started
	controller := worker.controller
	worker.started = false
	worker.controller = nil
	worker.mu.Unlock()
	if started && controller != nil {
		controller.Stop()
	}
}

func (worker *TemporalWorker) Started() bool {
	if worker == nil {
		return false
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.started
}

func (worker *TemporalWorker) Ready() bool {
	return worker != nil && worker.health.Ready()
}

// Run starts the worker and waits for cancellation. It is useful for the CLI,
// while tests can use Start and Stop directly.
func (worker *TemporalWorker) Run(ctx context.Context) error {
	if err := worker.Start(); err != nil {
		return err
	}
	<-ctx.Done()
	worker.Stop()
	return ctx.Err()
}
