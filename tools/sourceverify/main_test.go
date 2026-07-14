package main

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanContentDetectsRawAndDecodedCredentialFields(t *testing.T) {
	t.Parallel()

	secret := "Bearer " + strings.Repeat("x", 24)
	raw := `{"authorization":"` + secret + `"}`
	tests := []struct {
		name    string
		content string
	}{
		{name: "raw", content: raw},
		{name: "escaped", content: strings.ReplaceAll(raw, `"`, `\\"`)},
		{name: "url encoded", content: url.QueryEscape(raw)},
		{name: "base64 encoded", content: base64.StdEncoding.EncodeToString([]byte(raw))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			finding, err := scanContent([]byte(test.content))
			if err != nil {
				t.Fatal(err)
			}
			if finding == nil {
				t.Fatal("scanContent accepted an unredacted credential field")
			}
		})
	}
}

func TestScanContentAllowsExplicitRedactions(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		`{"authorization":"Bearer redacted"}`,
		`{"api_key":"local-only"}`,
	} {
		finding, err := scanContent([]byte(content))
		if err != nil {
			t.Fatal(err)
		}
		if finding != nil {
			t.Fatalf("scanContent rejected explicit redaction: %#v", finding)
		}
	}
}

func TestScanTestOutputDetectsRawAndDecodedDenyFieldLeaks(t *testing.T) {
	t.Parallel()

	raw := "raw provider body leaked: " + strings.Repeat("opaque-", 4)
	for _, output := range []string{
		raw,
		url.QueryEscape(raw),
		base64.StdEncoding.EncodeToString([]byte(raw)),
	} {
		finding, err := scanTestOutput([]byte(output))
		if err != nil {
			t.Fatal(err)
		}
		if finding == nil {
			t.Fatal("scanTestOutput accepted a denied-field leak")
		}
	}
}

func TestVerifyScansSourceFixturesAndTestOutputWithoutLeakingPayload(t *testing.T) {
	t.Parallel()

	secret := "Bearer " + strings.Repeat("y", 24)
	raw := `{"authorization":"` + secret + `"}`
	tests := []struct {
		name         string
		fixturePath  string
		fixtureBytes []byte
		testOutput   []byte
		wantLocation string
	}{
		{
			name:         "source",
			fixturePath:  "internal/config.go",
			fixtureBytes: []byte(`const authorization = "` + secret + `"`),
			wantLocation: "internal/config.go",
		},
		{
			name:         "fixture",
			fixturePath:  "llm/testdata/request.fixture",
			fixtureBytes: []byte(base64.StdEncoding.EncodeToString([]byte(raw))),
			wantLocation: "llm/testdata/request.fixture",
		},
		{
			name:         "test output",
			fixturePath:  "internal/safe.go",
			fixtureBytes: []byte("package internal\n"),
			testOutput:   []byte(url.QueryEscape(raw)),
			wantLocation: "test output",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTestFile(t, root, test.fixturePath, test.fixtureBytes)
			outputPath := ""
			if len(test.testOutput) > 0 {
				outputPath = filepath.Join(root, "test-output.json")
				writeTestFile(t, root, "test-output.json", test.testOutput)
			}

			err := verify(root, outputPath)
			if err == nil {
				t.Fatal("verify accepted an unredacted credential field")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("verify leaked credential bytes: %v", err)
			}
			if !strings.Contains(err.Error(), test.wantLocation) {
				t.Fatalf("verify error %q does not identify %q", err, test.wantLocation)
			}
		})
	}
}

func TestVerifyAllowsCredentialVariableWiringInSource(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, root, "internal/factory.go", []byte(`package internal

func configure(password string) {
	_ = struct{ Password string }{Password: password}
}
`))
	if err := verify(root, ""); err != nil {
		t.Fatalf("verify rejected a credential variable reference: %v", err)
	}
}

func TestMakefileDefinesSelfContainedSecurityVerify(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(data)
	_, target, found := strings.Cut(makefile, "security-verify:")
	if !found {
		t.Fatal("Makefile does not define security-verify")
	}
	for _, expected := range []string{
		"$(GO) test -json ./...",
		"$(GO) run ./tools/sourceverify",
		"mktemp",
	} {
		if !strings.Contains(target, expected) {
			t.Fatalf("security-verify target does not contain %q", expected)
		}
	}
	for _, externalTool := range []string{"govulncheck", "gitleaks", "trivy", "curl", "go install"} {
		if strings.Contains(target, externalTool) {
			t.Fatalf("security-verify target invokes external tool %q", externalTool)
		}
	}
}

func writeTestFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
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
