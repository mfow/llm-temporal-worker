package runtime

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
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

var _ llm.Engine = testEngine{}

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

func TestMetricsAllowOneShotResponsePhaseButNotStreaming(t *testing.T) {
	metrics, err := newMetrics(config.Config{Telemetry: config.TelemetryConfig{Metrics: config.MetricsConfig{Enabled: true}}, Models: map[string]config.ModelConfig{
		"logical-model": {Routes: []config.RouteConfig{{Model: "provider-model"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	metrics.RecordActivity("success", "none", time.Millisecond, "response_received")
	metrics.RecordActivity("completed", "none", time.Millisecond, "total")
	metrics.RecordActivity("success", "none", time.Millisecond, "streaming")
	metrics.RecordCost("endpoint", "model", "standard", "catalog_usage", 1)
	metrics.RecordProviderAttempt("endpoint", "provider-model", "standard", "success", time.Millisecond)
	families, err := metrics.Gather()
	if err != nil {
		t.Fatal(err)
	}
	phases := make(map[string]struct{})
	for _, family := range families {
		if family.GetName() != "llmtw_activity_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "phase" {
					phases[label.GetValue()] = struct{}{}
				}
			}
		}
	}
	if _, ok := phases["response_received"]; !ok {
		t.Fatalf("activity metric phases = %v, want response_received", phases)
	}
	if _, ok := phases["total"]; !ok {
		t.Fatalf("activity metric phases = %v, want total", phases)
	}
	if _, ok := phases["streaming"]; ok {
		t.Fatalf("activity metric phases = %v, must not allow streaming for the Temporal runtime", phases)
	}
	if _, ok := phases["other"]; !ok {
		t.Fatalf("activity metric phases = %v, want disallowed streaming to map to other", phases)
	}
	methods := make(map[string]struct{})
	for _, family := range families {
		if family.GetName() != "llmtw_cost_micro_usd_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "method" {
					methods[label.GetValue()] = struct{}{}
				}
			}
		}
	}
	if _, ok := methods["catalog_usage"]; !ok {
		t.Fatalf("cost metric methods = %v, want catalog_usage", methods)
	}
	providerModels := make(map[string]struct{})
	for _, family := range families {
		if family.GetName() != "llmtw_provider_attempt_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "model" {
					providerModels[label.GetValue()] = struct{}{}
				}
			}
		}
	}
	if _, ok := providerModels["provider-model"]; !ok {
		t.Fatalf("provider metric models = %v, want provider-model", providerModels)
	}
}

func TestRuntimeActivitiesUseConfiguredHeartbeatKeepaliveInterval(t *testing.T) {
	configuration, err := config.Load(runtimeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	configuration.Temporal.Worker.HeartbeatKeepaliveInterval = config.Duration(250 * time.Millisecond)
	activities := newRuntimeActivities(configuration, testEngine{}, nil, nil)
	if got, want := activities.HeartbeatKeepaliveInterval, 250*time.Millisecond; got != want {
		t.Fatalf("Activity heartbeat keepalive interval = %s, want %s", got, want)
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

func TestRuntimeInitialDependencyFailureClosesUnpublishedSnapshot(t *testing.T) {
	probe := &mutableRuntimeProbe{}
	var closed atomic.Bool
	options := testRuntimeOptions(t, &testWorker{}, &closed)
	var temporalCalls atomic.Int32
	options.TemporalFactory = TemporalClientFactoryFunc(func(context.Context, config.Config) (client.Client, error) {
		temporalCalls.Add(1)
		return nil, errors.New("Temporal must not start before required dependencies")
	})
	options.DependencyProbes = []DependencyProbe{probe}
	runtime, err := New(context.Background(), runtimeConfig(t), options)
	if !errors.Is(err, errRequiredDependencyUnavailable) {
		t.Fatalf("initial dependency error = %v", err)
	}
	if runtime != nil || temporalCalls.Load() != 0 || !closed.Load() {
		t.Fatalf("initial dependency failure runtime=%#v temporal-calls=%d clients-closed=%v", runtime, temporalCalls.Load(), closed.Load())
	}
}

func TestRuntimeReloadDependencyFailureKeepsPriorSnapshot(t *testing.T) {
	probe := &mutableRuntimeProbe{}
	probe.healthy.Store(true)
	var created atomic.Int32
	var closed atomic.Int32
	options := testRuntimeOptions(t, &testWorker{}, &atomic.Bool{})
	options.EngineFactory = EngineFactoryFunc(func(context.Context, *config.Snapshot) (llm.Engine, app.ClientSet, error) {
		created.Add(1)
		return testEngine{}, app.ClientSetFunc(func(context.Context) error {
			closed.Add(1)
			return nil
		}), nil
	})
	options.DependencyProbes = []DependencyProbe{probe}
	runtime, err := New(context.Background(), runtimeConfig(t), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	old := runtime.App.Current()
	probe.healthy.Store(false)
	if err := runtime.App.Reload(context.Background(), runtimeConfig(t)); !errors.Is(err, errRequiredDependencyUnavailable) {
		t.Fatalf("reload dependency error = %v", err)
	}
	if runtime.App.Current() != old {
		t.Fatal("failed reload replaced the active runtime snapshot")
	}
	if created.Load() != 2 || closed.Load() != 1 {
		t.Fatalf("reload client lifecycle created=%d closed=%d", created.Load(), closed.Load())
	}
}

func TestRuntimeMonitorPausesPollingAndRestoresReadyHealth(t *testing.T) {
	probe := &mutableRuntimeProbe{}
	probe.healthy.Store(true)
	controller := &monitoringWorker{}
	var closed atomic.Bool
	options := testRuntimeOptions(t, &testWorker{}, &closed)
	options.WorkerFactory = func(_ client.Client, _ string, _ worker.Options) (app.WorkerController, worker.ActivityRegistry, error) {
		return controller, testRegistry{}, nil
	}
	options.DependencyProbes = []DependencyProbe{probe}
	runtime, err := New(context.Background(), runtimeMonitorConfig(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("sandbox does not permit loopback listeners: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	if !runtime.Health.Ready() || controller.starts.Load() != 1 {
		t.Fatalf("initial monitor state ready=%v starts=%d", runtime.Health.Ready(), controller.starts.Load())
	}
	probe.healthy.Store(false)
	waitForRuntime(t, func() bool { return !runtime.Health.Ready() && controller.stops.Load() >= 1 })
	if runtime.Health.Live() != true {
		t.Fatal("dependency failure changed liveness")
	}
	if got := runtimeHealthStatus(t, runtime.HealthServer.Addr(), "/health/live"); got != http.StatusOK {
		t.Fatalf("live status during dependency failure = %d", got)
	}
	if got := runtimeHealthStatus(t, runtime.HealthServer.Addr(), "/health/ready"); got != http.StatusServiceUnavailable {
		t.Fatalf("ready status during dependency failure = %d", got)
	}
	probe.healthy.Store(true)
	waitForRuntime(t, func() bool { return runtime.Health.Ready() && controller.starts.Load() >= 2 })
	if got := runtimeHealthStatus(t, runtime.HealthServer.Addr(), "/health/ready"); got != http.StatusOK {
		t.Fatalf("ready status after dependency recovery = %d", got)
	}
}

func TestRuntimeMonitorContinuesCheckingWhilePausedWorkerDrains(t *testing.T) {
	probe := &countingRuntimeProbe{}
	probe.healthy.Store(true)
	releaseStop := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStop) }) }
	first := &drainingMonitoringWorker{
		stopEntered: make(chan struct{}, 1),
		releaseStop: releaseStop,
		stopExited:  make(chan struct{}, 1),
	}
	replacement := &monitoringWorker{}
	var built atomic.Int32
	var closed atomic.Bool
	options := testRuntimeOptions(t, &testWorker{}, &closed)
	options.WorkerFactory = func(_ client.Client, _ string, _ worker.Options) (app.WorkerController, worker.ActivityRegistry, error) {
		if built.Add(1) == 1 {
			return first, testRegistry{}, nil
		}
		return replacement, testRegistry{}, nil
	}
	options.DependencyProbes = []DependencyProbe{probe}
	runtime, err := New(context.Background(), runtimeMonitorConfig(t), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	t.Cleanup(release)
	if err := runtime.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("sandbox does not permit loopback listeners: %v", err)
		}
		t.Fatal(err)
	}
	if !runtime.Health.Ready() || first.starts.Load() != 1 {
		t.Fatalf("initial monitor state ready=%v starts=%d", runtime.Health.Ready(), first.starts.Load())
	}

	probe.healthy.Store(false)
	waitForRuntimeEvent(t, first.stopEntered, "first worker pause drain")
	waitForRuntime(t, func() bool { return !runtime.Health.Ready() && first.stops.Load() == 1 })

	healthyChecks := probe.healthyChecks.Load()
	probe.healthy.Store(true)
	waitForRuntime(t, func() bool { return probe.healthyChecks.Load() > healthyChecks })
	if runtime.Health.Ready() || replacement.starts.Load() != 0 || built.Load() != 1 {
		t.Fatalf("replacement started before pause drain completed: ready=%v replacement-starts=%d built=%d", runtime.Health.Ready(), replacement.starts.Load(), built.Load())
	}

	release()
	waitForRuntimeEvent(t, first.stopExited, "first worker pause drain completion")
	waitForRuntime(t, func() bool { return runtime.Health.Ready() && replacement.starts.Load() == 1 && built.Load() == 2 })
}

type mutableRuntimeProbe struct{ healthy atomic.Bool }

func (probe *mutableRuntimeProbe) Probe(context.Context) ProbeResult {
	if probe.healthy.Load() {
		return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusReady, Reason: ProbeReasonReady}
	}
	return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusUnavailable, Reason: ProbeReasonUnavailable}
}

type countingRuntimeProbe struct {
	healthy       atomic.Bool
	healthyChecks atomic.Int32
}

func (probe *countingRuntimeProbe) Probe(context.Context) ProbeResult {
	if probe.healthy.Load() {
		probe.healthyChecks.Add(1)
		return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusReady, Reason: ProbeReasonReady}
	}
	return ProbeResult{Dependency: DependencyRedis, Status: ProbeStatusUnavailable, Reason: ProbeReasonUnavailable}
}

type monitoringWorker struct {
	starts atomic.Int32
	stops  atomic.Int32
}

func (worker *monitoringWorker) Start() error {
	worker.starts.Add(1)
	return nil
}

func (worker *monitoringWorker) Stop() { worker.stops.Add(1) }

type drainingMonitoringWorker struct {
	starts      atomic.Int32
	stops       atomic.Int32
	stopEntered chan struct{}
	releaseStop <-chan struct{}
	stopExited  chan struct{}
}

func (worker *drainingMonitoringWorker) Start() error {
	worker.starts.Add(1)
	return nil
}

func (worker *drainingMonitoringWorker) Stop() {
	worker.stops.Add(1)
	select {
	case worker.stopEntered <- struct{}{}:
	default:
	}
	<-worker.releaseStop
	select {
	case worker.stopExited <- struct{}{}:
	default:
	}
}

func waitForRuntimeEvent(t *testing.T, event <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func runtimeMonitorConfig(t *testing.T) []byte {
	t.Helper()
	value := string(runtimeConfig(t))
	value = strings.Replace(value, "readiness_probe_interval: 5s", "readiness_probe_interval: 10ms", 1)
	value = strings.Replace(value, "readiness_probe_timeout: 2s", "readiness_probe_timeout: 5ms", 1)
	return []byte(value)
}

func waitForRuntime(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for runtime dependency transition")
}

func runtimeHealthStatus(t *testing.T, address, path string) int {
	t.Helper()
	response, err := (&http.Client{Timeout: time.Second}).Get("http://" + address + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
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
