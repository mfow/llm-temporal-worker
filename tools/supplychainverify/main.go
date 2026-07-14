// Command supplychainverify verifies the reviewed direct-module inventory and
// evaluates the redacted JSON stream produced by govulncheck. It deliberately
// retains identifiers and aggregate status only; raw scanner traces can expose
// repository paths and must never become CI artifacts.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const baselineVersion = 1

type baseline struct {
	Version                 int                      `json:"version"`
	AllowedLicenses         []string                 `json:"allowed_licenses"`
	DirectModules           []moduleRecord           `json:"direct_modules"`
	VulnerabilityExceptions []vulnerabilityException `json:"vulnerability_exceptions"`
}

type moduleRecord struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	License string `json:"license"`
	Source  string `json:"source"`
}

type vulnerabilityException struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Expires     string `json:"expires"`
	Remediation string `json:"remediation"`
}

type moduleRequirement struct {
	Path     string `json:"Path"`
	Version  string `json:"Version"`
	Indirect bool   `json:"Indirect"`
}

type goMod struct {
	Require []moduleRequirement `json:"Require"`
}

type componentStatus struct {
	Test          string `json:"test"`
	Source        string `json:"source"`
	GoMod         string `json:"go_mod"`
	Vulnerability string `json:"vulnerability"`
}

type verificationReport struct {
	Version           int             `json:"version"`
	Status            string          `json:"status"`
	Components        componentStatus `json:"components"`
	DirectModuleCount int             `json:"direct_module_count"`
	Findings          []string        `json:"findings"`
	ApprovedFindings  []string        `json:"approved_findings"`
	Error             string          `json:"error,omitempty"`
}

func main() {
	baselinePath := flag.String("baseline", "", "reviewed dependency and vulnerability baseline JSON")
	goModPath := flag.String("go-mod", "", "go mod edit -json output")
	vulnerabilityOutputPath := flag.String("vulnerability-output", "", "govulncheck JSON output")
	reportPath := flag.String("report", "", "redacted report output path")
	testStatus := flag.String("test-status", "pass", "captured Go test status: pass or fail")
	sourceStatus := flag.String("source-status", "pass", "source verification status: pass or fail")
	goModStatus := flag.String("go-mod-status", "pass", "go.mod inventory status: pass or fail")
	vulnerabilityStatus := flag.String("vulnerability-status", "pass", "govulncheck execution status: pass or fail")
	flag.Parse()

	if *baselinePath == "" || *goModPath == "" || *vulnerabilityOutputPath == "" || *reportPath == "" {
		fmt.Fprintln(os.Stderr, "supply chain verification requires -baseline, -go-mod, -vulnerability-output, and -report")
		os.Exit(2)
	}

	components := componentStatus{
		Test:          *testStatus,
		Source:        *sourceStatus,
		GoMod:         *goModStatus,
		Vulnerability: *vulnerabilityStatus,
	}
	report, err := execute(*baselinePath, *goModPath, *vulnerabilityOutputPath, components, time.Now().UTC())
	if writeErr := writeReportFile(*reportPath, report); writeErr != nil {
		fmt.Fprintln(os.Stderr, "supply chain verification cannot write redacted report")
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("supply chain verification passed")
}

func execute(baselinePath, goModPath, vulnerabilityOutputPath string, components componentStatus, now time.Time) (verificationReport, error) {
	report := verificationReport{
		Version:          baselineVersion,
		Status:           "fail",
		Components:       components,
		Findings:         []string{},
		ApprovedFindings: []string{},
	}

	baselineFile, err := os.Open(baselinePath)
	if err != nil {
		return reportWithError(report, errors.New("supply chain verification cannot read baseline"))
	}
	defer baselineFile.Close()
	var value baseline
	if err := json.NewDecoder(baselineFile).Decode(&value); err != nil {
		return reportWithError(report, errors.New("supply chain verification baseline is not valid JSON"))
	}

	goModFile, err := os.Open(goModPath)
	if err != nil {
		return reportWithError(report, errors.New("supply chain verification cannot read go.mod inventory"))
	}
	defer goModFile.Close()
	requirements, err := readGoMod(goModFile)
	if err != nil {
		return reportWithError(report, err)
	}

	vulnerabilityFile, err := os.Open(vulnerabilityOutputPath)
	if err != nil {
		return reportWithError(report, errors.New("supply chain verification cannot read vulnerability output"))
	}
	defer vulnerabilityFile.Close()
	report, err = verify(value, requirements, vulnerabilityFile, now)
	report.Components = components
	if err != nil {
		return reportWithError(report, err)
	}
	if err := validateComponentStatus(components); err != nil {
		return reportWithError(report, err)
	}
	report.Status = "pass"
	return report, nil
}

func reportWithError(report verificationReport, err error) (verificationReport, error) {
	report.Status = "fail"
	report.Error = err.Error()
	return report, err
}

func readGoMod(reader io.Reader) ([]moduleRequirement, error) {
	var decoded goMod
	if err := json.NewDecoder(reader).Decode(&decoded); err != nil {
		return nil, errors.New("supply chain verification go.mod inventory is not valid JSON")
	}
	requirements := make([]moduleRequirement, 0, len(decoded.Require))
	for _, requirement := range decoded.Require {
		if requirement.Indirect {
			continue
		}
		if strings.TrimSpace(requirement.Path) == "" || strings.TrimSpace(requirement.Version) == "" {
			return nil, errors.New("supply chain verification go.mod inventory has an invalid direct module")
		}
		requirements = append(requirements, moduleRequirement{Path: requirement.Path, Version: requirement.Version})
	}
	return requirements, nil
}

func verify(value baseline, requirements []moduleRequirement, vulnerabilityOutput io.Reader, now time.Time) (verificationReport, error) {
	report := verificationReport{
		Version:           baselineVersion,
		Status:            "fail",
		DirectModuleCount: len(requirements),
		Findings:          []string{},
		ApprovedFindings:  []string{},
	}
	exceptions, err := validateBaseline(value, now)
	if err != nil {
		return report, err
	}
	if err := validateInventory(value.DirectModules, requirements); err != nil {
		return report, err
	}
	findings, err := readFindings(vulnerabilityOutput)
	if err != nil {
		return report, err
	}
	report.Findings = findings
	approved := make([]string, 0, len(findings))
	usedExceptions := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		if _, ok := exceptions[finding]; !ok {
			return report, fmt.Errorf("supply chain verification found unreviewed vulnerability %s", finding)
		}
		usedExceptions[finding] = struct{}{}
		approved = append(approved, finding)
	}
	for id := range exceptions {
		if _, used := usedExceptions[id]; !used {
			return report, fmt.Errorf("supply chain verification vulnerability exception %s is unused", id)
		}
	}
	sort.Strings(approved)
	report.ApprovedFindings = approved
	report.Status = "pass"
	return report, nil
}

func validateBaseline(value baseline, now time.Time) (map[string]vulnerabilityException, error) {
	if value.Version != baselineVersion {
		return nil, fmt.Errorf("supply chain verification baseline version must be %d", baselineVersion)
	}
	allowedLicenses := make(map[string]struct{}, len(value.AllowedLicenses))
	for _, license := range value.AllowedLicenses {
		license = strings.TrimSpace(license)
		if license == "" {
			return nil, errors.New("supply chain verification baseline has an empty allowed license")
		}
		if _, duplicate := allowedLicenses[license]; duplicate {
			return nil, fmt.Errorf("supply chain verification baseline repeats allowed license %s", license)
		}
		allowedLicenses[license] = struct{}{}
	}
	if len(allowedLicenses) == 0 {
		return nil, errors.New("supply chain verification baseline has no allowed licenses")
	}

	modulePaths := make(map[string]struct{}, len(value.DirectModules))
	for _, module := range value.DirectModules {
		if strings.TrimSpace(module.Path) == "" || strings.TrimSpace(module.Version) == "" {
			return nil, errors.New("supply chain verification baseline has an incomplete direct module")
		}
		if _, duplicate := modulePaths[module.Path]; duplicate {
			return nil, fmt.Errorf("supply chain verification baseline repeats direct module %s", module.Path)
		}
		modulePaths[module.Path] = struct{}{}
		if _, allowed := allowedLicenses[module.License]; !allowed {
			return nil, fmt.Errorf("supply chain verification baseline direct module %s has an unapproved license", module.Path)
		}
		if err := validateHTTPSReference(module.Source); err != nil {
			return nil, fmt.Errorf("supply chain verification baseline direct module %s has an invalid source", module.Path)
		}
	}

	exceptions := make(map[string]vulnerabilityException, len(value.VulnerabilityExceptions))
	for _, exception := range value.VulnerabilityExceptions {
		if !strings.HasPrefix(exception.ID, "GO-") || strings.TrimSpace(exception.Owner) == "" || strings.TrimSpace(exception.Remediation) == "" {
			return nil, errors.New("supply chain verification vulnerability exception requires an id, owner, expiry, and remediation")
		}
		if _, duplicate := exceptions[exception.ID]; duplicate {
			return nil, fmt.Errorf("supply chain verification baseline repeats vulnerability exception %s", exception.ID)
		}
		expires, err := time.Parse(time.RFC3339, exception.Expires)
		if err != nil {
			return nil, fmt.Errorf("supply chain verification vulnerability exception %s has an invalid expiry", exception.ID)
		}
		if !expires.After(now) {
			return nil, fmt.Errorf("supply chain verification vulnerability exception %s is expired", exception.ID)
		}
		if err := validateHTTPSReference(exception.Remediation); err != nil {
			return nil, fmt.Errorf("supply chain verification vulnerability exception %s has an invalid remediation", exception.ID)
		}
		exceptions[exception.ID] = exception
	}
	return exceptions, nil
}

func validateHTTPSReference(raw string) error {
	value, err := url.Parse(raw)
	if err != nil || value.Scheme != "https" || value.Host == "" || value.User != nil {
		return errors.New("invalid HTTPS reference")
	}
	return nil
}

func validateInventory(records []moduleRecord, requirements []moduleRequirement) error {
	baselineByPath := make(map[string]moduleRecord, len(records))
	for _, record := range records {
		baselineByPath[record.Path] = record
	}
	actualByPath := make(map[string]moduleRequirement, len(requirements))
	for _, requirement := range requirements {
		if _, duplicate := actualByPath[requirement.Path]; duplicate {
			return fmt.Errorf("supply chain verification go.mod inventory repeats direct module %s", requirement.Path)
		}
		actualByPath[requirement.Path] = requirement
		record, known := baselineByPath[requirement.Path]
		if !known {
			return fmt.Errorf("supply chain verification direct module %s is missing from the reviewed baseline", requirement.Path)
		}
		if record.Version != requirement.Version {
			return fmt.Errorf("supply chain verification direct module %s version differs from the reviewed baseline", requirement.Path)
		}
	}
	for _, record := range records {
		if _, present := actualByPath[record.Path]; !present {
			return fmt.Errorf("supply chain verification baseline direct module %s is not in go.mod", record.Path)
		}
	}
	return nil
}

func readFindings(reader io.Reader) ([]string, error) {
	decoder := json.NewDecoder(reader)
	found := make(map[string]struct{})
	for {
		var event struct {
			Finding *struct {
				OSV string `json:"osv"`
			} `json:"finding"`
		}
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("supply chain verification vulnerability output is not valid JSON")
		}
		if event.Finding == nil {
			continue
		}
		id := strings.TrimSpace(event.Finding.OSV)
		if !strings.HasPrefix(id, "GO-") {
			return nil, errors.New("supply chain verification vulnerability output has an invalid finding identifier")
		}
		found[id] = struct{}{}
	}
	findings := make([]string, 0, len(found))
	for id := range found {
		findings = append(findings, id)
	}
	sort.Strings(findings)
	return findings, nil
}

func validateComponentStatus(status componentStatus) error {
	for _, component := range []struct {
		name  string
		value string
	}{
		{name: "test", value: status.Test},
		{name: "source", value: status.Source},
		{name: "go.mod", value: status.GoMod},
		{name: "vulnerability", value: status.Vulnerability},
	} {
		if component.value != "pass" && component.value != "fail" {
			return fmt.Errorf("supply chain verification component %s has an invalid status", component.name)
		}
		if component.value != "pass" {
			return fmt.Errorf("supply chain verification component %s did not pass", component.name)
		}
	}
	return nil
}

func writeReportFile(path string, report verificationReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return writeReport(file, report)
}

func writeReport(writer io.Writer, report verificationReport) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
