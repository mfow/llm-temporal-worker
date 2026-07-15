package architecturetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedisIntegrationForwardsConfiguredContainerPrefixToRestartGate(t *testing.T) {
	root := repositoryRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), `LLMTW_REDIS_CONTAINER_PREFIX="$(REDIS_INTEGRATION_CONTAINER_PREFIX)"`) {
		t.Fatal("redis-integration does not pass its configured container prefix to the persistence gate")
	}

	testSource, err := os.ReadFile(filepath.Join(root, "storage", "redis", "shared_state_conformance_integration_test.go"))
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
	root := repositoryRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	target := redisIntegrationTarget(t, string(makefile))
	if !strings.Contains(target, "bash ./scripts/redact-compose-logs.sh") {
		t.Fatal("redis-integration must invoke its failure redactor through bash")
	}
}

func TestRedisPersistenceReopensAtCurrentContainerAddressAfterRestart(t *testing.T) {
	root := repositoryRoot(t)
	testSource, err := os.ReadFile(filepath.Join(root, "storage", "redis", "shared_state_conformance_integration_test.go"))
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
	root := repositoryRoot(t)
	testSource, err := os.ReadFile(filepath.Join(root, "storage", "redis", "shared_state_conformance_integration_test.go"))
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
