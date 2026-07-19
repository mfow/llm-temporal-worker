package redis

import (
	"strings"
	"testing"
)

func TestConfiguredPrefixesIsolateEveryWorkerKey(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	left, err := newKeySpace(KeyOptions{Prefix: "worker-a", HashTag: "admission", KeySecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	right, err := newKeySpace(KeyOptions{Prefix: "worker-b", HashTag: "admission", KeySecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	leftKeys := []string{left.operationKey("tenant", "operation"), left.operationIndexKey("operation"), left.budgetKey("policy", "window"), left.continuationIndexKey("handle"), left.continuationKey("tenant", "handle"), left.continuationOperationKey("tenant", "parent", "operation")}
	rightKeys := []string{right.operationKey("tenant", "operation"), right.operationIndexKey("operation"), right.budgetKey("policy", "window"), right.continuationIndexKey("handle"), right.continuationKey("tenant", "handle"), right.continuationOperationKey("tenant", "parent", "operation")}
	for index := range leftKeys {
		if leftKeys[index] == rightKeys[index] {
			t.Fatalf("key %d aliases across prefixes: %q", index, leftKeys[index])
		}
		if !strings.HasPrefix(leftKeys[index], "worker-a:{admission}:") || !strings.HasPrefix(rightKeys[index], "worker-b:{admission}:") {
			t.Fatalf("key %d lost configured namespace: %q / %q", index, leftKeys[index], rightKeys[index])
		}
	}
}
