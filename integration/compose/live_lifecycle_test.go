//go:build composeliveintegration

package compose_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/config"
)

const (
	composeStatusPollInterval   = 100 * time.Millisecond
	composeStatusRequestTimeout = time.Second
	// The Compose worker factory installs Redis and blob-store probes. Runtime
	// checks required probes serially with an independent timeout for each.
	composeRequiredDependencyProbeCount = 2
)

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

	if got, want := composeReadinessTransitionTimeoutForConfig(configuration), 39*time.Second+composeStatusRequestTimeout+composeStatusPollInterval; got != want {
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

// composeReadinessTransitionTimeoutForConfig covers the longest worker
// recovery path: a configured graceful poller drain, one readiness monitor
// interval, and every required dependency probe. The probe count is explicit
// because CheckDependencyProbes applies an individual timeout serially. The
// final terms bound this test's own HTTP request and status polling rather
// than adding arbitrary headroom.
func composeReadinessTransitionTimeoutForConfig(configuration config.Config) time.Duration {
	return time.Duration(configuration.Temporal.Worker.GracefulStopTimeout) +
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
