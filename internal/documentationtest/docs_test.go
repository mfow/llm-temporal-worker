package documentationtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var markdownLink = regexp.MustCompile(`(?m)(?:^|[^!])\[[^\]]+\]\(([^)]+)\)`)

func TestDocumentationLinksAndInvariants(t *testing.T) {
	root := repositoryRoot(t)
	required := []string{
		"README.md",
		"docs/index.md",
		"docs/superpowers/plans/2026-07-13-master-sequence.md",
	}
	for _, relative := range required {
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			t.Fatalf("required documentation %s: %v", relative, err)
		}
	}
	var files []string
	files = append(files, filepath.Join(root, "README.md"))
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "TBD") || strings.Contains(text, "TODO") || strings.Contains(text, "FIXME") || strings.Contains(text, "PLACEHOLDER") {
			t.Fatalf("planning placeholder found in %s", path)
		}
		for _, match := range markdownLink.FindAllStringSubmatch(text, -1) {
			raw := strings.TrimSpace(strings.Fields(match[1])[0])
			raw = strings.Trim(raw, "<>")
			if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "mailto:") {
				continue
			}
			target := strings.SplitN(raw, "#", 2)[0]
			resolved, err := filepath.Abs(filepath.Join(filepath.Dir(path), target))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(resolved); err != nil {
				t.Fatalf("%s links to missing target %s: %s", path, raw, err)
			}
		}
	}
	decision, err := os.ReadFile(filepath.Join(root, "docs/decisions/0002-service-classes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(decision), "economy | standard | priority") != 1 {
		t.Fatal("service-class decision must state the exact public enum once")
	}
}

func TestLiveProviderDocumentationSeparatesManualWorkflowFromRelease(t *testing.T) {
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs/testing/strategy.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(data)), " ")
	if !strings.Contains(text, "protected manual live-provider workflow") {
		t.Fatal("live-provider documentation must name the protected manual live-provider workflow")
	}
	if !strings.Contains(text, "redacted live-provider evidence") {
		t.Fatal("live-provider documentation must distinguish its redacted evidence")
	}
	if strings.Contains(text, "protected manual release workflow") {
		t.Fatal("live-provider workflow must not be documented as a release workflow")
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
