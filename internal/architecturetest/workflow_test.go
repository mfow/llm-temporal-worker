package architecturetest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v4"
)

const (
	checkoutActionPin = "actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10"
	setupGoActionPin  = "actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16"
)

var immutableActionReference = regexp.MustCompile(`^[0-9a-f]{40}$`)

type workflowDocument struct {
	name   string
	raw    string
	fields map[string]any
}

func TestWorkflowYAMLParses(t *testing.T) {
	for _, name := range []string{"master.yml", "pull-request.yml"} {
		_ = readWorkflow(t, name)
	}
}

func TestWorkflowContract(t *testing.T) {
	pullRequest := readWorkflow(t, "pull-request.yml")
	master := readWorkflow(t, "master.yml")

	assertPullRequestTrigger(t, pullRequest)
	assertReadOnlyPermissions(t, pullRequest)
	assertMasterTriggers(t, master)
	for _, workflow := range []workflowDocument{pullRequest, master} {
		assertWorkflowControls(t, workflow)
		assertVerificationStep(t, workflow)
		assertRequiredOfflineGates(t, workflow)
	}
}

func TestWorkflowPolicyDoesNotReferenceProviderCredentialsOrDeployment(t *testing.T) {
	for _, workflow := range []workflowDocument{
		readWorkflow(t, "pull-request.yml"),
		readWorkflow(t, "master.yml"),
	} {
		lower := strings.ToLower(workflow.raw)
		for _, forbidden := range []string{
			"secrets.",
			"openai_api_key",
			"anthropic_api_key",
			"azure_openai",
			"aws_access_key_id",
			"aws_secret_access_key",
			"llmtw_live_",
			"kubectl apply",
			"helm upgrade",
			"docker push",
			"cosign sign",
			"gh release",
			"make release",
		} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%s contains forbidden credential or deployment reference %q", workflow.name, forbidden)
			}
		}
	}
}

func TestWorkflowActionsUseImmutablePinsWithVersionComments(t *testing.T) {
	for _, workflow := range []workflowDocument{
		readWorkflow(t, "pull-request.yml"),
		readWorkflow(t, "master.yml"),
	} {
		for _, reference := range actionReferences(t, workflow) {
			if strings.HasPrefix(reference, "./") {
				continue
			}
			parts := strings.SplitN(reference, "@", 2)
			if len(parts) != 2 || !immutableActionReference.MatchString(parts[1]) {
				t.Fatalf("%s action %q is not pinned to an immutable commit", workflow.name, reference)
			}
		}
		for _, want := range []string{checkoutActionPin, setupGoActionPin} {
			if !strings.Contains(workflow.raw, "uses: "+want+" # v6") {
				t.Fatalf("%s does not record readable v6 comment beside immutable action pin %q", workflow.name, want)
			}
		}
	}
}

func TestWorkflowVerificationEntrypoint(t *testing.T) {
	root := repositoryRoot(t)
	makefile := readRepositoryFile(t, root, "Makefile")
	if !strings.Contains(makefile, "workflow-verify:") || !strings.Contains(makefile, "scripts/check-workflow-policy.sh") {
		t.Fatal("Makefile does not expose workflow-verify through scripts/check-workflow-policy.sh")
	}

	script := readRepositoryFile(t, root, "scripts", "check-workflow-policy.sh")
	if !strings.Contains(script, "github.com/rhysd/actionlint/cmd/actionlint@v1.7.12") {
		t.Fatal("workflow policy helper does not pin actionlint v1.7.12")
	}
	if !strings.Contains(script, "go test ./internal/architecturetest -run '^TestWorkflow'") {
		t.Fatal("workflow policy helper does not execute the workflow contract tests")
	}
}

func TestWorkflowsRunHardenedImageVerification(t *testing.T) {
	for _, name := range []string{"master.yml", "pull-request.yml"} {
		workflow := readWorkflow(t, name)
		if !hasRunCommand(workflow, "make image-verify") {
			t.Fatalf("%s does not run make image-verify", workflow.name)
		}
	}
}

func assertPullRequestTrigger(t *testing.T, workflow workflowDocument) {
	t.Helper()
	triggers := workflowMapping(t, workflow, "on")
	pullRequest := nestedMapping(t, workflow.name, triggers, "pull_request")
	branches := stringSequence(t, workflow.name, pullRequest, "branches")
	if len(branches) != 1 || branches[0] != "master" {
		t.Fatalf("%s pull request branches = %v, want [master]", workflow.name, branches)
	}
}

func assertReadOnlyPermissions(t *testing.T, workflow workflowDocument) {
	t.Helper()
	permissions := workflowMapping(t, workflow, "permissions")
	if len(permissions) != 1 || scalarString(t, workflow.name, permissions, "contents") != "read" {
		t.Fatalf("%s permissions = %#v, want only contents: read", workflow.name, permissions)
	}
}

func assertMasterTriggers(t *testing.T, workflow workflowDocument) {
	t.Helper()
	triggers := workflowMapping(t, workflow, "on")
	push := nestedMapping(t, workflow.name, triggers, "push")
	branches := stringSequence(t, workflow.name, push, "branches")
	if len(branches) != 1 || branches[0] != "master" {
		t.Fatalf("%s push branches = %v, want [master]", workflow.name, branches)
	}
	if _, ok := triggers["workflow_dispatch"]; !ok {
		t.Fatalf("%s does not support workflow_dispatch", workflow.name)
	}

	schedules, ok := triggers["schedule"].([]any)
	if !ok || len(schedules) != 1 {
		t.Fatalf("%s schedule = %#v, want one daily schedule", workflow.name, triggers["schedule"])
	}
	schedule, ok := schedules[0].(map[string]any)
	if !ok {
		t.Fatalf("%s schedule entry = %#v, want mapping", workflow.name, schedules[0])
	}
	if scalarString(t, workflow.name, schedule, "cron") != "0 5 * * *" {
		t.Fatalf("%s cron = %q, want exact 05:00 daily schedule", workflow.name, scalarString(t, workflow.name, schedule, "cron"))
	}
	if scalarString(t, workflow.name, schedule, "timezone") != "Australia/Sydney" {
		t.Fatalf("%s timezone = %q, want Australia/Sydney", workflow.name, scalarString(t, workflow.name, schedule, "timezone"))
	}
}

func assertWorkflowControls(t *testing.T, workflow workflowDocument) {
	t.Helper()
	if _, ok := workflow.fields["concurrency"].(map[string]any); !ok {
		t.Fatalf("%s does not declare workflow concurrency", workflow.name)
	}
	verify := nestedMapping(t, workflow.name, workflowMapping(t, workflow, "jobs"), "verify")
	if _, ok := verify["timeout-minutes"]; !ok {
		t.Fatalf("%s verify job does not declare a timeout", workflow.name)
	}
}

func assertVerificationStep(t *testing.T, workflow workflowDocument) {
	t.Helper()
	if !hasRunCommand(workflow, "make workflow-verify") {
		t.Fatalf("%s does not run make workflow-verify", workflow.name)
	}
}

func assertRequiredOfflineGates(t *testing.T, workflow workflowDocument) {
	t.Helper()
	for _, command := range []string{
		"scripts/check-go-format.sh",
		"make schema-verify",
		"make docs-verify",
		"go vet ./...",
		"go test -race ./...",
		"go build ./...",
	} {
		if !strings.Contains(workflow.raw, command) {
			t.Fatalf("%s does not retain required offline gate %q", workflow.name, command)
		}
	}
}

func readWorkflow(t *testing.T, name string) workflowDocument {
	t.Helper()
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err := yaml.Load(data, &fields, yaml.WithUniqueKeys(), yaml.WithSingleDocument()); err != nil {
		t.Fatalf("workflow %s is not valid YAML: %v", name, err)
	}
	return workflowDocument{name: name, raw: string(data), fields: fields}
}

func readRepositoryFile(t *testing.T, root string, path ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{root}, path...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func workflowMapping(t *testing.T, workflow workflowDocument, key string) map[string]any {
	t.Helper()
	return nestedMapping(t, workflow.name, workflow.fields, key)
}

func nestedMapping(t *testing.T, name string, fields map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := fields[key]
	if !ok {
		t.Fatalf("%s is missing %q", name, key)
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s %q = %#v, want mapping", name, key, value)
	}
	return mapping
}

func stringSequence(t *testing.T, name string, fields map[string]any, key string) []string {
	t.Helper()
	value, ok := fields[key]
	if !ok {
		t.Fatalf("%s is missing %q", name, key)
	}
	sequence, ok := value.([]any)
	if !ok {
		t.Fatalf("%s %q = %#v, want sequence", name, key, value)
	}
	result := make([]string, len(sequence))
	for index, item := range sequence {
		stringValue, ok := item.(string)
		if !ok {
			t.Fatalf("%s %q item %d = %#v, want string", name, key, index, item)
		}
		result[index] = stringValue
	}
	return result
}

func scalarString(t *testing.T, name string, fields map[string]any, key string) string {
	t.Helper()
	value, ok := fields[key]
	if !ok {
		t.Fatalf("%s is missing %q", name, key)
	}
	stringValue, ok := value.(string)
	if !ok {
		t.Fatalf("%s %q = %#v, want string", name, key, value)
	}
	return stringValue
}

func actionReferences(t *testing.T, workflow workflowDocument) []string {
	t.Helper()
	jobs := workflowMapping(t, workflow, "jobs")
	var references []string
	for name, rawJob := range jobs {
		job, ok := rawJob.(map[string]any)
		if !ok {
			t.Fatalf("%s job %q = %#v, want mapping", workflow.name, name, rawJob)
		}
		steps, ok := job["steps"].([]any)
		if !ok {
			t.Fatalf("%s job %q steps = %#v, want sequence", workflow.name, name, job["steps"])
		}
		for index, rawStep := range steps {
			step, ok := rawStep.(map[string]any)
			if !ok {
				t.Fatalf("%s job %q step %d = %#v, want mapping", workflow.name, name, index, rawStep)
			}
			if reference, ok := step["uses"].(string); ok {
				references = append(references, reference)
			}
		}
	}
	return references
}

func hasRunCommand(workflow workflowDocument, command string) bool {
	jobs, ok := workflow.fields["jobs"].(map[string]any)
	if !ok {
		return false
	}
	for _, rawJob := range jobs {
		job, ok := rawJob.(map[string]any)
		if !ok {
			continue
		}
		steps, ok := job["steps"].([]any)
		if !ok {
			continue
		}
		for _, rawStep := range steps {
			step, ok := rawStep.(map[string]any)
			if !ok {
				continue
			}
			run, ok := step["run"].(string)
			if !ok {
				continue
			}
			for _, line := range strings.Split(run, "\n") {
				if strings.TrimSpace(line) == command {
					return true
				}
			}
		}
	}
	return false
}
