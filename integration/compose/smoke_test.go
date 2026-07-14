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

func TestLocalFixtureDocumentationPreservesFailClosedProviderEgress(t *testing.T) {
	root := repositoryRoot(t)
	for path, required := range map[string][]string{
		"deploy/local/README.md": {
			"parser/configuration/readiness fixture",
			"provider egress is not available",
			"weaken the policy",
		},
		"docs/architecture/deployment-and-operations.md": {
			"parser/configuration/readiness-only",
			"does not execute a fixture Activity",
			"must not create a Docker-private-address bypass",
		},
	} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, phrase := range required {
			if !strings.Contains(string(data), phrase) {
				t.Errorf("%s must state %q", path, phrase)
			}
		}
	}
}

func TestWorkerComposeProvisionsAdmissionFunctionBeforeStart(t *testing.T) {
	document, raw := readCompose(t)
	provisioner, ok := document.Services["redis-function-provisioner"]
	if !ok {
		t.Fatal("Compose fixture is missing redis Function provisioner")
	}
	if len(provisioner.Profiles) != 1 || provisioner.Profiles[0] != "worker" {
		t.Fatalf("provisioner profiles = %#v, want [worker]", provisioner.Profiles)
	}
	if _, ok := provisioner.DependsOn["redis"]; !ok {
		t.Fatal("redis Function provisioner does not wait for Redis")
	}
	worker := document.Services["worker"]
	dependency, ok := worker.DependsOn["redis-function-provisioner"]
	if !ok {
		t.Fatal("worker does not wait for Redis Function provisioning")
	}
	var condition struct {
		Condition string `yaml:"condition"`
	}
	if err := dependency.Decode(&condition); err != nil {
		t.Fatalf("decode provisioner dependency: %v", err)
	}
	if condition.Condition != "service_completed_successfully" {
		t.Fatalf("worker provisioner condition = %q, want service_completed_successfully", condition.Condition)
	}
	for _, required := range []string{
		"FUNCTION LOAD",
		"./storage/redis/functions/admission.lua",
		"llmtw_admission_v1",
		"admission_v1",
	} {
		if !strings.Contains(string(raw), required) {
			t.Errorf("Compose fixture is missing explicit Redis Function provisioning input %q", required)
		}
	}
}

func TestRedisFunctionProvisionerEscapesShellVariablesFromCompose(t *testing.T) {
	_, raw := readCompose(t)
	for _, variable := range []string{"library", "version", "source"} {
		if !strings.Contains(string(raw), "$${"+variable+"}") {
			t.Errorf("provisioner does not escape shell variable %q from Compose interpolation", variable)
		}
	}
}
