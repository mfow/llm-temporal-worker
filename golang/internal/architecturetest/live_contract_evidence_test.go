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

const liveContractSourceRevision = "0123456789abcdef0123456789abcdef01234567"

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
		"--source-revision", liveContractSourceRevision,
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
		"source_revision":       true,
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
	if got := evidence["source_revision"]; got != liveContractSourceRevision {
		t.Fatalf("evidence source_revision = %#v, want %q", got, liveContractSourceRevision)
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
	if !strings.Contains(string(log), "source_revision="+liveContractSourceRevision+"\n") {
		t.Fatalf("redacted log does not bind the source revision: %q", log)
	}

	output, err = runLiveContractEvidence(root,
		"verify",
		"--evidence", evidencePath,
		"--log", logPath,
		"--source-revision", liveContractSourceRevision,
	)
	if err != nil {
		t.Fatalf("verify recorded live contract evidence: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "live contract evidence verified" {
		t.Fatalf("verify output = %q, want fixed success message", got)
	}

	output, err = runLiveContractEvidence(root,
		"verify",
		"--evidence", evidencePath,
		"--log", logPath,
		"--source-revision", "fedcba9876543210fedcba9876543210fedcba98",
	)
	if err == nil {
		t.Fatalf("verify accepted evidence for a different source revision: %s", output)
	}
}

func TestLiveContractEvidenceRecorderRejectsInvalidSourceRevision(t *testing.T) {
	root := repositoryRoot(t)
	for _, revision := range []string{
		"0123456789abcdef0123456789abcdef0123456",
		"0123456789abcdef0123456789abcdef0123456g",
		"0123456789ABCDEF0123456789abcdef01234567",
	} {
		t.Run(revision, func(t *testing.T) {
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
				"--source-revision", revision,
				"--generated-at", "2026-07-15T00:00:00Z",
			)
			if err == nil {
				t.Fatalf("record accepted invalid source revision %q: %s", revision, output)
			}
			for _, path := range []string{evidencePath, logPath} {
				if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
					t.Fatalf("recorder left output %q after invalid source revision: %v", path, statErr)
				}
			}
		})
	}
}

func TestLiveContractEvidenceRecorderRejectsUnsafeOrInconsistentInputWithoutEchoingIt(t *testing.T) {
	root := repositoryRoot(t)
	for _, test := range []struct {
		name         string
		profile      string
		logProfile   string
		requestID    string
		known        bool
		microUSD     int
		method       string
		serviceClass string
		continued    bool
		secret       string
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
			name:       "unrecognized reported cost method",
			profile:    "openai-responses",
			logProfile: "openai-responses",
			requestID:  "req_123",
			known:      true,
			microUSD:   17,
			method:     "catalog_usage",
			continued:  true,
		},
		{
			name:         "nonstandard actual service class",
			profile:      "openai-responses",
			logProfile:   "openai-responses",
			requestID:    "req_123",
			known:        true,
			microUSD:     17,
			method:       "provider_reported",
			serviceClass: "priority",
			continued:    true,
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
			serviceClass := test.serviceClass
			if serviceClass == "" {
				serviceClass = "standard"
			}
			writeLiveContractRawLogWithServiceClass(t, rawPath, test.logProfile, test.requestID, "resp_456", serviceClass, test.known, test.microUSD, test.method, test.continued)

			output, err := runLiveContractEvidence(root,
				"record",
				"--profile", test.profile,
				"--input", rawPath,
				"--evidence", evidencePath,
				"--log", logPath,
				"--source-revision", liveContractSourceRevision,
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

func TestLiveContractEvidenceRecorderAllowsEachSupportedReportedCostMethod(t *testing.T) {
	root := repositoryRoot(t)
	for _, test := range []struct {
		profile string
		method  string
	}{
		{profile: "openai-responses", method: "provider_reported"},
		{profile: "openai-responses", method: "usage"},
		{profile: "openrouter-chat", method: "openrouter_reported"},
		{profile: "exa-chat", method: "exa_reported"},
	} {
		t.Run(test.method, func(t *testing.T) {
			directory := t.TempDir()
			rawPath := filepath.Join(directory, "live-contract.raw.log")
			evidencePath := filepath.Join(directory, "evidence.json")
			logPath := filepath.Join(directory, "live-contract.log")
			writeLiveContractRawLog(t, rawPath, test.profile, "req_123", "resp_456", true, 17, test.method, true)

			output, err := runLiveContractEvidence(root,
				"record",
				"--profile", test.profile,
				"--input", rawPath,
				"--evidence", evidencePath,
				"--log", logPath,
				"--source-revision", liveContractSourceRevision,
				"--generated-at", "2026-07-15T00:00:00Z",
			)
			if err != nil {
				t.Fatalf("record %s evidence: %v\\n%s", test.method, err, output)
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
				t.Fatalf("decode %s evidence: %v", test.method, err)
			}
			if evidence.ActualSpend.Method != test.method {
				t.Fatalf("actual spend method = %q, want %q", evidence.ActualSpend.Method, test.method)
			}
		})
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
	got := stringSequence(t, "live provider evidence schema", schema, "required")
	want := []string{
		"schema_version",
		"generated_at",
		"source_revision",
		"profile",
		"tenant",
		"ceiling_micro_usd",
		"actual_spend",
		"request_id",
		"response_id",
		"actual_service_class",
		"continuation_verified",
	}
	if !sameStringSet(got, want) {
		t.Fatalf("live provider evidence schema required fields = %#v, want %#v", got, want)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("live provider evidence schema properties = %#v, want mapping", schema["properties"])
	}
	for _, field := range []string{"schema_version", "generated_at", "source_revision", "profile", "tenant", "ceiling_micro_usd", "actual_spend", "request_id", "response_id", "actual_service_class", "continuation_verified"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("live provider evidence schema is missing %q", field)
		}
	}
	sourceRevision, ok := properties["source_revision"].(map[string]any)
	if !ok || sourceRevision["pattern"] != "^[0-9a-f]{40}$" {
		t.Fatalf("live provider source revision schema must require a full lowercase Git SHA, got %#v", properties["source_revision"])
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
	if !ok || !sameStringSet(stringSequence(t, "live provider cost method", method, "enum"), []string{"provider_reported", "usage", "openrouter_reported", "exa_reported", "not_reported"}) {
		t.Fatalf("live provider evidence cost method must remain closed to recorded methods, got %#v", spendProperties["method"])
	}
	allOfSpend, ok := actualSpend["allOf"].([]any)
	if !ok {
		t.Fatalf("live provider actual spend allOf = %#v, want sequence", actualSpend["allOf"])
	}
	var knownMethod map[string]any
	for _, rawRule := range allOfSpend {
		rule, ok := rawRule.(map[string]any)
		if !ok {
			continue
		}
		condition, ok := rule["if"].(map[string]any)
		if !ok {
			continue
		}
		conditionProperties, ok := condition["properties"].(map[string]any)
		if !ok {
			continue
		}
		known, ok := conditionProperties["known"].(map[string]any)
		if !ok || known["const"] != true {
			continue
		}
		then, ok := rule["then"].(map[string]any)
		if !ok {
			continue
		}
		thenProperties, ok := then["properties"].(map[string]any)
		if !ok {
			continue
		}
		knownMethod, _ = thenProperties["method"].(map[string]any)
	}
	if !sameStringSet(stringSequence(t, "known live provider cost method", knownMethod, "enum"), []string{"provider_reported", "usage", "openrouter_reported", "exa_reported"}) {
		t.Fatalf("known live provider evidence cost method must remain closed to provider-reported methods, got %#v", knownMethod)
	}
	actualServiceClass, ok := properties["actual_service_class"].(map[string]any)
	if !ok || actualServiceClass["const"] != "standard" {
		t.Fatalf("live provider actual service class must be const standard, got %#v", properties["actual_service_class"])
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
	writeLiveContractRawLogWithServiceClass(t, path, profile, requestID, responseID, "standard", known, microUSD, method, continued)
}

func writeLiveContractRawLogWithServiceClass(t *testing.T, path, profile, requestID, responseID, serviceClass string, known bool, microUSD int, method string, continued bool) {
	t.Helper()
	content := strings.Join([]string{
		"=== RUN   TestLiveProviderContracts/" + profile,
		"    live_provider_test.go:35: profile=" + profile + " tenant=llmtw-live-contract request_id=" + requestID + " response_id=" + responseID + " actual_service_class=" + serviceClass + " actual_spend_known=" + strconv.FormatBool(known) + " actual_micro_usd=" + strconv.Itoa(microUSD) + " cost_method=" + method + " continuation_verified=" + strconv.FormatBool(continued),
		"--- PASS: TestLiveProviderContracts/" + profile,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
