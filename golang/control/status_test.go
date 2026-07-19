package control

import (
	"testing"
	"time"
)

func digest(value byte) [32]byte {
	var digest [32]byte
	digest[0] = value
	return digest
}

func observation(at time.Time) StatusObservation {
	return StatusObservation{
		ConfigDigest: digest(1), RouteID: "route", EndpointID: "endpoint", EndpointAccountHMAC: digest(2),
		Provider: "provider", EndpointFamily: "chat", ObservedAt: at, Source: SourceInference,
		Availability: AvailabilityAvailable, Credit: CreditOK, Billing: BillingOK, EvidenceDigest: digest(3),
		ConfigEpoch: "epoch-1", ExpiresAt: at.Add(time.Minute),
	}
}

func TestStatusEventRejectsUnsafeProviderText(t *testing.T) {
	value := observation(time.Unix(100, 0))
	value.ProviderCode = "429 quota\n"
	if _, err := NewStatusEvent(value); err == nil {
		t.Fatal("unsafe provider code was accepted")
	}
}

func TestStatusEventRequiresEvidenceForCreditIncident(t *testing.T) {
	value := observation(time.Unix(100, 0))
	value.Credit = CreditExhausted
	if _, err := NewStatusEvent(value); err == nil {
		t.Fatal("exhausted credit without provider evidence was accepted")
	}
	value.Source, value.SafeErrorCode = SourceOperator, "credit_exhausted"
	if _, err := NewStatusEvent(value); err != nil {
		t.Fatalf("operator evidence rejected: %v", err)
	}
}

func TestRouteStatusCreditIncidentIsStickyUntilAuthoritativeClear(t *testing.T) {
	base := time.Unix(100, 0)
	status := RouteStatus{}
	value := observation(base)
	value.Credit, value.Billing = CreditExhausted, BillingIssue
	value.Source, value.ProviderCode = SourceManagementAPI, "insufficient_quota"
	event, err := NewStatusEvent(value)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Apply(event) || status.Credit != CreditExhausted || status.Billing != BillingIssue {
		t.Fatalf("incident projection = %#v", status)
	}
	value = observation(base.Add(time.Second))
	value.Source, value.Credit, value.Billing = SourceInference, CreditOK, BillingOK
	event, err = NewStatusEvent(value)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Apply(event) || status.Credit != CreditExhausted || status.Billing != BillingIssue {
		t.Fatalf("inference observation cleared incident: %#v", status)
	}
	value = observation(base.Add(1500 * time.Millisecond))
	value.Source, value.Credit, value.Billing = SourceStartupProbe, CreditUnknown, BillingUnknown
	event, err = NewStatusEvent(value)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Apply(event) || status.Credit != CreditExhausted || status.Billing != BillingIssue {
		t.Fatalf("unknown health observation cleared incident: %#v", status)
	}
	value = observation(base.Add(2 * time.Second))
	value.Source, value.Credit, value.Billing, value.ProviderCode = SourceManagementAPI, CreditOK, BillingOK, "quota_restored"
	event, err = NewStatusEvent(value)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Apply(event) || status.Credit != CreditOK || status.Billing != BillingOK {
		t.Fatalf("authoritative clear not applied: %#v", status)
	}
}

func TestRouteStatusRejectsStaleEvent(t *testing.T) {
	base := time.Unix(100, 0)
	status := RouteStatus{}
	first, _ := NewStatusEvent(observation(base))
	if !status.Apply(first) {
		t.Fatal("first event rejected")
	}
	second, _ := NewStatusEvent(observation(base))
	if status.Apply(second) {
		t.Fatal("stale event applied")
	}
}
