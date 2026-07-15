package architecturetest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLiveContractEvidenceRecorderWritesOnlyRedactedAllowlistedFields(t *testing.T) {
	root := repositoryRoot(t)
	directory := t.TempDir()
	rawPath := filepath.Join(directory, "live-contract.raw.log")
	evidencePath := filepath.Join(directory, "evidence.json")
	logPath := filepath.Join(directory, "live-contract.log")
	writeLiveContractRawLog(t, rawPath, "openai-responses", "req_123", "resp_456", true, 17, "provider_reported", true)

	output, err := runLiveContractEvidence(root,
		"record",
		"--profile", "openai-responses",
		"--input", rawPath,
		"--evidence", evidencePath,
		"--log", logPath,
		"--generated-at", "2026-07-15T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("record safe live contract evidence: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "live contract evidence recorded" {
		t.Fatalf("record output = %q, want fixed success message", got)
	}

	data, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence map[string]any
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatalf("decode redacted evidence: %v", err)
	}
	wantKeys := map[string]bool{
		"schema_version":        true,
		"generated_at":          true,
		"profile":               true,
		"tenant":                true,
		"ceiling_micro_usd":     true,
		"actual_spend":          true,
		"request_id":            true,
		"response_id":           true,
		"actual_service_class":  true,
		"continuation_verified": true,
	}
	if len(evidence) != len(wantKeys) {
		t.Fatalf("evidence keys = %#v, want only %#v", evidence, wantKeys)
	}
	for key := range wantKeys {
		if _, ok := evidence[key]; !ok {
			t.Fatalf("evidence does not contain %q: %#v", key, evidence)
		}
	}
	for _, forbidden := range []string{"prompt", "output", "endpoint", "credential", "api_key", "raw"} {
		if _, found := evidence[forbidden]; found {
			t.Fatalf("evidence retains forbidden %q field: %#v", forbidden, evidence)
		}
	}
	if got := evidence["profile"]; got != "openai-responses" {
		t.Fatalf("evidence profile = %#v, want openai-responses", got)
	}
	if got := evidence["ceiling_micro_usd"]; got != float64(25_000) {
		t.Fatalf("evidence ceiling_micro_usd = %#v, want 25000", got)
	}
	spend, ok := evidence["actual_spend"].(map[string]any)
	if !ok {
		t.Fatalf("evidence actual_spend = %#v, want object", evidence["actual_spend"])
	}
	if got := spend["micro_usd"]; got != float64(17) {
		t.Fatalf("evidence actual spend = %#v, want 17", got)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "live-contract-ok") || strings.Contains(string(log), "api.openai.com") {
		t.Fatalf("redacted log contains non-allowlisted content: %q", log)
	}

	output, err = runLiveContractEvidence(root, "verify", "--evidence", evidencePath, "--log", logPath)
	if err != nil {
		t.Fatalf("verify recorded live contract evidence: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "live contract evidence verified" {
		t.Fatalf("verify output = %q, want fixed success message", got)
	}
}

func TestLiveContractEvidenceRecorderRejectsUnsafeOrInconsistentInputWithoutEchoingIt(t *testing.T) {
	root := repositoryRoot(t)
	for _, test := range []struct {
		name       string
		profile    string
		logProfile string
		requestID  string
		known      bool
		microUSD   int
		method     string
		continued  bool
		secret     string
	}{
		{
			name:       "unsafe request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "sk-unsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "sk-unsafe-live-secret",
		},
		{
			name:       "mixed-case OpenAI-style request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "Sk-unsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "Sk-unsafe-live-secret",
		},
		{
			name:       "JWT-like request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "EYJunsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "EYJunsafe-live-secret",
		},
		{
			name:       "AWS access-key-like request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "AkIaunsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "AkIaunsafe-live-secret",
		},
		{
			name:       "AWS session-like request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "ASIAunsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "ASIAunsafe-live-secret",
		},
		{
			name:       "GitHub token-like request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "GHP_unsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "GHP_unsafe-live-secret",
		},
		{
			name:       "GitHub fine-grained token-like request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "GitHub_Pat_unsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "GitHub_Pat_unsafe-live-secret",
		},
		{
			name:       "Slack bot token-like request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "XoXb-unsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "XoXb-unsafe-live-secret",
		},
		{
			name:       "Slack user token-like request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "xOxP-unsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "xOxP-unsafe-live-secret",
		},
		{
			name:       "Google API key-like request identifier",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "AiZaunsafe-live-secret",
			known:      true,
			microUSD:   17,
			method:     "provider_reported",
			continued:  true,
			secret:     "AiZaunsafe-live-secret",
		},
		{
			name:       "spend over ceiling",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "req_123",
			known:      true,
			microUSD:   25_001,
			method:     "provider_reported",
			continued:  true,
		},
		{
			name:       "profile mismatch",
			profile:    "openai-responses",
			logProfile: "openai-chat",
			requestID:  "req_123",
			known:      false,
			microUSD:   0,
			method:     "not_reported",
			continued:  false,
		},
		{
			name:       "unreported cost with a reported method",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "req_123",
			known:      false,
			microUSD:   0,
			method:     "provider_reported",
			continued:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			rawPath := filepath.Join(directory, "live-contract.raw.log")
			evidencePath := filepath.Join(directory, "evidence.json")
			logPath := filepath.Join(directory, "live-contract.log")
			writeLiveContractRawLog(t, rawPath, test.logProfile, test.requestID, "resp_456", test.known, test.microUSD, test.method, test.continued)

			output, err := runLiveContractEvidence(root,
				"record",
				"--profile", test.profile,
				"--input", rawPath,
				"--evidence", evidencePath,
				"--log", logPath,
				"--generated-at", "2026-07-15T00:00:00Z",
			)
			if err == nil {
				t.Fatalf("record accepted %s: %s", test.name, output)
			}
			if test.secret != "" && strings.Contains(string(output), test.secret) {
				t.Fatalf("recorder echoed rejected secret-like input: %q", output)
			}
			for _, path := range []string{evidencePath, logPath} {
				if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
					t.Fatalf("recorder left output %q after rejected input: %v", path, statErr)
				}
			}
		})
	}
}

func TestLiveContractEvidenceRecorderAllowsTheCheckedInUsageCostMethod(t *testing.T) {
	root := repositoryRoot(t)
	directory := t.TempDir()
	rawPath := filepath.Join(directory, "live-contract.raw.log")
	evidencePath := filepath.Join(directory, "evidence.json")
	logPath := filepath.Join(directory, "live-contract.log")
	writeLiveContractRawLog(t, rawPath, "openai-responses", "req_123", "resp_456", true, 17, "usage", true)

	output, err := runLiveContractEvidence(root,
		"record",
		"--profile", "openai-responses",
		"--input", rawPath,
		"--evidence", evidencePath,
		"--log", logPath,
		"--generated-at", "2026-07-15T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("record usage-method evidence: %v\\n%s", err, output)
	}

	data, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		ActualSpend struct {
			Method string `json:"method"`
		} `json:"actual_spend"`
	}
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatalf("decode usage-method evidence: %v", err)
	}
	if evidence.ActualSpend.Method != "usage" {
		t.Fatalf("actual spend method = %q, want usage", evidence.ActualSpend.Method)
	}
}

func TestLiveContractEvidenceSchemaIsClosedAndMatchesTheRecorderContract(t *testing.T) {
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "release", "live-provider-contract.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode live provider evidence schema: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("live provider evidence schema must be closed: %#v", schema["additionalProperties"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("live provider evidence schema properties = %#v, want mapping", schema["properties"])
	}
	for _, field := range []string{"schema_version", "generated_at", "profile", "tenant", "ceiling_micro_usd", "actual_spend", "request_id", "response_id", "actual_service_class", "continuation_verified"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("live provider evidence schema is missing %q", field)
		}
	}
	for _, forbidden := range []string{"prompt", "output", "endpoint", "credential", "api_key", "raw"} {
		if _, found := properties[forbidden]; found {
			t.Fatalf("live provider evidence schema permits forbidden %q", forbidden)
		}
	}
	actualSpend, ok := properties["actual_spend"].(map[string]any)
	if !ok {
		t.Fatalf("live provider actual spend schema = %#v, want mapping", properties["actual_spend"])
	}
	spendProperties, ok := actualSpend["properties"].(map[string]any)
	if !ok {
		t.Fatalf("live provider actual spend properties = %#v, want mapping", actualSpend["properties"])
	}
	method, ok := spendProperties["method"].(map[string]any)
	if !ok || !sameStringSet(stringSequence(t, "live provider cost method", method, "enum"), []string{"provider_reported", "usage", "not_reported"}) {
		t.Fatalf("live provider evidence cost method must remain closed to recorded methods, got %#v", spendProperties["method"])
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("live provider evidence schema definitions = %#v, want mapping", schema["$defs"])
	}
	identifier, ok := definitions["safe_identifier"].(map[string]any)
	if !ok {
		t.Fatalf("live provider safe identifier schema = %#v, want mapping", definitions["safe_identifier"])
	}
	allOf, ok := identifier["allOf"].([]any)
	if !ok {
		t.Fatalf("live provider safe identifier allOf = %#v, want sequence", identifier["allOf"])
	}
	gotSecretPatterns := make(map[string]bool)
	for _, rawRule := range allOf {
		rule, ok := rawRule.(map[string]any)
		if !ok {
			continue
		}
		not, ok := rule["not"].(map[string]any)
		if !ok {
			continue
		}
		if pattern, ok := not["pattern"].(string); ok {
			gotSecretPatterns[pattern] = true
		}
	}
	for _, pattern := range []string{
		"^[sS][kK][-_]",
		"^[eE][yY][jJ]",
		"^[aA][kK][iI][aA]",
		"^[aA][sS][iI][aA]",
		"^[gG][hH][pP]_",
		"^[gG][iI][tT][hH][uU][bB]_[pP][aA][tT]_",
		"^[xX][oO][xX][bB]-",
		"^[xX][oO][xX][pP]-",
		"^[aA][iI][zZ][aA]",
	} {
		if !gotSecretPatterns[pattern] {
			t.Fatalf("live provider safe identifier schema does not reject recorder secret pattern %q: %#v", pattern, gotSecretPatterns)
		}
	}
}

func runLiveContractEvidence(root string, arguments ...string) ([]byte, error) {
	command := exec.Command("python3", append([]string{filepath.Join(root, "scripts", "release", "live-contract-evidence.py")}, arguments...)...)
	command.Dir = root
	return command.CombinedOutput()
}

func writeLiveContractRawLog(t *testing.T, path, profile, requestID, responseID string, known bool, microUSD int, method string, continued bool) {
	t.Helper()
	content := strings.Join([]string{
		"=== RUN   TestLiveProviderContracts/" + profile,
		"    live_provider_test.go:35: profile=" + profile + " tenant=llmtw-live-contract request_id=" + requestID + " response_id=" + responseID + " actual_service_class=standard actual_spend_known=" + strconv.FormatBool(known) + " actual_micro_usd=" + strconv.Itoa(microUSD) + " cost_method=" + method + " continuation_verified=" + strconv.FormatBool(continued),
		"--- PASS: TestLiveProviderContracts/" + profile,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
