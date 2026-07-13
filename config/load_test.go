package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/llm"
)

func exampleYAML(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLoadCompleteExample(t *testing.T) {
	loaded, err := config.Load(exampleYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != config.APIVersion || loaded.Environment != "production" {
		t.Fatalf("loaded identity = %#v", loaded)
	}
	classes := loaded.Endpoints["openai-prod"].ServiceClasses
	if len(classes) != 3 {
		t.Fatalf("openai service classes = %#v", classes)
	}
	for _, class := range []llm.ServiceClass{llm.ServiceClassEconomy, llm.ServiceClassStandard, llm.ServiceClassPriority} {
		if _, ok := classes[class]; !ok {
			t.Fatalf("missing public service class %q", class)
		}
	}
	if _, ok := classes[llm.ServiceClass("provider_default")]; ok {
		t.Fatal("configuration exposed a provider-default public service class")
	}
}

func TestLoadRejectsUnknownDuplicateAndFourthClass(t *testing.T) {
	unknown := append(exampleYAML(t), []byte("\nunknown_field: true\n")...)
	if _, err := config.Load(unknown); err == nil {
		t.Fatal("accepted an unknown top-level field")
	}
	duplicate := append(exampleYAML(t), []byte("\nversion: llm-temporal-worker/v1\n")...)
	if _, err := config.Load(duplicate); err == nil {
		t.Fatal("accepted a duplicate top-level field")
	}
	fourth := strings.Replace(string(exampleYAML(t)), "service_classes:\n      economy:", "service_classes:\n      turbo:\n        provider_value: turbo\n      economy:", 1)
	if _, err := config.Load([]byte(fourth)); err == nil {
		t.Fatal("accepted a fourth public service class")
	}
}

func TestLoadRejectsUnsafeValuesAndReferences(t *testing.T) {
	cases := map[string]string{
		"unsafe URL":     strings.Replace(string(exampleYAML(t)), "https://api.openai.com/v1", "http://api.openai.com/v1", 1),
		"timeout":        strings.Replace(string(exampleYAML(t)), "timeout: 115s", "timeout: 121s", 1),
		"retention":      strings.Replace(string(exampleYAML(t)), "ambiguous_retention: 90d", "ambiguous_retention: 1d", 1),
		"overflow":       strings.Replace(string(exampleYAML(t)), "max_connections: 96", "max_connections: 999999999999999999999999", 1),
		"reference":      strings.Replace(string(exampleYAML(t)), "endpoint: openai-prod", "endpoint: missing-endpoint", 1),
		"literal secret": strings.Replace(string(exampleYAML(t)), "password:\n      kind: file\n      path: /var/run/secrets/redis-password", "password: plaintext-secret", 1),
	}
	for name, data := range cases {
		if _, err := config.Load([]byte(data)); err == nil {
			t.Errorf("accepted invalid %s", name)
		}
	}
}
