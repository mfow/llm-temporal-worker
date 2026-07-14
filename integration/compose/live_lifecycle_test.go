//go:build composeliveintegration

package compose_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

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
	waitForComposeStatus(t, address, "/health/ready", http.StatusServiceUnavailable)
	assertComposeStatus(t, address, "/health/live", http.StatusOK)

	runComposeDocker(t, "start", container)
	stopped = false
	waitForComposeStatus(t, address, "/health/ready", http.StatusOK)
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

func waitForComposeStatus(t *testing.T, address, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
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
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not reach HTTP %d: %v", path, want, last)
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
	response, err := (&http.Client{Timeout: time.Second}).Get("http://" + address + path)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}
