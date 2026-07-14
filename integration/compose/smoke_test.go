package compose_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

type composeDocument struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image       string               `yaml:"image"`
	Build       composeBuild         `yaml:"build"`
	Profiles    []string             `yaml:"profiles"`
	DependsOn   map[string]yaml.Node `yaml:"depends_on"`
	ReadOnly    bool                 `yaml:"read_only"`
	CapDrop     []string             `yaml:"cap_drop"`
	Healthcheck map[string]yaml.Node `yaml:"healthcheck"`
}

type composeBuild struct {
	Context string `yaml:"context"`
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func readCompose(t *testing.T) (composeDocument, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document composeDocument
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&document); err != nil {
		t.Fatalf("decode compose.yaml: %v", err)
	}
	return document, data
}

func TestComposeFixtureIsOfflineSafe(t *testing.T) {
	document, raw := readCompose(t)
	if document.Name != "llmtw" {
		t.Fatalf("Compose project name = %q, want llmtw", document.Name)
	}
	for _, name := range []string{"temporal", "redis", "provider-mock", "worker"} {
		if _, ok := document.Services[name]; !ok {
			t.Fatalf("Compose fixture is missing service %q", name)
		}
	}

	for _, name := range []string{"temporal", "redis"} {
		service := document.Services[name]
		if !strings.Contains(service.Image, "@sha256:") {
			t.Errorf("%s image must be digest pinned, got %q", name, service.Image)
		}
		if service.Healthcheck == nil {
			t.Errorf("%s must expose a healthcheck", name)
		}
	}

	mock := document.Services["provider-mock"]
	if mock.Build.Context != "./deploy/local/provider-mock" {
		t.Errorf("provider-mock build context = %q", mock.Build.Context)
	}
	if mock.Healthcheck == nil {
		t.Error("provider-mock must expose a healthcheck")
	}

	worker := document.Services["worker"]
	if len(worker.Profiles) != 1 || worker.Profiles[0] != "worker" {
		t.Fatalf("worker profiles = %#v, want [worker]", worker.Profiles)
	}
	if !worker.ReadOnly {
		t.Error("worker must use a read-only root filesystem")
	}
	if len(worker.CapDrop) != 1 || worker.CapDrop[0] != "ALL" {
		t.Fatalf("worker cap_drop = %#v, want [ALL]", worker.CapDrop)
	}
	for _, dependency := range []string{"temporal", "redis", "provider-mock"} {
		if _, ok := worker.DependsOn[dependency]; !ok {
			t.Errorf("worker does not wait for %s health", dependency)
		}
	}

	if !strings.Contains(string(raw), "continuation_hmac") ||
		!strings.Contains(string(raw), "${LLMTW_CONTINUATION_KEY_FILE:-./.local/continuation-hmac}") {
		t.Error("continuation key must come from an explicit local secret-file override")
	}
}
