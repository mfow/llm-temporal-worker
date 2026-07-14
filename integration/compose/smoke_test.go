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
	User        string               `yaml:"user"`
	WorkingDir  string               `yaml:"working_dir"`
	Entrypoint  yaml.Node            `yaml:"entrypoint"`
	DependsOn   map[string]yaml.Node `yaml:"depends_on"`
	Command     yaml.Node            `yaml:"command"`
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

func TestComposeTemporalUsesPinnedPostgresStorage(t *testing.T) {
	document, raw := readCompose(t)
	postgres, ok := document.Services["postgres"]
	if !ok {
		t.Fatal("Compose fixture is missing the Temporal PostgreSQL service")
	}
	if !strings.Contains(postgres.Image, "@sha256:") {
		t.Errorf("postgres image must be digest pinned, got %q", postgres.Image)
	}
	if postgres.Healthcheck == nil {
		t.Error("postgres must expose a healthcheck before Temporal starts")
	}

	temporal := document.Services["temporal"]
	dependency, ok := temporal.DependsOn["postgres"]
	if !ok {
		t.Fatal("temporal does not wait for PostgreSQL health")
	}
	var condition struct {
		Condition string `yaml:"condition"`
	}
	if err := dependency.Decode(&condition); err != nil {
		t.Fatalf("decode PostgreSQL dependency: %v", err)
	}
	if condition.Condition != "service_healthy" {
		t.Fatalf("temporal PostgreSQL condition = %q, want service_healthy", condition.Condition)
	}

	for _, required := range []string{
		"DB: postgres12",
		"DB_PORT: \"5432\"",
		"POSTGRES_USER: temporal",
		"POSTGRES_PWD: ${LLMTW_POSTGRES_PASSWORD:-local-only}",
		"POSTGRES_SEEDS: postgres",
		"DYNAMIC_CONFIG_FILE_PATH: config/dynamicconfig/development-sql.yaml",
		"${LLMTW_POSTGRES_PASSWORD:-local-only}",
		"temporal-postgres-data:/var/lib/postgresql/data",
	} {
		if !strings.Contains(string(raw), required) {
			t.Errorf("Compose fixture is missing PostgreSQL Temporal input %q", required)
		}
	}
	if strings.Contains(string(raw), "DB: sqlite") {
		t.Error("Temporal auto-setup must not use unsupported sqlite storage")
	}
	if got := strings.Count(string(raw), "${LLMTW_POSTGRES_PASSWORD:-local-only}"); got != 2 {
		t.Fatalf("PostgreSQL password variable occurrences = %d, want the same variable on Postgres and Temporal", got)
	}
}

func TestLocalFixtureDocumentationPreservesFailClosedProviderEgress(t *testing.T) {
	root := repositoryRoot(t)
	for path, required := range map[string][]string{
		"deploy/local/README.md": {
			"parser/configuration/readiness fixture",
			"provider egress is not available",
			"content-free adapter",
			"weaken the policy",
		},
		"docs/architecture/deployment-and-operations.md": {
			"LLMTW_COMPOSE_LIVE=1 make compose-live-integration",
			"content-free injected adapter",
			"Docker-private-address bypass",
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

func TestComposeRedisRequiresTheWorkerNamedACL(t *testing.T) {
	document, raw := readCompose(t)
	redis := document.Services["redis"]
	if redis.User != "" {
		t.Fatalf("Redis ACL wrapper must invoke the image entrypoint as root so it can initialize /data, got user %q", redis.User)
	}
	if redis.WorkingDir != "/data" {
		t.Fatalf("Redis ACL wrapper working directory = %q, want /data for entrypoint volume ownership initialization", redis.WorkingDir)
	}
	if redis.Entrypoint.Kind != yaml.SequenceNode || len(redis.Entrypoint.Content) != 3 {
		t.Fatalf("Redis ACL wrapper entrypoint must be a three-argument shell invocation, got kind=%d len=%d", redis.Entrypoint.Kind, len(redis.Entrypoint.Content))
	}
	if redis.Command.Kind != yaml.SequenceNode || len(redis.Command.Content) != 0 {
		t.Fatalf("Redis ACL wrapper must clear the image command, got kind=%d len=%d", redis.Command.Kind, len(redis.Command.Content))
	}
	aclScript := redis.Entrypoint.Content[2].Value
	for _, required := range []string{
		"chown redis /tmp/llmtw-users.acl",
		"exec /usr/local/bin/docker-entrypoint.sh redis-server",
	} {
		if !strings.Contains(aclScript, required) {
			t.Errorf("Redis ACL wrapper is missing %q", required)
		}
	}
	aclOwnership := strings.Index(aclScript, "chown redis /tmp/llmtw-users.acl")
	entrypointHandoff := strings.Index(aclScript, "exec /usr/local/bin/docker-entrypoint.sh redis-server")
	if aclOwnership < 0 || entrypointHandoff < aclOwnership {
		t.Error("Redis ACL wrapper must prepare the ACL, then hand off as root to the image entrypoint so it can initialize /data and drop to redis")
	}
	if strings.Contains(aclScript, "exec /docker-entrypoint.sh") {
		t.Error("Redis ACL wrapper must use the image's resolved /usr/local/bin/docker-entrypoint.sh path")
	}
	for _, required := range []string{
		"REDIS_USERNAME: ${LLMTW_REDIS_USERNAME:-local}",
		"REDIS_PASSWORD: ${LLMTW_REDIS_PASSWORD:-local-only}",
		"umask 077",
		"user default off",
		"user %s on >%s ~* +@all",
		"--aclfile /tmp/llmtw-users.acl",
		"--save 60 1",
		"redis-cli --user \"$${REDIS_USERNAME}\" --pass \"$${REDIS_PASSWORD}\" ping",
		"redis-cli -h redis --user \"$${REDIS_USERNAME}\" --pass \"$${REDIS_PASSWORD}\"",
	} {
		if !strings.Contains(string(raw), required) {
			t.Errorf("Compose Redis ACL fixture is missing %q", required)
		}
	}
	if strings.Contains(string(raw), "--save \"60 1\"") {
		t.Error("Redis save interval and change count must be separate command-line arguments")
	}
	for _, inlineRule := range []string{
		"--user \"default off\"",
		"--user \"$${REDIS_USERNAME} on >$${REDIS_PASSWORD} ~* +@all\"",
	} {
		if !strings.Contains(string(raw), inlineRule) {
			continue
		}
		t.Error("Redis ACLs must use the generated aclfile rather than quoted command-line rules")
	}
}

func TestComposeProvisionerShellScriptsAreSingleArguments(t *testing.T) {
	document, _ := readCompose(t)
	for _, name := range []string{"redis-function-provisioner", "blob-volume-provisioner"} {
		service := document.Services[name]
		if service.Command.Kind != yaml.SequenceNode || len(service.Command.Content) != 1 {
			t.Errorf("%s command must be a one-element sequence so /bin/sh -c receives the full script, got kind=%d len=%d", name, service.Command.Kind, len(service.Command.Content))
		}
	}
}

func TestComposeWorkerUsesDurableFileBlobAndExactHealthEndpoints(t *testing.T) {
	document, raw := readCompose(t)
	worker := document.Services["worker"]
	if worker.Healthcheck == nil {
		t.Error("worker must have a healthcheck that verifies its live and ready endpoints")
	}
	if _, ok := document.Services["blob-volume-provisioner"]; !ok {
		t.Error("Compose fixture is missing a file-blob volume provisioner")
	}
	if _, ok := worker.DependsOn["blob-volume-provisioner"]; !ok {
		t.Error("worker does not wait for file-blob volume provisioning")
	}
	for _, required := range []string{
		"blob-data:/var/lib/llmtw/blobs",
		"/health/live",
		"/health/ready",
		"healthcheck",
	} {
		if !strings.Contains(string(raw), required) {
			t.Errorf("Compose fixture is missing %q", required)
		}
	}
	root := repositoryRoot(t)
	localConfig, err := os.ReadFile(filepath.Join(root, "deploy", "local", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"kind: file", "root: /var/lib/llmtw/blobs"} {
		if !strings.Contains(string(localConfig), required) {
			t.Errorf("local configuration is missing %q", required)
		}
	}
	deployment, err := os.ReadFile(filepath.Join(root, "deploy", "kubernetes", "base", "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/health/live", "/health/ready"} {
		if !strings.Contains(string(raw), endpoint) || !strings.Contains(string(deployment), endpoint) {
			t.Errorf("Compose and Kubernetes must both use %q", endpoint)
		}
	}
}

func TestComposeLiveIntegrationTargetIsExplicitAndFailsClosed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"compose-live-integration:",
		"LLMTW_COMPOSE_LIVE:-0",
		"docker info",
		"set -e;",
		"--profile worker",
		"build --no-cache --quiet",
		"temporal_port=0",
		"temporal_ui_port=0",
		"redis_port=0",
		"health_port=0",
		"metrics_port=0",
		"LLMTW_COMPOSE_TEMPORAL_UI_PORT",
		"$(COMPOSE) port temporal 7233",
		"$(COMPOSE) port temporal 8233",
		"$(COMPOSE) port redis 6379",
		"$(COMPOSE) port worker 8080",
		"$(COMPOSE) port worker 9090",
		"awk -v secret=\"$$redis_password\"",
		"[REDACTED]",
		"LLMTW_TEMPORAL_ADDRESS=\"$$temporal_address\"",
		"LLMTW_REDIS_ADDR=\"$$redis_address\"",
		"LLMTW_REDIS_USERNAME",
		"LLMTW_REDIS_PASSWORD",
	} {
		if !strings.Contains(string(data), required) {
			t.Errorf("compose live integration target is missing %q", required)
		}
	}
	for _, fixedPort := range []string{
		"LLMTW_COMPOSE_TEMPORAL_PORT:-17233",
		"LLMTW_COMPOSE_REDIS_PORT:-16380",
		"LLMTW_COMPOSE_HEALTH_PORT:-18080",
		"LLMTW_COMPOSE_METRICS_PORT:-19090",
	} {
		if strings.Contains(string(data), fixedPort) {
			t.Errorf("compose live integration target retains fixed host port %q", fixedPort)
		}
	}
}

func TestComposePublishedPortsDefaultToDockerAllocatedPorts(t *testing.T) {
	_, raw := readCompose(t)
	for _, required := range []string{
		"${LLMTW_COMPOSE_TEMPORAL_PORT:-0}:7233",
		"${LLMTW_COMPOSE_TEMPORAL_UI_PORT:-0}:8233",
		"${LLMTW_COMPOSE_REDIS_PORT:-0}:6379",
		"${LLMTW_COMPOSE_HEALTH_PORT:-0}:8080",
		"${LLMTW_COMPOSE_METRICS_PORT:-0}:9090",
	} {
		if !strings.Contains(string(raw), required) {
			t.Errorf("Compose fixture does not request a Docker-assigned host port for %q", required)
		}
	}
}

func TestCIExercisesAuthorizedComposeLiveGate(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		".github/workflows/pull-request.yml",
		".github/workflows/master.yml",
	} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "LLMTW_COMPOSE_LIVE=1 make compose-live-integration") {
			t.Errorf("%s must exercise the authorized Compose lifecycle gate", path)
		}
	}
}
