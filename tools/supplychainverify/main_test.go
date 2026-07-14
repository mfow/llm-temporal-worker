package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVerifyAcceptsReviewedInventoryAndApprovedFinding(t *testing.T) {
	t.Parallel()

	baseline := testBaseline()
	baseline.VulnerabilityExceptions = []vulnerabilityException{{
		ID:          "GO-2099-0001",
		Owner:       "security@example.test",
		Expires:     "2099-01-01T00:00:00Z",
		Remediation: "https://example.test/security/GO-2099-0001",
		Scope:       "module_only",
	}}

	report, err := verify(
		baseline,
		testRequirements(),
		strings.NewReader(`{"finding":{"osv":"GO-2099-0001","trace":[{"module":"example.test/module","version":"v1.2.3"}]}}`),
		time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "pass" {
		t.Fatalf("report status = %q, want pass", report.Status)
	}
	if report.DirectModuleCount != 2 {
		t.Fatalf("direct module count = %d, want 2", report.DirectModuleCount)
	}
	if !reflect.DeepEqual(report.ApprovedFindings, []string{"GO-2099-0001"}) {
		t.Fatalf("approved findings = %#v", report.ApprovedFindings)
	}
}

func TestVerifyRejectsUnreviewedAndExpiredVulnerabilityFindings(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		baseline baseline
		want     string
	}{
		{
			name:     "unreviewed finding",
			baseline: testBaseline(),
			want:     "GO-2099-0001",
		},
		{
			name: "expired exception",
			baseline: func() baseline {
				value := testBaseline()
				value.VulnerabilityExceptions = []vulnerabilityException{{
					ID:          "GO-2099-0001",
					Owner:       "security@example.test",
					Expires:     "2026-07-13T00:00:00Z",
					Remediation: "https://example.test/security/GO-2099-0001",
					Scope:       "module_only",
				}}
				return value
			}(),
			want: "expired",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := verify(test.baseline, testRequirements(), strings.NewReader(`{"finding":{"osv":"GO-2099-0001"}}`), now)
			if err == nil {
				t.Fatal("verify accepted an unreviewed or expired finding")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verify error %q does not contain %q", err, test.want)
			}
		})
	}
}

func TestVerifyRejectsInventoryDriftAndIncompleteExceptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		baseline     baseline
		requirements []moduleRequirement
		want         string
	}{
		{
			name:         "unreviewed direct module",
			baseline:     testBaseline(),
			requirements: append(testRequirements(), moduleRequirement{Path: "example.test/new", Version: "v1.2.3"}),
			want:         "example.test/new",
		},
		{
			name: "missing exception owner",
			baseline: func() baseline {
				value := testBaseline()
				value.VulnerabilityExceptions = []vulnerabilityException{{
					ID:          "GO-2099-0001",
					Expires:     "2099-01-01T00:00:00Z",
					Remediation: "https://example.test/security/GO-2099-0001",
					Scope:       "module_only",
				}}
				return value
			}(),
			requirements: testRequirements(),
			want:         "owner",
		},
		{
			name: "missing exception scope",
			baseline: func() baseline {
				value := testBaseline()
				value.VulnerabilityExceptions = []vulnerabilityException{{
					ID:          "GO-2099-0001",
					Owner:       "security@example.test",
					Expires:     "2099-01-01T00:00:00Z",
					Remediation: "https://example.test/security/GO-2099-0001",
				}}
				return value
			}(),
			requirements: testRequirements(),
			want:         "scope",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := verify(test.baseline, test.requirements, strings.NewReader(""), time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC))
			if err == nil {
				t.Fatal("verify accepted an invalid inventory or vulnerability exception")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("verify error %q does not contain %q", err, test.want)
			}
		})
	}
}

func TestReportDoesNotRetainRawScannerTrace(t *testing.T) {
	t.Parallel()

	baseline := testBaseline()
	baseline.VulnerabilityExceptions = []vulnerabilityException{{
		ID:          "GO-2099-0001",
		Owner:       "security@example.test",
		Expires:     "2099-01-01T00:00:00Z",
		Remediation: "https://example.test/security/GO-2099-0001",
		Scope:       "reachable",
	}}
	report, err := verify(
		baseline,
		testRequirements(),
		strings.NewReader(`{"finding":{"osv":"GO-2099-0001","trace":[{"frame":{"position":{"filename":"raw-provider-response.txt"}}}]}}`),
		time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := writeReport(&output, report); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"raw-provider-response.txt", "trace", "filename"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("redacted report contains scanner detail %q: %s", forbidden, output.String())
		}
	}
}

func TestReadGoModExcludesIndirectRequirements(t *testing.T) {
	t.Parallel()

	requirements, err := readGoMod(strings.NewReader(`{"Require":[{"Path":"example.test/direct","Version":"v1.2.3"},{"Path":"example.test/indirect","Version":"v1.2.3","Indirect":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(requirements, []moduleRequirement{{Path: "example.test/direct", Version: "v1.2.3"}}) {
		t.Fatalf("requirements = %#v", requirements)
	}
}

func TestReadGoModRejectsReplacements(t *testing.T) {
	t.Parallel()

	_, err := readGoMod(strings.NewReader(`{
		"Require":[{"Path":"example.test/direct","Version":"v1.2.3"}],
		"Replace":[{
			"Old":{"Path":"example.test/direct","Version":"v1.2.3"},
			"New":{"Path":"./unreviewed-local-fork"}
		}]
	}`))
	if err == nil {
		t.Fatal("readGoMod accepted a replacement")
	}
	if !strings.Contains(err.Error(), "replacement") {
		t.Fatalf("readGoMod error %q does not identify the replacement", err)
	}
}

func TestVerifyRejectsReachableTraceOutsideModuleOnlyException(t *testing.T) {
	t.Parallel()

	baseline := testBaseline()
	baseline.VulnerabilityExceptions = []vulnerabilityException{{
		ID:          "GO-2099-0001",
		Owner:       "security@example.test",
		Expires:     "2099-01-01T00:00:00Z",
		Remediation: "https://example.test/security/GO-2099-0001",
		Scope:       "module_only",
	}}

	_, err := verify(
		baseline,
		testRequirements(),
		strings.NewReader(`{"finding":{"osv":"GO-2099-0001","trace":[{"module":"example.test/module","version":"v1.2.3"}]}}
{"finding":{"osv":"GO-2099-0001","trace":[{"module":"example.test/module","version":"v1.2.3"},{"package":"example.test/module/vulnerable","function":"Parse"}]}}`),
		time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC),
	)
	if err == nil {
		t.Fatal("verify accepted a reachable trace outside the module-only exception scope")
	}
	if !strings.Contains(err.Error(), "reachable trace") {
		t.Fatalf("verify error %q does not identify the reachable trace", err)
	}
}

func TestMakefileAndWorkflowsComposeBoundedSecurityVerification(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"GOTOOLCHAIN=$(SECURITY_GO_TOOLCHAIN)",
		"golang.org/x/vuln/cmd/govulncheck@v1.6.0",
		"govulncheck.stderr",
		"$(GO) mod edit -json",
		"$(GO) run ./tools/supplychainverify",
		"SECURITY_REPORT",
	} {
		if !strings.Contains(string(makefile), expected) {
			t.Fatalf("Makefile does not contain %q", expected)
		}
	}

	pullRequest, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "pull-request.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pullRequest), "make security-verify") {
		t.Fatal("PR workflow does not run security-verify")
	}

	master, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"id: security_verify",
		"SECURITY_REPORT=security-artifacts/security-verify.json make security-verify",
		"actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2",
		"steps.security_verify.outcome == 'failure'",
	} {
		if !strings.Contains(string(master), expected) {
			t.Fatalf("master workflow does not contain %q", expected)
		}
	}
}

func testBaseline() baseline {
	return baseline{
		Version:         1,
		AllowedLicenses: []string{"Apache-2.0", "MIT"},
		DirectModules: []moduleRecord{
			{Path: "example.test/one", Version: "v1.2.3", License: "MIT", Source: "https://example.test/one"},
			{Path: "example.test/two", Version: "v2.3.4", License: "Apache-2.0", Source: "https://example.test/two"},
		},
	}
}

func testRequirements() []moduleRequirement {
	return []moduleRequirement{
		{Path: "example.test/one", Version: "v1.2.3"},
		{Path: "example.test/two", Version: "v2.3.4"},
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
