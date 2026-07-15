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
