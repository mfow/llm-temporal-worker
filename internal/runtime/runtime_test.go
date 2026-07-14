package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/internal/app"
	"github.com/mfow/llm-temporal-worker/llm"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func runtimeConfig(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	value := string(data)
	value = strings.Replace(value, "health_address: 0.0.0.0:8080", "health_address: 127.0.0.1:0", 1)
	value = strings.Replace(value, "metrics_address: 0.0.0.0:9090", "metrics_address: 127.0.0.1:0", 1)
	return []byte(value)
}

type testEngine struct{}

func (testEngine) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("test engine")
}

type testRegistry struct{}

func (testRegistry) RegisterActivity(interface{})                                         {}
func (testRegistry) RegisterActivityWithOptions(interface{}, activity.RegisterOptions)    {}
func (testRegistry) RegisterDynamicActivity(interface{}, activity.DynamicRegisterOptions) {}

var _ worker.ActivityRegistry = testRegistry{}

type testWorker struct {
	started atomic.Bool
	stopped atomic.Bool
}

func (worker *testWorker) Start() error {
	worker.started.Store(true)
	return nil
}

func (worker *testWorker) Stop() { worker.stopped.Store(true) }

func testRuntimeOptions(t *testing.T, workerController *testWorker, closed *atomic.Bool) Options {
	t.Helper()
	return Options{
		Resolver: config.ReferenceResolverFunc(func(context.Context, *config.Config) error { return nil }),
		TemporalFactory: TemporalClientFactoryFunc(func(_ context.Context, value config.Config) (client.Client, error) {
			return client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1", Namespace: value.Temporal.Namespace})
		}),
		EngineFactory: EngineFactoryFunc(func(context.Context, *config.Snapshot) (llm.Engine, app.ClientSet, error) {
			return testEngine{}, app.ClientSetFunc(func(context.Context) error {
				closed.Store(true)
				return nil
			}), nil
		}),
		WorkerFactory: func(_ client.Client, _ string, _ worker.Options) (app.WorkerController, worker.ActivityRegistry, error) {
			return workerController, testRegistry{}, nil
		},
	}
}

func TestNewFailsClosedWithoutProviderFactory(t *testing.T) {
	marker := "runtime-secret-marker"
	data := strings.Replace(string(runtimeConfig(t)), "OPENAI_API_KEY", marker, 1)
	_, err := New(context.Background(), []byte(data), Options{Resolver: config.ReferenceResolverFunc(func(context.Context, *config.Config) error { return nil })})
	if !errors.Is(err, ErrEngineFactoryUnavailable) {
		t.Fatalf("error = %v, want ErrEngineFactoryUnavailable", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("error leaked configuration marker: %v", err)
	}
}

func TestFactoryErrorsDoNotLeakSecretText(t *testing.T) {
	marker := "provider-secret-marker"
	_, err := New(context.Background(), runtimeConfig(t), Options{
		Resolver: config.ReferenceResolverFunc(func(context.Context, *config.Config) error { return nil }),
		EngineFactory: EngineFactoryFunc(func(context.Context, *config.Snapshot) (llm.Engine, app.ClientSet, error) {
			return nil, nil, errors.New(marker)
		}),
	})
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("engine factory error = %v", err)
	}
	_, err = New(context.Background(), runtimeConfig(t), Options{
		Resolver: config.ReferenceResolverFunc(func(context.Context, *config.Config) error { return nil }),
		EngineFactory: EngineFactoryFunc(func(context.Context, *config.Snapshot) (llm.Engine, app.ClientSet, error) {
			return testEngine{}, nil, nil
		}),
		TemporalFactory: TemporalClientFactoryFunc(func(context.Context, config.Config) (client.Client, error) {
			return nil, errors.New(marker)
		}),
	})
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("Temporal factory error = %v", err)
	}
}

func TestRuntimeStartsAndShutsDownInOrder(t *testing.T) {
	controller := &testWorker{}
	var closed atomic.Bool
	runtime, err := New(context.Background(), runtimeConfig(t), testRuntimeOptions(t, controller, &closed))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Health.Ready() {
		t.Fatal("runtime became ready before Start")
	}
	if err := runtime.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("sandbox does not permit loopback listeners: %v", err)
		}
		t.Fatal(err)
	}
	if !controller.started.Load() || !runtime.Health.Ready() {
		t.Fatal("worker did not start or readiness did not transition")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if runtime.Health.Ready() || runtime.Health.Live() || !controller.stopped.Load() || !closed.Load() {
		t.Fatalf("shutdown state = ready %v live %v stopped %v closed %v", runtime.Health.Ready(), runtime.Health.Live(), controller.stopped.Load(), closed.Load())
	}
}

func TestRunTreatsCancellationAsGracefulExit(t *testing.T) {
	controller := &testWorker{}
	var closed atomic.Bool
	runtime, err := New(context.Background(), runtimeConfig(t), testRuntimeOptions(t, controller, &closed))
	if err != nil {
		t.Fatal(err)
	}
	// Probe listeners are intentionally real sockets. Some restricted test
	// sandboxes deny loopback binds; skip this lifecycle test there while the
	// listener behavior remains covered by internal/httpserver tests.
	if err := runtime.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("sandbox does not permit loopback listeners: %v", err)
		}
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	controller = &testWorker{}
	closed = atomic.Bool{}
	runtime, err = New(context.Background(), runtimeConfig(t), testRuntimeOptions(t, controller, &closed))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for !runtime.Health.Ready() {
		select {
		case <-deadline:
			t.Fatal("worker did not become ready")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not shut down")
	}
	if runtime.Health.Live() || runtime.Health.Ready() || !controller.stopped.Load() || !closed.Load() {
		t.Fatal("cancellation did not complete graceful shutdown")
	}
}

func TestLoadTLSConfigDoesNotExposeCertificateBytes(t *testing.T) {
	marker := "PRIVATE-CERT-MARKER"
	_, err := loadTLSConfig(config.TLSConfig{Enabled: true, CAFile: "/redacted"}, func(string) ([]byte, error) {
		return []byte(marker), nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error = %v, want invalid certificate", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("TLS error leaked certificate bytes: %v", err)
	}
}
