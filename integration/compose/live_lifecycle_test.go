//go:build composeliveintegration

package compose_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/config"
	"go.yaml.in/yaml/v4"
)

const (
	composeStatusPollInterval            = 100 * time.Millisecond
	composeStatusRequestTimeout          = time.Second
	composeContainerHealthPollInterval   = 100 * time.Millisecond
	composeContainerHealthInspectTimeout = time.Second
	// This idle Compose recovery test exercises the Temporal SDK v1.43.0
	// AggregatedWorker's two remote-poller stop phases: workflow then activity.
	// Each may consume WorkerStopTimeout, which this application maps from
	// GracefulStopTimeout. Active local work has a separate drain path and is
	// intentionally outside this readiness-recovery contract.
	composeTemporalRemotePollerStopPhaseCount = 2
	// The Compose worker factory installs Redis and blob-store probes. Runtime
	// checks required probes serially with an independent timeout for each.
	composeRequiredDependencyProbeCount = 2
)

type composeHealthcheckTiming struct {
	interval    time.Duration
	timeout     time.Duration
	startPeriod time.Duration
	retries     int
}

type composeContainerHealthSnapshot struct {
	status               string
	latestCheckStartedAt time.Time
}

type composeContainerHealthVerdict string

const (
	composeContainerHealthPending   composeContainerHealthVerdict = "pending"
	composeContainerHealthHealthy   composeContainerHealthVerdict = "healthy"
	composeContainerHealthUnhealthy composeContainerHealthVerdict = "unhealthy"
)

func TestComposeContainerHealthVerdictRequiresPostRestartProbe(t *testing.T) {
	containerStartedAt := time.Date(2026, time.July, 14, 20, 10, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		snapshot composeContainerHealthSnapshot
		want     composeContainerHealthVerdict
	}{
		{
			name: "stale unhealthy health status remains pending",
			snapshot: composeContainerHealthSnapshot{
				status:               "unhealthy",
				latestCheckStartedAt: containerStartedAt.Add(-time.Second),
			},
			want: composeContainerHealthPending,
		},
		{
			name: "stale healthy health status remains pending",
			snapshot: composeContainerHealthSnapshot{
				status:               "healthy",
				latestCheckStartedAt: containerStartedAt.Add(-time.Second),
			},
			want: composeContainerHealthPending,
		},
		{
			name: "health status without a recorded probe remains pending",
			snapshot: composeContainerHealthSnapshot{
				status: "unhealthy",
			},
			want: composeContainerHealthPending,
		},
		{
			name: "post-restart unhealthy health status fails promptly",
			snapshot: composeContainerHealthSnapshot{
				status:               "unhealthy",
				latestCheckStartedAt: containerStartedAt.Add(time.Second),
			},
			want: composeContainerHealthUnhealthy,
		},
		{
			name: "post-restart healthy health status succeeds",
			snapshot: composeContainerHealthSnapshot{
				status:               "healthy",
				latestCheckStartedAt: containerStartedAt.Add(time.Second),
			},
			want: composeContainerHealthHealthy,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := composeContainerHealthVerdictAfterRestart(test.snapshot, containerStartedAt); got != test.want {
				t.Fatalf("health verdict = %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseComposeContainerHealthSnapshot(t *testing.T) {
	snapshot, err := parseComposeContainerHealthSnapshot("unhealthy\n2026-07-14T20:09:55.000000000Z\n2026-07-14T20:10:01.000000000Z\n")
	if err != nil {
		t.Fatalf("parse health snapshot: %v", err)
	}
	if got, want := snapshot.status, "unhealthy"; got != want {
		t.Fatalf("health status = %q, want %q", got, want)
	}
	if got, want := snapshot.latestCheckStartedAt, time.Date(2026, time.July, 14, 20, 10, 1, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("latest health check started at = %s, want %s", got, want)
	}
}

func TestComposeContainerHealthTransitionTimeoutUsesHealthcheckContract(t *testing.T) {
	healthcheck := composeHealthcheckTiming{
		interval:    5 * time.Second,
		timeout:     3 * time.Second,
		startPeriod: 2 * time.Second,
		retries:     3,
	}

	if got, want := composeContainerHealthTransitionTimeout(healthcheck), 27*time.Second+100*time.Millisecond; got != want {
		t.Fatalf("Compose container health transition timeout = %s, want %s", got, want)
	}
}

func TestComposeRedisHealthcheckTimingComesFromManifest(t *testing.T) {
	healthcheck := composeRedisHealthcheckTiming(t)
	if got, want := healthcheck.interval, 5*time.Second; got != want {
		t.Fatalf("Redis healthcheck interval = %s, want %s", got, want)
	}
	if got, want := healthcheck.timeout, 3*time.Second; got != want {
		t.Fatalf("Redis healthcheck timeout = %s, want %s", got, want)
	}
	if got, want := healthcheck.retries, 24; got != want {
		t.Fatalf("Redis healthcheck retries = %d, want %d", got, want)
	}
	if got, want := composeContainerHealthTransitionTimeout(healthcheck), 193*time.Second+100*time.Millisecond; got != want {
		t.Fatalf("Redis health transition timeout = %s, want %s", got, want)
	}
}

func TestComposeReadinessTransitionTimeoutUsesWorkerAndProbeConfiguration(t *testing.T) {
	configuration := config.Config{
		Server: config.ServerConfig{
			ReadinessProbeInterval: config.Duration(5 * time.Second),
			ReadinessProbeTimeout:  config.Duration(2 * time.Second),
		},
		Temporal: config.TemporalConfig{
			Worker: config.TemporalWorkerConfig{
				GracefulStopTimeout: config.Duration(30 * time.Second),
			},
		},
	}

	if got, want := composeReadinessTransitionTimeoutForConfig(configuration), 70*time.Second+100*time.Millisecond; got != want {
		t.Fatalf("compose readiness transition timeout = %s, want %s", got, want)
	}
}

// TestComposeWorkerReadinessTracksRedis exercises the already-running worker
// profile rather than a test double. The Make target owns the Docker project
// and passes its Redis container name, so this test can prove that a loss of
// the durable admission dependency makes only readiness unavailable and that
// polling becomes eligible again after the same Redis instance is restored.
func TestComposeWorkerReadinessTracksRedis(t *testing.T) {
	address := os.Getenv("LLMTW_COMPOSE_WORKER_HEALTH_ADDR")
	container := os.Getenv("LLMTW_COMPOSE_REDIS_CONTAINER")
	if address == "" || container == "" {
		t.Skip("make compose-live-integration supplies the worker health address and isolated Redis container")
	}
	readinessTransitionTimeout := composeReadinessTransitionTimeout(t)

	assertComposeStatus(t, address, "/health/live", http.StatusOK)
	assertComposeStatus(t, address, "/health/ready", http.StatusOK)

	stopped := false
	t.Cleanup(func() {
		if stopped {
			runComposeDocker(t, "start", container)
		}
	})
	runComposeDocker(t, "stop", container)
	stopped = true
	waitForComposeStatus(t, address, "/health/ready", http.StatusServiceUnavailable, readinessTransitionTimeout)
	assertComposeStatus(t, address, "/health/live", http.StatusOK)

	runComposeDocker(t, "start", container)
	stopped = false
	restartedAt := composeContainerStartedAt(t, container)
	waitForComposeContainerHealthy(t, container, restartedAt, composeContainerHealthTransitionTimeout(composeRedisHealthcheckTiming(t)))
	waitForComposeStatus(t, address, "/health/ready", http.StatusOK, readinessTransitionTimeout)
	assertComposeStatus(t, address, "/health/live", http.StatusOK)
}

func runComposeDocker(t *testing.T, arguments ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v (%s)", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
}

func composeReadinessTransitionTimeout(t *testing.T) time.Duration {
	t.Helper()
	configurationPath := filepath.Join("..", "..", "deploy", "local", "config.yaml")
	data, err := os.ReadFile(configurationPath)
	if err != nil {
		t.Fatalf("read mounted Compose configuration %s: %v", configurationPath, err)
	}
	configuration, err := config.Load(data)
	if err != nil {
		t.Fatalf("load mounted Compose configuration %s: %v", configurationPath, err)
	}
	return composeReadinessTransitionTimeoutForConfig(configuration)
}

func composeRedisHealthcheckTiming(t *testing.T) composeHealthcheckTiming {
	t.Helper()
	document, _ := readCompose(t)
	redis, ok := document.Services["redis"]
	if !ok {
		t.Fatal("Compose fixture is missing Redis")
	}
	return composeHealthcheckTimingFromManifest(t, "Redis", redis.Healthcheck)
}

func composeHealthcheckTimingFromManifest(t *testing.T, service string, healthcheck map[string]yaml.Node) composeHealthcheckTiming {
	t.Helper()
	if healthcheck == nil {
		t.Fatalf("%s service is missing a healthcheck", service)
	}
	interval := composeHealthcheckDuration(t, service, "interval", healthcheck["interval"].Value, false)
	timeout := composeHealthcheckDuration(t, service, "timeout", healthcheck["timeout"].Value, false)
	startPeriod := composeHealthcheckDuration(t, service, "start_period", healthcheck["start_period"].Value, true)
	retries, err := strconv.Atoi(healthcheck["retries"].Value)
	if err != nil || retries < 1 {
		t.Fatalf("%s healthcheck retries = %q, want a positive integer", service, healthcheck["retries"].Value)
	}
	return composeHealthcheckTiming{
		interval:    interval,
		timeout:     timeout,
		startPeriod: startPeriod,
		retries:     retries,
	}
}

func composeHealthcheckDuration(t *testing.T, service, field, raw string, optional bool) time.Duration {
	t.Helper()
	if raw == "" && optional {
		return 0
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		t.Fatalf("%s healthcheck %s = %q, want a positive Go duration", service, field, raw)
	}
	return duration
}

// composeContainerHealthTransitionTimeout bounds the Docker health verdict
// after docker start returns. Docker runs the first check after interval and
// subsequent checks after each previous check completes. A passing check marks
// the container healthy; retries consecutive failed checks mark it unhealthy,
// which this test reports immediately rather than waiting for arbitrary later
// recovery. The final terms bound this test's own inspect call and polling
// interval rather than adding a sleep.
func composeContainerHealthTransitionTimeout(healthcheck composeHealthcheckTiming) time.Duration {
	return healthcheck.startPeriod + time.Duration(healthcheck.retries)*(healthcheck.interval+healthcheck.timeout) +
		composeContainerHealthInspectTimeout + composeContainerHealthPollInterval
}

// composeReadinessTransitionTimeoutForConfig covers the longest worker
// recovery path: each synchronous remote-poller graceful-stop phase exercised
// by this idle Compose test, one readiness monitor interval, and every
// required dependency probe. The phase and probe counts are explicit because
// the Temporal SDK and
// CheckDependencyProbes execute them serially. The final terms bound this
// test's own HTTP request and status polling rather than adding arbitrary
// headroom.
func composeReadinessTransitionTimeoutForConfig(configuration config.Config) time.Duration {
	return time.Duration(composeTemporalRemotePollerStopPhaseCount)*time.Duration(configuration.Temporal.Worker.GracefulStopTimeout) +
		time.Duration(configuration.Server.ReadinessProbeInterval) +
		time.Duration(composeRequiredDependencyProbeCount)*time.Duration(configuration.Server.ReadinessProbeTimeout) +
		composeStatusRequestTimeout + composeStatusPollInterval
}

func waitForComposeStatus(t *testing.T, address, path string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		status, err := composeStatus(address, path)
		if err == nil && status == want {
			return
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("status %d", status)
		}
		time.Sleep(composeStatusPollInterval)
	}
	t.Fatalf("%s did not reach HTTP %d within %s: %v", path, want, timeout, last)
}

func waitForComposeContainerHealthy(t *testing.T, container string, restartedAt time.Time, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "no Docker health status"
	for time.Now().Before(deadline) {
		snapshot, err := composeContainerHealthSnapshotForContainer(container)
		if err != nil {
			last = fmt.Sprintf("inspect error: %v", err)
		} else {
			switch composeContainerHealthVerdictAfterRestart(snapshot, restartedAt) {
			case composeContainerHealthHealthy:
				return
			case composeContainerHealthUnhealthy:
				t.Fatalf("Redis container reported post-restart Docker health status unhealthy before readiness recovered")
			default:
				if snapshot.status == "unhealthy" {
					last = "stale Docker health status \"unhealthy\" before a post-restart health check"
				} else {
					last = fmt.Sprintf("Docker health status %q", snapshot.status)
				}
			}
		}
		time.Sleep(composeContainerHealthPollInterval)
	}
	t.Fatalf("Redis container did not reach Docker health status healthy within %s: %s", timeout, last)
}

func composeContainerStartedAt(t *testing.T, container string) time.Time {
	t.Helper()
	output, err := composeDockerInspect(container, "{{.State.StartedAt}}")
	if err != nil {
		t.Fatalf("inspect Redis container start time: %v", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(output))
	if err != nil {
		t.Fatalf("parse Redis container start time: %v", err)
	}
	return startedAt
}

func composeContainerHealthSnapshotForContainer(container string) (composeContainerHealthSnapshot, error) {
	output, err := composeDockerInspect(container, "{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}\n{{if .State.Health}}{{range .State.Health.Log}}{{.Start}}\n{{end}}{{end}}")
	if err != nil {
		return composeContainerHealthSnapshot{}, err
	}
	return parseComposeContainerHealthSnapshot(output)
}

func composeDockerInspect(container, format string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), composeContainerHealthInspectTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", format, container).Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect Redis container: %w", err)
	}
	return string(output), nil
}

func parseComposeContainerHealthSnapshot(output string) (composeContainerHealthSnapshot, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return composeContainerHealthSnapshot{}, fmt.Errorf("Docker health status is empty")
	}
	snapshot := composeContainerHealthSnapshot{status: strings.TrimSpace(lines[0])}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		startedAt, err := time.Parse(time.RFC3339Nano, line)
		if err != nil {
			return composeContainerHealthSnapshot{}, fmt.Errorf("parse Docker health check timestamp %q: %w", line, err)
		}
		if startedAt.After(snapshot.latestCheckStartedAt) {
			snapshot.latestCheckStartedAt = startedAt
		}
	}
	return snapshot, nil
}

func composeContainerHealthVerdictAfterRestart(snapshot composeContainerHealthSnapshot, restartedAt time.Time) composeContainerHealthVerdict {
	if snapshot.latestCheckStartedAt.Before(restartedAt) {
		return composeContainerHealthPending
	}
	switch snapshot.status {
	case "healthy":
		return composeContainerHealthHealthy
	case "unhealthy":
		return composeContainerHealthUnhealthy
	default:
		return composeContainerHealthPending
	}
}

func assertComposeStatus(t *testing.T, address, path string, want int) {
	t.Helper()
	status, err := composeStatus(address, path)
	if err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Fatalf("%s status = %d, want %d", path, status, want)
	}
}

func composeStatus(address, path string) (int, error) {
	response, err := (&http.Client{Timeout: composeStatusRequestTimeout}).Get("http://" + address + path)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}
