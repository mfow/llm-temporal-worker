// Package runtime composes the validated configuration, Temporal worker,
// probe servers, and reloadable application clients. It deliberately keeps
// provider construction behind EngineFactory: a process must not start with a
// misleading ready state when its provider/state implementation is absent.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mfow/llm-temporal-worker/activity"
	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/internal/app"
	"github.com/mfow/llm-temporal-worker/internal/httpserver"
	"github.com/mfow/llm-temporal-worker/internal/observability"
	"github.com/mfow/llm-temporal-worker/internal/secrets"
	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"go.temporal.io/sdk/client"
)

var (
	// ErrEngineFactoryUnavailable is returned when the process has not been
	// given a provider/state-backed engine factory. Failing before worker start
	// is safer than accepting Activities that can never perform inference.
	ErrEngineFactoryUnavailable = errors.New("provider-backed engine factory is not configured")
	// ErrTemporalClientUnavailable is returned when the Temporal factory gives
	// runtime an unusable client.
	ErrTemporalClientUnavailable = errors.New("Temporal client factory returned no client")
)

// EngineFactory constructs the provider-neutral engine and the clients owned
// by one immutable configuration snapshot. The returned ClientSet must not
// retain secret values in errors or logs.
type EngineFactory interface {
	Build(context.Context, *config.Snapshot) (llm.Engine, app.ClientSet, error)
}

// EngineFactoryFunc adapts a function to EngineFactory.
type EngineFactoryFunc func(context.Context, *config.Snapshot) (llm.Engine, app.ClientSet, error)

func (function EngineFactoryFunc) Build(ctx context.Context, snapshot *config.Snapshot) (llm.Engine, app.ClientSet, error) {
	return function(ctx, snapshot)
}

// UnavailableEngineFactory makes the missing production provider wiring
// explicit. It is the default used by the CLI until a deployment supplies a
// provider-backed factory through the process composition layer.
type UnavailableEngineFactory struct{}

func (UnavailableEngineFactory) Build(context.Context, *config.Snapshot) (llm.Engine, app.ClientSet, error) {
	return nil, nil, ErrEngineFactoryUnavailable
}

// Options controls process composition. TemporalFactory and EngineFactory are
// injectable so package tests never require credentials or a Temporal cluster.
type Options struct {
	Resolver       config.ReferenceResolver
	SecretResolver secrets.Resolver

	TemporalFactory TemporalClientFactory
	EngineFactory   EngineFactory
	WorkerFactory   app.WorkerFactory

	Health  *httpserver.HealthState
	Metrics *observability.Metrics

	// Identity overrides the generated Temporal identity.
	Identity string
}

// Runtime owns all process resources created from one initial configuration.
// Reloading the App swaps engines while the worker's dynamic engine lease
// keeps in-flight calls on their captured snapshot.
type Runtime struct {
	App      *app.App
	Temporal client.Client
	Worker   *app.TemporalWorker
	Health   *httpserver.HealthState
	Metrics  *observability.Metrics

	HealthServer  *httpserver.Server
	MetricsServer *httpserver.Server

	shutdown *app.ShutdownCoordinator
	timeout  time.Duration

	mu      sync.Mutex
	started bool
}

// New validates and composes a runtime. It performs no provider calls when
// EngineFactory is unavailable; the returned error is safe to show in CLI
// output and contains neither config contents nor resolved secret values.
func New(ctx context.Context, data []byte, options Options) (*Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(data) == 0 {
		return nil, errors.New("runtime configuration is required")
	}
	if options.EngineFactory == nil {
		return nil, ErrEngineFactoryUnavailable
	}
	references := options.Resolver
	if references == nil {
		secretResolver := options.SecretResolver
		if secretResolver == nil {
			secretResolver = secrets.New(secrets.Options{})
		}
		references = secrets.ConfigResolver{Resolver: secretResolver}
	}
	builder := app.SnapshotBuilder{References: references}
	var engineFactory = options.EngineFactory
	application, err := app.New(ctx, app.Options{
		InitialConfig: data,
		Builder:       builder,
		Clients: func(buildContext context.Context, snapshot *config.Snapshot) (app.ClientSet, error) {
			engine, clients, err := engineFactory.Build(buildContext, snapshot)
			if err != nil {
				return nil, &engineFactoryError{cause: err}
			}
			if engine == nil {
				return nil, errors.New("engine factory returned no engine")
			}
			return &snapshotClients{engine: engine, clients: clients}, nil
		},
	})
	if err != nil {
		var factoryError *engineFactoryError
		if errors.As(err, &factoryError) {
			return nil, fmt.Errorf("construct provider-backed engine: %w", unwrapEngineFactoryError(err))
		}
		return nil, err
	}
	configuration := application.Current().Config.Config()
	temporalFactory := options.TemporalFactory
	if temporalFactory == nil {
		temporalFactory = DefaultTemporalClientFactory{Identity: options.Identity}
	}
	temporalClient, err := temporalFactory.New(ctx, configuration)
	if err != nil {
		_ = application.Close(context.Background())
		return nil, safeTemporalFactoryError(err)
	}
	if temporalClient == nil {
		_ = application.Close(context.Background())
		return nil, ErrTemporalClientUnavailable
	}
	health := options.Health
	if health == nil {
		health = httpserver.NewHealthState()
	}
	metrics := options.Metrics
	if metrics == nil {
		metrics, err = newMetrics(configuration)
		if err != nil {
			temporalClient.Close()
			_ = application.Close(context.Background())
			return nil, fmt.Errorf("construct metrics: %w", err)
		}
	}
	dynamic := &snapshotEngine{application: application}
	identity := options.Identity
	if identity == "" {
		identity = (DefaultTemporalClientFactory{}).identity(configuration.Temporal.IdentityPrefix)
	}
	worker, err := app.NewWorker(app.WorkerOptions{
		Client:                         temporalClient,
		TaskQueue:                      configuration.Temporal.TaskQueue,
		Identity:                       identity,
		MaxConcurrentActivities:        configuration.Temporal.Worker.MaxConcurrentActivities,
		MaxConcurrentActivityTaskPolls: configuration.Temporal.Worker.MaxConcurrentActivityTaskPolls,
		GracefulStopTimeout:            time.Duration(configuration.Temporal.Worker.GracefulStopTimeout),
		Activities: &activity.Activities{
			Engine:        dynamic,
			Heartbeater:   &activity.TemporalHeartbeater{},
			PayloadLimits: activity.PayloadLimits{MaxInlineBytes: configuration.Server.InlinePayloadBytes},
		},
		Health:  health,
		Metrics: metrics,
		Factory: options.WorkerFactory,
	})
	if err != nil {
		temporalClient.Close()
		_ = application.Close(context.Background())
		return nil, err
	}
	healthServer, err := httpserver.New(httpserver.Options{
		Address: configuration.Server.HealthAddress,
		Health:  health,
		Metrics: metrics.Handler(),
	})
	if err != nil {
		temporalClient.Close()
		_ = application.Close(context.Background())
		return nil, fmt.Errorf("construct health server: %w", err)
	}
	var metricsServer *httpserver.Server
	if configuration.Server.MetricsAddress != configuration.Server.HealthAddress {
		metricsServer, err = httpserver.New(httpserver.Options{
			Address: configuration.Server.MetricsAddress,
			Health:  health,
			Metrics: metrics.Handler(),
		})
		if err != nil {
			temporalClient.Close()
			_ = application.Close(context.Background())
			return nil, fmt.Errorf("construct metrics server: %w", err)
		}
	}
	timeout := time.Duration(configuration.Server.ShutdownTimeout)
	coordinator, err := app.NewShutdownCoordinator(app.ShutdownOptions{
		Worker: worker,
		Health: health,
		CloseApp: func(closeContext context.Context) error {
			return closeResources(closeContext, application, temporalClient, healthServer, metricsServer, health)
		},
		Telemetry: []app.TelemetryFlusher{app.FlushFunc(func(context.Context) error { return nil })},
		Timeout:   timeout,
	})
	if err != nil {
		temporalClient.Close()
		_ = application.Close(context.Background())
		return nil, err
	}
	return &Runtime{
		App: application, Temporal: temporalClient, Worker: worker,
		Health: health, Metrics: metrics,
		HealthServer: healthServer, MetricsServer: metricsServer,
		shutdown: coordinator, timeout: timeout,
	}, nil
}

func newMetrics(configuration config.Config) (*observability.Metrics, error) {
	endpoints := make([]string, 0, len(configuration.Endpoints))
	models := make([]string, 0, len(configuration.Models))
	policies := make([]string, 0, len(configuration.Budgets.Policies))
	for endpoint := range configuration.Endpoints {
		endpoints = append(endpoints, endpoint)
	}
	for model := range configuration.Models {
		models = append(models, model)
	}
	for _, policy := range configuration.Budgets.Policies {
		policies = append(policies, policy.ID)
	}
	return observability.NewMetrics(observability.AllowedValues{Endpoints: endpoints, Models: models, Policies: policies})
}

// Start starts probe listeners before Temporal polling. Readiness is set by
// the worker only after its controller starts successfully.
func (runtime *Runtime) Start() error {
	if runtime == nil || runtime.Worker == nil {
		return errors.New("runtime is not initialized")
	}
	runtime.mu.Lock()
	if runtime.started {
		runtime.mu.Unlock()
		return errors.New("runtime is already started")
	}
	runtime.mu.Unlock()
	if err := runtime.HealthServer.Start(); err != nil {
		return err
	}
	if runtime.MetricsServer != nil {
		if err := runtime.MetricsServer.Start(); err != nil {
			_ = runtime.HealthServer.Shutdown(context.Background())
			return err
		}
	}
	if err := runtime.Worker.Start(); err != nil {
		_ = runtime.HealthServer.Shutdown(context.Background())
		if runtime.MetricsServer != nil {
			_ = runtime.MetricsServer.Shutdown(context.Background())
		}
		return err
	}
	runtime.mu.Lock()
	runtime.started = true
	runtime.mu.Unlock()
	return nil
}

// Shutdown applies the readiness-first, worker-stop, client-drain, telemetry
// flush ordering and is safe to call more than once.
func (runtime *Runtime) Shutdown(ctx context.Context) error {
	if runtime == nil || runtime.shutdown == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := runtime.shutdown.Shutdown(ctx)
	runtime.Health.SetLive(false)
	return err
}

// Run starts the process and blocks until cancellation or a probe listener
// fails. Cancellation is a successful graceful exit when shutdown succeeds.
func (runtime *Runtime) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runtime.Start(); err != nil {
		return err
	}
	var healthErrors <-chan error
	var metricsErrors <-chan error
	if runtime.HealthServer != nil {
		healthErrors = runtime.HealthServer.Errors()
	}
	if runtime.MetricsServer != nil {
		metricsErrors = runtime.MetricsServer.Errors()
	}
	for healthErrors != nil || metricsErrors != nil {
		select {
		case <-ctx.Done():
			return runtime.gracefulShutdown()
		case err, ok := <-healthErrors:
			if !ok {
				healthErrors = nil
				continue
			}
			if err != nil {
				shutdownErr := runtime.gracefulShutdown()
				return errors.Join(err, shutdownErr)
			}
		case err, ok := <-metricsErrors:
			if !ok {
				metricsErrors = nil
				continue
			}
			if err != nil {
				shutdownErr := runtime.gracefulShutdown()
				return errors.Join(err, shutdownErr)
			}
		}
	}
	return runtime.gracefulShutdown()
}

func (runtime *Runtime) gracefulShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), runtime.timeout)
	defer cancel()
	return runtime.Shutdown(ctx)
}

func closeResources(ctx context.Context, application *app.App, temporalClient client.Client, healthServer, metricsServer *httpserver.Server, health *httpserver.HealthState) error {
	if health != nil {
		health.SetReady(false)
	}
	var errs []error
	if healthServer != nil {
		if err := healthServer.Shutdown(ctx); err != nil {
			errs = append(errs, errors.New("shutdown health server failed"))
		}
	}
	if metricsServer != nil {
		if err := metricsServer.Shutdown(ctx); err != nil {
			errs = append(errs, errors.New("shutdown metrics server failed"))
		}
	}
	if application != nil {
		if err := application.Close(ctx); err != nil {
			errs = append(errs, errors.New("close runtime app failed"))
		}
	}
	if temporalClient != nil {
		temporalClient.Close()
	}
	return errors.Join(errs...)
}

// RunWorker is the CLI-facing entry point. It intentionally uses the default
// unavailable EngineFactory until provider/state composition is supplied by a
// deployment-specific process layer.
func RunWorker(ctx context.Context, data []byte, _ io.Writer) error {
	_, err := New(ctx, data, Options{EngineFactory: UnavailableEngineFactory{}})
	return err
}

type snapshotClients struct {
	engine  llm.Engine
	clients app.ClientSet
}

func (clients *snapshotClients) Engine() llm.Engine {
	if clients == nil {
		return nil
	}
	return clients.engine
}

func (clients *snapshotClients) Close(ctx context.Context) error {
	if clients == nil || clients.clients == nil {
		return nil
	}
	return clients.clients.Close(ctx)
}

type snapshotEngine struct{ application *app.App }

func (engine *snapshotEngine) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	if engine == nil || engine.application == nil {
		return llm.Response{}, provider.NewError(provider.CodeInternal, provider.PhaseFinalize, provider.DispatchNotDispatched, provider.RetryNever, "runtime engine is unavailable")
	}
	lease, err := engine.application.Acquire()
	if err != nil {
		return llm.Response{}, provider.NewError(provider.CodeInternal, provider.PhasePlan, provider.DispatchNotDispatched, provider.RetryNever, "runtime snapshot is unavailable")
	}
	defer lease.Release()
	clients, ok := lease.Snapshot().Clients.(*snapshotClients)
	if !ok || clients.Engine() == nil {
		return llm.Response{}, provider.NewError(provider.CodeInternal, provider.PhasePlan, provider.DispatchNotDispatched, provider.RetryNever, "runtime engine is unavailable")
	}
	return clients.Engine().Generate(ctx, request)
}

var _ llm.Engine = (*snapshotEngine)(nil)

// engineFactoryError deliberately hides an injectable factory's Error text.
// Factory implementations are required to return safe errors, but the
// process boundary still fails closed if one accidentally contains payload or
// credential material.
type engineFactoryError struct{ cause error }

func (err *engineFactoryError) Error() string { return "engine construction failed" }

func (err *engineFactoryError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func unwrapEngineFactoryError(err error) error {
	var factoryError *engineFactoryError
	if !errors.As(err, &factoryError) || factoryError == nil {
		return errors.New("engine construction failed")
	}
	if errors.Is(factoryError.cause, ErrEngineFactoryUnavailable) {
		return ErrEngineFactoryUnavailable
	}
	if errors.Is(factoryError.cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(factoryError.cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("engine construction failed")
}

func safeTemporalFactoryError(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("construct Temporal client: %w", context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("construct Temporal client: %w", context.DeadlineExceeded)
	}
	return errors.New("construct Temporal client failed")
}
