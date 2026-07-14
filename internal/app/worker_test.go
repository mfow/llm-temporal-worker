package app_test

import (
	"errors"
	"testing"
	"time"

	domainactivity "github.com/mfow/llm-temporal-worker/activity"
	"github.com/mfow/llm-temporal-worker/internal/app"
	"github.com/mfow/llm-temporal-worker/internal/httpserver"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

type fakeWorker struct {
	startErr error
	started  bool
	stopped  bool
}

func (worker *fakeWorker) Start() error {
	worker.started = worker.startErr == nil
	return worker.startErr
}
func (worker *fakeWorker) Stop() { worker.stopped = true }

type fakeRegistry struct{ name string }

func (registry *fakeRegistry) RegisterActivity(value interface{}) {}
func (registry *fakeRegistry) RegisterActivityWithOptions(_ interface{}, options activity.RegisterOptions) {
	registry.name = options.Name
}
func (registry *fakeRegistry) RegisterDynamicActivity(interface{}, activity.DynamicRegisterOptions) {}

var _ worker.ActivityRegistry = (*fakeRegistry)(nil)

func TestWorkerRegistersExactActivityAndTransitionsReadiness(t *testing.T) {
	health := httpserver.NewHealthState()
	controller := &fakeWorker{}
	registry := &fakeRegistry{}
	var gotQueue string
	var gotOptions worker.Options
	temporalWorker, err := app.NewWorker(app.WorkerOptions{
		TaskQueue: "queue-a", Identity: "identity-a", MaxConcurrentActivities: 3,
		MaxConcurrentActivityTaskPolls: 2, GracefulStopTimeout: time.Second,
		Activities: &domainactivity.Activities{}, Health: health,
		Factory: func(_ client.Client, queue string, options worker.Options) (app.WorkerController, worker.ActivityRegistry, error) {
			gotQueue, gotOptions = queue, options
			return controller, registry, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotQueue != "queue-a" || gotOptions.Identity != "identity-a" || gotOptions.MaxConcurrentActivityExecutionSize != 3 || gotOptions.MaxConcurrentActivityTaskPollers != 2 {
		t.Fatalf("worker options = %#v queue=%q", gotOptions, gotQueue)
	}
	if registry.name != domainactivity.GenerateActivityName {
		t.Fatalf("registered activity = %q", registry.name)
	}
	if health.Ready() {
		t.Fatal("worker was ready before Start")
	}
	if err := temporalWorker.Start(); err != nil {
		t.Fatal(err)
	}
	if !health.Ready() || !temporalWorker.Started() {
		t.Fatal("worker did not become ready")
	}
	temporalWorker.Stop()
	if health.Ready() || !controller.stopped {
		t.Fatal("worker did not stop polling")
	}
}

func TestWorkerStartErrorLeavesReadinessFalse(t *testing.T) {
	health := httpserver.NewHealthState()
	controller := &fakeWorker{startErr: errors.New("start failed")}
	registry := &fakeRegistry{}
	temporalWorker, err := app.NewWorker(app.WorkerOptions{
		TaskQueue: "queue-a", MaxConcurrentActivities: 1, MaxConcurrentActivityTaskPolls: 1,
		GracefulStopTimeout: time.Second, Activities: &domainactivity.Activities{}, Health: health,
		Factory: func(_ client.Client, _ string, _ worker.Options) (app.WorkerController, worker.ActivityRegistry, error) {
			return controller, registry, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := temporalWorker.Start(); err == nil || health.Ready() {
		t.Fatal("start error did not fail closed")
	}
}

func TestWorkerPauseStopsPollingAndResumeBuildsFreshController(t *testing.T) {
	health := httpserver.NewHealthState()
	controllers := make([]*fakeWorker, 0, 2)
	temporalWorker, err := app.NewWorker(app.WorkerOptions{
		TaskQueue: "queue-a", MaxConcurrentActivities: 1, MaxConcurrentActivityTaskPolls: 1,
		GracefulStopTimeout: time.Second, Activities: &domainactivity.Activities{}, Health: health,
		Factory: func(_ client.Client, _ string, _ worker.Options) (app.WorkerController, worker.ActivityRegistry, error) {
			controller := &fakeWorker{}
			controllers = append(controllers, controller)
			return controller, &fakeRegistry{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := temporalWorker.Start(); err != nil {
		t.Fatal(err)
	}
	temporalWorker.Pause()
	if health.Ready() || !controllers[0].stopped || temporalWorker.Started() {
		t.Fatal("pause did not turn readiness off and stop polling")
	}
	if err := temporalWorker.Resume(); err != nil {
		t.Fatal(err)
	}
	if len(controllers) != 2 || !controllers[1].started || !health.Ready() || !temporalWorker.Started() {
		t.Fatalf("resume controllers=%d started=%v ready=%v", len(controllers), len(controllers) == 2 && controllers[1].started, health.Ready())
	}
	temporalWorker.Stop()
	if !controllers[1].stopped || health.Ready() {
		t.Fatal("permanent stop did not stop the resumed controller")
	}
}
