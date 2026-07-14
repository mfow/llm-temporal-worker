package qualitytest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var fuzzTarget = regexp.MustCompile(`(?m)^func (Fuzz[[:alnum:]_]+)\(f \*testing\.F\)`)

func TestFuzzGateCoversEveryRepositoryFuzzTarget(t *testing.T) {
	root := repositoryRoot(t)
	script := readFile(t, filepath.Join(root, "scripts", "run-fuzz.sh"))

	for _, target := range repositoryFuzzTargets(t, root) {
		entry := target.packagePath + " " + target.name
		if !strings.Contains(script, entry) {
			t.Fatalf("fuzz gate does not include %s", entry)
		}
	}
	if !strings.Contains(script, "reproduce a saved corpus input") {
		t.Fatal("fuzz gate does not document a saved-corpus reproduction command")
	}
}

func TestRepositoryFuzzTargetsIncludesOrdinaryTestFiles(t *testing.T) {
	root := repositoryRoot(t)
	for _, target := range repositoryFuzzTargets(t, root) {
		if target.packagePath == "./state" && target.name == "FuzzVerifyHandleNeverPanics" {
			return
		}
	}
	t.Fatal("repository fuzz target scan omitted FuzzVerifyHandleNeverPanics in state/handle_test.go")
}

func TestQualityGatesAreWiredIntoMakeCIAndTestingStrategy(t *testing.T) {
	root := repositoryRoot(t)
	makefile := readFile(t, filepath.Join(root, "Makefile"))
	for _, target := range []string{"fuzz-smoke:", "mutation-verify:", "scripts/run-fuzz.sh smoke", "scripts/run-mutation.sh"} {
		if !strings.Contains(makefile, target) {
			t.Fatalf("Makefile does not include %q", target)
		}
	}

	pullRequest := readFile(t, filepath.Join(root, ".github", "workflows", "pull-request.yml"))
	for _, command := range []string{"make fuzz-smoke", "make mutation-verify"} {
		if !strings.Contains(pullRequest, command) {
			t.Fatalf("pull-request workflow does not run %q", command)
		}
	}

	master := readFile(t, filepath.Join(root, ".github", "workflows", "master.yml"))
	for _, value := range []string{"fuzz-shard", "matrix:", "shard: [0, 1, 2]", "scripts/run-fuzz.sh shard"} {
		if !strings.Contains(master, value) {
			t.Fatalf("master workflow does not configure %q", value)
		}
	}

	strategy := readFile(t, filepath.Join(root, "docs", "testing", "strategy.md"))
	for _, command := range []string{"make fuzz-smoke", "make mutation-verify"} {
		if !strings.Contains(strategy, command) {
			t.Fatalf("testing strategy does not document %q", command)
		}
	}
}

func TestMutationManifestCoversCriticalSemanticInvariants(t *testing.T) {
	root := repositoryRoot(t)
	manifest := readFile(t, filepath.Join(root, "tools", "mutationverify", "main.go"))
	for _, name := range []string{
		"money-round-up",
		"window-comparison-boundary",
		"dispatch-certainty",
		"service-class-default",
		"state-transition",
	} {
		if !strings.Contains(manifest, name) {
			t.Fatalf("mutation manifest does not include %q", name)
		}
	}
}

type fuzzEntry struct {
	packagePath string
	name        string
}

func repositoryFuzzTargets(t *testing.T, root string) []fuzzEntry {
	t.Helper()
	var targets []fuzzEntry
	for _, directory := range []string{"llm", "pricing", "budget", "state", "storage/redis", "routing"} {
		base := filepath.Join(root, directory)
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			for _, match := range fuzzTarget.FindAllStringSubmatch(string(data), -1) {
				targets = append(targets, fuzzEntry{packagePath: "./" + filepath.ToSlash(relative), name: match[1]})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].packagePath != targets[j].packagePath {
			return targets[i].packagePath < targets[j].packagePath
		}
		return targets[i].name < targets[j].name
	})
	return targets
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root with go.mod not found")
		}
		directory = parent
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
