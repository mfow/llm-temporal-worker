package control

import (
	"testing"
	"time"
)

func TestClassifyCreditDoesNotTreatGenericRateLimitAsExhausted(t *testing.T) {
	incident := ClassifyCredit(SourceInference, "epoch", "", "", time.Unix(1, 0))
	if incident.Credit == CreditExhausted || incident.Billing == BillingIssue {
		t.Fatalf("generic rate limit became billing incident: %#v", incident)
	}
}

func TestClassifyCreditUsesDocumentedProviderCodes(t *testing.T) {
	incident := ClassifyCredit(SourceInference, "epoch", "insufficient_quota", "", time.Unix(1, 0))
	if incident.Credit != CreditExhausted || incident.Billing != BillingIssue {
		t.Fatalf("provider code was not classified: %#v", incident)
	}
	cleared := ClassifyCredit(SourceManagementAPI, "epoch", "quota_restored", "", time.Unix(2, 0))
	if cleared.Credit != CreditOK || cleared.Billing != BillingOK {
		t.Fatalf("restore code was not classified: %#v", cleared)
	}
}
