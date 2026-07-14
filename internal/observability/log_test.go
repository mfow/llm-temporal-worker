package observability_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/internal/observability"
	"github.com/mfow/llm-temporal-worker/llm/provider"
	"log/slog"
)

func TestLoggerDropsContentAndRawErrorsAndHashesTenant(t *testing.T) {
	var output bytes.Buffer
	logger, err := observability.NewLogger(observability.LogOptions{Format: "json", Level: "debug", Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	secret := "adversarial-secret-value"
	logger.Info(context.Background(), "provider request accepted", slog.String("tenant", secret), slog.String("prompt", secret), slog.String("unknown", secret))
	logger.Error(context.Background(), "provider failure", errors.New(secret), slog.String("operation_id", "op-1"))
	providerErr := provider.NewError(provider.CodeProviderUnavailable, provider.PhaseDispatch, provider.DispatchRejected, provider.RetryNextRoute, "safe provider failure")
	logger.Error(context.Background(), "provider failure", providerErr)
	encoded := output.String()
	if strings.Contains(encoded, secret) || strings.Contains(encoded, `"prompt"`) || strings.Contains(encoded, `"unknown"`) {
		t.Fatalf("unsafe log field leaked: %s", encoded)
	}
	if !strings.Contains(encoded, `"tenant_hash"`) || !strings.Contains(encoded, `"error_code":"provider_unavailable"`) {
		t.Fatalf("safe fields missing: %s", encoded)
	}
}

func TestLoggerRejectsUnsafeMessage(t *testing.T) {
	var output bytes.Buffer
	logger, err := observability.NewLogger(observability.LogOptions{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info(context.Background(), "prompt: hidden user content")
	if strings.Contains(output.String(), "hidden user content") {
		t.Fatalf("unsafe message leaked: %s", output.String())
	}
}
