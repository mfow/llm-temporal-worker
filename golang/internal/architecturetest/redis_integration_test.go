package architecturetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedisIntegrationForwardsConfiguredContainerPrefixToRestartGate(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(moduleRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), `LLMTW_REDIS_CONTAINER_PREFIX="$(REDIS_INTEGRATION_CONTAINER_PREFIX)"`) {
		t.Fatal("redis-integration does not pass its configured container prefix to the persistence gate")
	}

	testSource, err := os.ReadFile(filepath.Join(moduleRoot(t), "storage", "redis", "shared_state_conformance_integration_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`os.Getenv("LLMTW_REDIS_CONTAINER_PREFIX")`,
		`isLiveRedisPersistenceContainer(container, configuredPrefix)`,
	} {
		if !strings.Contains(string(testSource), required) {
			t.Errorf("Redis persistence gate does not honor configured container prefixes: missing %q", required)
		}
	}
}

func TestRedisIntegrationFailureRedactorRunsThroughBash(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(moduleRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	target := redisIntegrationTarget(t, string(makefile))
	if !strings.Contains(target, "bash ./scripts/redact-compose-logs.sh") {
		t.Fatal("redis-integration must invoke its failure redactor through bash")
	}
}

func TestRedisBenchmarkIsOperatorGatedAndExcludedFromCI(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(moduleRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	target := redisBenchmarkTarget(t, string(makefile))
	for _, required := range []string{
		"LLMTW_REDIS_BENCHMARK=1",
		"LLMTW_REDIS_BENCHMARK_ALLOW_MUTATION=1",
		"LLMTW_REDIS_BENCHMARK_ADDR",
		"CI",
		"-tags=redisbenchmark",
		"BenchmarkGenerateRedisAdmissionAndCompile",
	} {
		if !strings.Contains(target, required) {
			t.Errorf("redis-benchmark is missing required operator guard %q", required)
		}
	}
	for _, forbidden := range []string{"docker", "FunctionLoad", "LLMTW_REDIS_TEST_PROVISION"} {
		if strings.Contains(target, forbidden) {
			t.Errorf("redis-benchmark must not contain %q", forbidden)
		}
	}
	compileTarget := redisBenchmarkCompileTarget(t, string(makefile))
	for _, required := range []string{
		"$(GO) test -tags=redisbenchmark ./engine -run '^$$' -bench '^$$'",
	} {
		if !strings.Contains(compileTarget, required) {
			t.Errorf("redis-benchmark-compile is missing %q", required)
		}
	}
	for _, forbidden := range []string{"LLMTW_REDIS_BENCHMARK", "docker", "Function", "redis-benchmark:"} {
		if strings.Contains(compileTarget, forbidden) {
			t.Errorf("redis-benchmark-compile must not contain %q", forbidden)
		}
	}
	for _, workflow := range []string{"master.yml", "pull-request.yml"} {
		data, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", workflow))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "make redis-benchmark-compile") {
			t.Fatalf("%s must compile the build-tagged Redis benchmark", workflow)
		}
		if strings.Contains(string(data), "make redis-benchmark\n") {
			t.Fatalf("%s must not execute the operator-only redis-benchmark target", workflow)
		}
	}
}

func TestRedisPersistenceReopensAtCurrentContainerAddressAfterRestart(t *testing.T) {
	testSource, err := os.ReadFile(filepath.Join(moduleRoot(t), "storage", "redis", "shared_state_conformance_integration_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`runLiveRedisDocker(t, "restart", container)`,
		`reopenLiveRedisAfterRestart(t, container)`,
		`liveRedisAddressForContainer(container)`,
	} {
		if !strings.Contains(string(testSource), required) {
			t.Errorf("Redis restart persistence test is missing %q", required)
		}
	}
}

func TestRedisPersistenceCleanupUsesRestartedClient(t *testing.T) {
	testSource, err := os.ReadFile(filepath.Join(moduleRoot(t), "storage", "redis", "shared_state_conformance_integration_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "func TestLiveRedisConfiguredPersistenceSurvivesRestart(t *testing.T) {"
	const end = "\nfunc TestLiveRedisConfiguredPersistenceGateAllowsOverriddenPrefix"
	persistenceTest := string(testSource)
	startOffset := strings.Index(persistenceTest, start)
	if startOffset < 0 {
		t.Fatal("Redis restart persistence test is missing")
	}
	endOffset := strings.Index(persistenceTest[startOffset:], end)
	if endOffset < 0 {
		t.Fatal("Redis restart persistence test boundary is missing")
	}
	persistenceTest = persistenceTest[startOffset : startOffset+endOffset]
	if strings.Contains(persistenceTest, "cleanupLivePrefix(t, client, keys.Prefix)") {
		t.Fatal("Redis restart persistence cleanup must not retain the pre-restart client")
	}
	if !strings.Contains(persistenceTest, "cleanupLivePrefix(t, restartedClient, keys.Prefix)") {
		t.Fatal("Redis restart persistence cleanup must use the restarted client")
	}
}

func redisIntegrationTarget(t *testing.T, makefile string) string {
	t.Helper()
	const start = "redis-integration:\n"
	const end = "\n\n# Builds a fresh local image"
	startOffset := strings.Index(makefile, start)
	if startOffset < 0 {
		t.Fatal("Makefile is missing redis-integration")
	}
	endOffset := strings.Index(makefile[startOffset:], end)
	if endOffset < 0 {
		t.Fatal("redis-integration is missing its target boundary")
	}
	return makefile[startOffset : startOffset+endOffset]
}

func redisBenchmarkTarget(t *testing.T, makefile string) string {
	t.Helper()
	const start = "redis-benchmark:\n"
	const end = "\n\n# Runs the readiness recovery gate"
	startOffset := strings.Index(makefile, start)
	if startOffset < 0 {
		t.Fatal("Makefile is missing redis-benchmark")
	}
	endOffset := strings.Index(makefile[startOffset:], end)
	if endOffset < 0 {
		t.Fatal("redis-benchmark is missing its target boundary")
	}
	return makefile[startOffset : startOffset+endOffset]
}

func redisBenchmarkCompileTarget(t *testing.T, makefile string) string {
	t.Helper()
	const start = "redis-benchmark-compile:\n"
	const end = "\n\n# Measures Generate"
	startOffset := strings.Index(makefile, start)
	if startOffset < 0 {
		t.Fatal("Makefile is missing redis-benchmark-compile")
	}
	endOffset := strings.Index(makefile[startOffset:], end)
	if endOffset < 0 {
		t.Fatal("redis-benchmark-compile is missing its target boundary")
	}
	return makefile[startOffset : startOffset+endOffset]
}
