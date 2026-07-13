package architecturetest

import (
	"os"
	"path/filepath"
	"testing"

	yaml "go.yaml.in/yaml/v4"
)

func TestWorkflowYAMLParses(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range []string{"master.yml", "pull-request.yml"} {
		data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := yaml.Load(data, &document, yaml.WithUniqueKeys(), yaml.WithSingleDocument()); err != nil {
			t.Fatalf("workflow %s is not valid YAML: %v", name, err)
		}
	}
}
