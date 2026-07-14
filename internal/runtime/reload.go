package runtime

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"
)

const defaultConfigWatchInterval = time.Second

// ReloadFile compiles and verifies a complete file replacement before the app
// publishes it. Reload failures are deliberately reduced to a safe process
// boundary error: configuration contents, paths, and resolved references never
// become log fields or command output.
func (runtime *Runtime) ReloadFile(ctx context.Context, path string) error {
	if runtime == nil || runtime.App == nil {
		return errors.New("runtime is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runtime.App.ReloadFile(ctx, path); err != nil {
		runtime.recordReloadFailure(ctx, err)
		return safeReloadError(err)
	}
	runtime.Metrics.RecordConfigReload("success")
	version := ""
	if current := runtime.App.Current(); current != nil && current.Config != nil {
		version = current.Config.ConfigVersion()
	}
	runtime.Logger.Info(ctx, "configuration reloaded", slog.String("outcome", "success"), slog.String("config_version", version))
	return nil
}

func (runtime *Runtime) recordReloadFailure(ctx context.Context, err error) {
	if runtime == nil {
		return
	}
	runtime.Metrics.RecordConfigReload("failure")
	runtime.Logger.Error(ctx, "configuration reload failed", err, slog.String("outcome", "failure"))
}

func safeReloadError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return errors.New("reload configuration failed")
	}
}

// configFileWatcher intentionally uses metadata polling rather than a native
// watcher dependency. It observes atomic rename replacements as well as
// in-place updates, gives reload a complete file to compile, and keeps the
// portable process image small. It only emits a bounded notification; reload
// owns reading and validation.
type configFileWatcher struct {
	path     string
	interval time.Duration

	mu      sync.Mutex
	state   configFileState
	changes chan struct{}
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

type configFileState struct {
	info os.FileInfo
}

func newConfigFileWatcher(path string, interval time.Duration) (*configFileWatcher, error) {
	if path == "" {
		return nil, errors.New("configuration path is required")
	}
	if interval <= 0 {
		interval = defaultConfigWatchInterval
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	watcher := &configFileWatcher{
		path: path, interval: interval, state: configFileState{info: info},
		changes: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{}),
	}
	go watcher.watch()
	return watcher, nil
}

func (watcher *configFileWatcher) Changes() <-chan struct{} {
	if watcher == nil {
		return nil
	}
	return watcher.changes
}

func (watcher *configFileWatcher) Close() error {
	if watcher == nil {
		return nil
	}
	watcher.once.Do(func() {
		close(watcher.done)
		<-watcher.stopped
		close(watcher.changes)
	})
	return nil
}

func (watcher *configFileWatcher) watch() {
	defer close(watcher.stopped)
	ticker := time.NewTicker(watcher.interval)
	defer ticker.Stop()
	for {
		select {
		case <-watcher.done:
			return
		case <-ticker.C:
			next := readConfigFileState(watcher.path)
			watcher.mu.Lock()
			changed := !watcher.state.equal(next)
			if changed {
				watcher.state = next
			}
			watcher.mu.Unlock()
			if changed {
				select {
				case watcher.changes <- struct{}{}:
				default:
				}
			}
		}
	}
}

func readConfigFileState(path string) configFileState {
	info, err := os.Stat(path)
	if err != nil {
		return configFileState{}
	}
	return configFileState{info: info}
}

func (left configFileState) equal(right configFileState) bool {
	if left.info == nil || right.info == nil {
		return left.info == nil && right.info == nil
	}
	return os.SameFile(left.info, right.info) && left.info.Size() == right.info.Size() && left.info.Mode() == right.info.Mode() && left.info.ModTime().Equal(right.info.ModTime())
}

// combineReloadTriggers turns signal and watcher notifications into one
// nonblocking input for Runtime.RunWithReload. The caller owns cancellation;
// closing it never writes to an already closed channel.
func combineReloadTriggers(ctx context.Context, changes <-chan struct{}, signals <-chan os.Signal) <-chan struct{} {
	if ctx == nil {
		ctx = context.Background()
	}
	triggers := make(chan struct{}, 1)
	go func() {
		defer close(triggers)
		for changes != nil || signals != nil {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-changes:
				if !ok {
					changes = nil
					continue
				}
				notifyReload(triggers)
			case _, ok := <-signals:
				if !ok {
					signals = nil
					continue
				}
				notifyReload(triggers)
			}
		}
	}()
	return triggers
}

func notifyReload(triggers chan<- struct{}) {
	select {
	case triggers <- struct{}{}:
	default:
	}
}
