package qualitytest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var fuzzTargetName = regexp.MustCompile(`^Fuzz[[:alnum:]_]+$`)

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

func TestRepositoryFuzzTargetsIncludesOutsideLegacyAllowlist(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"llm", "pricing", "budget", "state", "storage/redis", "routing", "admission"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "admission", "engine_fuzz_test.go"), []byte(`package admission

import "testing"

func FuzzAdmissionEngine(f *testing.F) {}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, target := range repositoryFuzzTargets(t, root) {
		if target.packagePath == "./admission" && target.name == "FuzzAdmissionEngine" {
			return
		}
	}
	t.Fatal("repository fuzz target scan omitted FuzzAdmissionEngine outside the legacy allowlist")
}

func TestRepositoryFuzzTargetsIgnoresFuzzLookalikesOutsideDeclarations(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "lookalike_fuzz_test.go"), []byte(`package fixture

import "testing"

// func FuzzComment(f *testing.F) {}
const source = `+"`"+`
func FuzzString(f *testing.F) {}
`+"`"+`

func FuzzReal(f *testing.F) {}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	targets := repositoryFuzzTargets(t, root)
	if len(targets) != 1 || targets[0].packagePath != "." || targets[0].name != "FuzzReal" {
		t.Fatalf("fuzz targets = %#v, want only FuzzReal", targets)
	}
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
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && excludedFromFuzzTargetScan(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
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
		packagePath := "."
		if relative != "." {
			packagePath = "./" + filepath.ToSlash(relative)
		}
		names, err := fuzzTargetsInFile(path, data)
		if err != nil {
			return err
		}
		for _, name := range names {
			targets = append(targets, fuzzEntry{packagePath: packagePath, name: name})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].packagePath != targets[j].packagePath {
			return targets[i].packagePath < targets[j].packagePath
		}
		return targets[i].name < targets[j].name
	})
	return targets
}

func fuzzTargetsInFile(path string, data []byte) ([]string, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, data, 0)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || !isFuzzTarget(function) {
			continue
		}
		names = append(names, function.Name.Name)
	}
	return names, nil
}

func isFuzzTarget(function *ast.FuncDecl) bool {
	if function.Recv != nil || !fuzzTargetName.MatchString(function.Name.Name) || function.Type.Params == nil || len(function.Type.Params.List) != 1 {
		return false
	}
	if function.Type.Results != nil && len(function.Type.Results.List) > 0 {
		return false
	}
	parameter := function.Type.Params.List[0]
	if len(parameter.Names) > 1 {
		return false
	}
	pointer, ok := parameter.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && selector.Sel.Name == "F"
}

// excludedFromFuzzTargetScan deliberately mirrors directories that Go does not
// treat as module packages, so every actual repository fuzz target remains
// covered without interpreting fixtures or vendored code as runnable tests.
func excludedFromFuzzTargetScan(name string) bool {
	switch name {
	case ".git", ".github", ".agents", ".codex", "testdata", "vendor", "node_modules":
		return true
	default:
		return strings.HasPrefix(name, ".")
	}
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
