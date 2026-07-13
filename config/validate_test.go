package config_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/llm/schema"
)

func TestConfigExampleMatchesJSONSchema(t *testing.T) {
	configData := exampleYAML(t)
	loaded, err := config.Load(configData)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	schemaData, err := os.ReadFile("../api/schema/v1/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := schema.Parse(schemaData)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(encoded); err != nil {
		t.Fatal(err)
	}
}

func TestConfigSchemaRejectsFourthServiceClass(t *testing.T) {
	schemaData, err := os.ReadFile("../api/schema/v1/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := schema.Parse(schemaData)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(exampleYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	loaded.Endpoints["openai-prod"].ServiceClasses["turbo"] = loaded.Endpoints["openai-prod"].ServiceClasses["standard"]
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(encoded); err == nil {
		t.Fatal("schema accepted a fourth public service class")
	}
}
