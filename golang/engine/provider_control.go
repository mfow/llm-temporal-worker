package engine

import (
	"context"
	"crypto/sha256"
	"strings"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/admission"
	"github.com/mfow/llm-temporal-worker/golang/control"
	"github.com/mfow/llm-temporal-worker/golang/llm/provider"
	"github.com/mfow/llm-temporal-worker/golang/routing"
)

const providerStatusRetention = 15 * time.Minute

// recordProviderStatus is deliberately best effort. Provider control is
// durable operational state, but an unavailable control database must not turn
// an otherwise valid provider response into an inference failure.
func (engine *Engine) recordProviderStatus(ctx context.Context, snapshot Snapshot, operation admission.Operation, candidate routing.Candidate, result provider.Result, failure *provider.Error) {
	recorder := engine.dependencies.ProviderControl
	if recorder == nil {
		return
	}
	now := engine.dependencies.Clock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	observation := control.StatusObservation{
		ConfigDigest:        snapshot.ConfigDigest,
		RouteID:             candidate.RouteID,
		EndpointID:          candidate.EndpointID,
		EndpointAccountHMAC: candidate.EndpointAccountHMAC,
		Provider:            candidate.Provider,
		EndpointFamily:      candidate.Family,
		ObservedAt:          now.UTC(),
		Source:              control.SourceInference,
		Availability:        control.AvailabilityAvailable,
		Credit:              control.CreditOK,
		Billing:             control.BillingOK,
		ConfigEpoch:         snapshot.ConfigEpoch,
		ExpiresAt:           now.UTC().Add(providerStatusRetention),
	}
	if observation.ConfigEpoch == "" {
		observation.ConfigEpoch = snapshot.Version
	}
	if failure != nil {
		observation.Availability = providerStatusAvailability(failure)
		providerCode, safeCode := providerStatusCodes(failure)
		incident := control.ClassifyCredit(control.SourceInference, observation.ConfigEpoch, providerCode, safeCode, now.UTC())
		observation.Credit = incident.Credit
		observation.Billing = incident.Billing
		observation.ProviderCode = providerCode
		observation.SafeErrorCode = safeCode
	}
	observation.EvidenceDigest = providerEvidenceDigest(operation.ID, candidate, result, failure)
	// Persistence errors are intentionally ignored here. The recorder itself
	// should expose failures through its own control-plane telemetry.
	_ = recorder.RecordProviderStatus(ctx, observation)
}

func providerStatusAvailability(failure *provider.Error) control.Availability {
	if failure == nil {
		return control.AvailabilityAvailable
	}
	switch failure.Code {
	case provider.CodeProviderRateLimited:
		return control.AvailabilityDegraded
	case provider.CodeProviderUnavailable, provider.CodeAuthentication, provider.CodePermissionDenied, provider.CodeConfiguration:
		return control.AvailabilityUnavailable
	default:
		return control.AvailabilityUnknown
	}
}

func providerStatusCodes(failure *provider.Error) (providerCode, safeCode string) {
	if failure == nil {
		return "", ""
	}
	if failure.SafeDetails != nil {
		providerCode = strings.TrimSpace(failure.SafeDetails["provider_code"])
		if providerCode == "" {
			providerCode = strings.TrimSpace(failure.SafeDetails["provider_type"])
		}
	}
	// The closed provider code is safe to retain as the classifier code. It is
	// kept separate from provider-specific evidence so generic 429s cannot be
	// mistaken for credit exhaustion by ClassifyCredit.
	safeCode = string(failure.Code)
	return boundedStatusCode(providerCode), boundedStatusCode(safeCode)
}

func boundedStatusCode(value string) string {
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func providerEvidenceDigest(operationID string, candidate routing.Candidate, result provider.Result, failure *provider.Error) [32]byte {
	parts := []string{operationID, candidate.RouteID, candidate.EndpointID, candidate.Provider, candidate.Family, candidate.Model}
	if failure != nil {
		parts = append(parts, string(failure.Code), string(failure.Phase), string(failure.Dispatch), failure.Provider.RequestID)
	} else {
		parts = append(parts, result.Response.Provider.ResponseID, result.Response.Provider.RequestID, result.Response.Provider.GenerationID)
	}
	data := strings.Join(parts, "\x00")
	digest := sha256.Sum256([]byte(data))
	return digest
}
