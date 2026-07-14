package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/mfow/llm-temporal-worker/config"
)

type ClientSet interface {
	Close(context.Context) error
}

type ClientSetFunc func(context.Context) error

func (function ClientSetFunc) Close(ctx context.Context) error { return function(ctx) }

// RuntimeSnapshot couples an immutable config snapshot to the clients created
// from it. Acquired leases keep old clients alive while an Activity finishes;
// reload marks the snapshot draining before publishing its replacement.
type RuntimeSnapshot struct {
	Config  *config.Snapshot
	Clients ClientSet

	mu           sync.Mutex
	refs         int
	draining     bool
	closeStarted bool
	closeDone    chan struct{}
	closeErr     error
}

func NewRuntimeSnapshot(snapshot *config.Snapshot, clients ClientSet) (*RuntimeSnapshot, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime configuration snapshot is required")
	}
	return &RuntimeSnapshot{Config: snapshot, Clients: clients, closeDone: make(chan struct{})}, nil
}

func (snapshot *RuntimeSnapshot) Acquire() (*Lease, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime snapshot is nil")
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.draining {
		return nil, fmt.Errorf("runtime snapshot is draining")
	}
	snapshot.refs++
	return &Lease{snapshot: snapshot}, nil
}

func (snapshot *RuntimeSnapshot) Drain(ctx context.Context) error {
	if snapshot == nil {
		return nil
	}
	snapshot.mu.Lock()
	snapshot.draining = true
	shouldClose := snapshot.refs == 0 && !snapshot.closeStarted
	if shouldClose {
		snapshot.closeStarted = true
	}
	snapshot.mu.Unlock()
	if shouldClose {
		return snapshot.close(ctx)
	}
	return snapshot.WaitClosed(ctx)
}

func (snapshot *RuntimeSnapshot) WaitClosed(ctx context.Context) error {
	if snapshot == nil {
		return nil
	}
	select {
	case <-snapshot.closeDone:
		snapshot.mu.Lock()
		err := snapshot.closeErr
		snapshot.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (snapshot *RuntimeSnapshot) release() {
	snapshot.mu.Lock()
	if snapshot.refs > 0 {
		snapshot.refs--
	}
	shouldClose := snapshot.draining && snapshot.refs == 0 && !snapshot.closeStarted
	if shouldClose {
		snapshot.closeStarted = true
	}
	snapshot.mu.Unlock()
	if shouldClose {
		// Drain owns the timeout. A release can only race with Drain after it
		// has marked the snapshot draining, so background close is safe and
		// bounded by the caller's process shutdown policy.
		go func() { _ = snapshot.close(context.Background()) }()
	}
}

func (snapshot *RuntimeSnapshot) close(ctx context.Context) error {
	var err error
	if snapshot.Clients != nil {
		err = snapshot.Clients.Close(ctx)
	}
	snapshot.mu.Lock()
	snapshot.closeErr = err
	close(snapshot.closeDone)
	snapshot.mu.Unlock()
	return err
}

type Lease struct {
	snapshot *RuntimeSnapshot
	once     sync.Once
}

func (lease *Lease) Release() {
	if lease == nil || lease.snapshot == nil {
		return
	}
	lease.once.Do(lease.snapshot.release)
}

func (lease *Lease) Snapshot() *RuntimeSnapshot {
	if lease == nil {
		return nil
	}
	return lease.snapshot
}

type Options struct {
	InitialConfig []byte
	Builder       SnapshotBuilder
	Clients       func(context.Context, *config.Snapshot) (ClientSet, error)
}

type App struct {
	builder SnapshotBuilder
	clients func(context.Context, *config.Snapshot) (ClientSet, error)
	current atomic.Pointer[RuntimeSnapshot]
}

func New(ctx context.Context, options Options) (*App, error) {
	if len(options.InitialConfig) == 0 {
		return nil, fmt.Errorf("initial configuration is required")
	}
	snapshot, err := options.Builder.Build(ctx, options.InitialConfig)
	if err != nil {
		return nil, err
	}
	var clients ClientSet
	if options.Clients != nil {
		clients, err = options.Clients(ctx, snapshot)
		if err != nil {
			return nil, fmt.Errorf("construct initial clients: %w", err)
		}
	}
	runtimeSnapshot, err := NewRuntimeSnapshot(snapshot, clients)
	if err != nil {
		return nil, err
	}
	app := &App{builder: options.Builder, clients: options.Clients}
	app.current.Store(runtimeSnapshot)
	return app, nil
}

func (app *App) Current() *RuntimeSnapshot {
	if app == nil {
		return nil
	}
	return app.current.Load()
}

func (app *App) Acquire() (*Lease, error) {
	if app == nil {
		return nil, fmt.Errorf("app is nil")
	}
	snapshot := app.current.Load()
	if snapshot == nil {
		return nil, fmt.Errorf("app has no active snapshot")
	}
	return snapshot.Acquire()
}

func (app *App) Reload(ctx context.Context, data []byte) error {
	if app == nil {
		return fmt.Errorf("app is not initialized")
	}
	nextConfig, err := app.builder.Build(ctx, data)
	if err != nil {
		return err
	}
	var clients ClientSet
	if app.clients != nil {
		clients, err = app.clients(ctx, nextConfig)
		if err != nil {
			return fmt.Errorf("construct reloaded clients: %w", err)
		}
	}
	next, err := NewRuntimeSnapshot(nextConfig, clients)
	if err != nil {
		return err
	}
	old := app.current.Swap(next)
	if old == nil {
		return nil
	}
	if err := old.Drain(ctx); err != nil {
		return fmt.Errorf("drain previous snapshot: %w", err)
	}
	return nil
}

func (app *App) Close(ctx context.Context) error {
	if app == nil {
		return nil
	}
	old := app.current.Swap(nil)
	if old == nil {
		return nil
	}
	return old.Drain(ctx)
}
