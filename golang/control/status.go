// Package control contains the provider-control domain model used by the
// durable status and inventory repositories.  It deliberately has no
// provider or database dependencies: observations are normalised here before
// they are appended to storage or exposed by a query Activity.
package control

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Source string

const (
	SourceInference     Source = "inference"
	SourceManagementAPI Source = "management_api"
	SourceStartupProbe  Source = "startup_probe"
	SourceOperator      Source = "operator"
)

func (source Source) valid() bool {
	return source == SourceInference || source == SourceManagementAPI || source == SourceStartupProbe || source == SourceOperator
}

type Availability string

const (
	AvailabilityAvailable   Availability = "available"
	AvailabilityDegraded    Availability = "degraded"
	AvailabilityUnavailable Availability = "unavailable"
	AvailabilityUnknown     Availability = "unknown"
)

func (availability Availability) valid() bool {
	return availability == AvailabilityAvailable || availability == AvailabilityDegraded || availability == AvailabilityUnavailable || availability == AvailabilityUnknown
}

type CreditState string

const (
	CreditOK        CreditState = "ok"
	CreditLow       CreditState = "low"
	CreditExhausted CreditState = "exhausted"
	CreditUnknown   CreditState = "unknown"
)

func (credit CreditState) valid() bool {
	return credit == CreditOK || credit == CreditLow || credit == CreditExhausted || credit == CreditUnknown
}

type BillingState string

const (
	BillingOK      BillingState = "ok"
	BillingIssue   BillingState = "issue"
	BillingUnknown BillingState = "unknown"
)

func (billing BillingState) valid() bool {
	return billing == BillingOK || billing == BillingIssue || billing == BillingUnknown
}

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

// StatusObservation is the provider-facing input to NewStatusEvent.  The
// caller may receive arbitrary provider text, but only bounded safe codes and
// digests are retained in the event.  Raw response bodies and credentials
// never belong in this type.
type StatusObservation struct {
	ConfigDigest        [32]byte
	RouteID             string
	EndpointID          string
	EndpointAccountHMAC [32]byte
	Provider            string
	EndpointFamily      string
	ObservedAt          time.Time
	Source              Source
	Availability        Availability
	Credit              CreditState
	Billing             BillingState
	SafeErrorCode       string
	ProviderCode        string
	EvidenceDigest      [32]byte
	ConfigEpoch         string
	ExpiresAt           time.Time
}

// StatusEvent is the closed safe event written to the append-only event
// ledger.  NewStatusEvent rejects incomplete or unsafe observations so every
// persisted event has the same invariant.
type StatusEvent struct {
	StatusObservation
	EventDigest [32]byte
}

func NewStatusEvent(observation StatusObservation) (StatusEvent, error) {
	if observation.ConfigDigest == ([32]byte{}) || observation.EndpointAccountHMAC == ([32]byte{}) || observation.EvidenceDigest == ([32]byte{}) {
		return StatusEvent{}, errors.New("status observation requires config, account, and evidence digests")
	}
	for name, value := range map[string]string{"route_id": observation.RouteID, "endpoint_id": observation.EndpointID, "provider": observation.Provider, "endpoint_family": observation.EndpointFamily, "config_epoch": observation.ConfigEpoch} {
		if err := validateIdentifier(name, value); err != nil {
			return StatusEvent{}, err
		}
	}
	if !observation.Source.valid() || !observation.Availability.valid() || !observation.Credit.valid() || !observation.Billing.valid() {
		return StatusEvent{}, errors.New("status observation contains an unknown enum")
	}
	if (observation.Credit == CreditExhausted || observation.Billing == BillingIssue) && observation.ProviderCode == "" && observation.Source != SourceOperator {
		return StatusEvent{}, errors.New("exhausted credit or billing issue requires provider evidence or an operator event")
	}
	if observation.Source == SourceOperator && (observation.Credit == CreditExhausted || observation.Billing == BillingIssue) && observation.SafeErrorCode == "" && observation.ProviderCode == "" {
		return StatusEvent{}, errors.New("operator credit or billing incident requires a safe evidence code")
	}
	if observation.ObservedAt.IsZero() || observation.ExpiresAt.IsZero() || !observation.ExpiresAt.After(observation.ObservedAt) {
		return StatusEvent{}, errors.New("status observation has an invalid observation interval")
	}
	for name, value := range map[string]string{"safe_error_code": observation.SafeErrorCode, "provider_code": observation.ProviderCode} {
		if value == "" {
			continue
		}
		if err := validateSafeCode(name, value); err != nil {
			return StatusEvent{}, err
		}
	}
	data := []byte(strings.Join([]string{
		hex.EncodeToString(observation.ConfigDigest[:]), observation.RouteID, observation.EndpointID,
		hex.EncodeToString(observation.EndpointAccountHMAC[:]), observation.Provider, observation.EndpointFamily,
		observation.ObservedAt.UTC().Format(time.RFC3339Nano), string(observation.Source), string(observation.Availability),
		string(observation.Credit), string(observation.Billing), observation.SafeErrorCode, observation.ProviderCode,
		hex.EncodeToString(observation.EvidenceDigest[:]), observation.ConfigEpoch, observation.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, "\x00"))
	return StatusEvent{StatusObservation: observation, EventDigest: sha256.Sum256(data)}, nil
}

func validateIdentifier(name, value string) error {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s is empty or unsafe", name)
	}
	return nil
}

func validateSafeCode(name, value string) error {
	if len(value) > 128 || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is too long or contains surrounding whitespace", name)
	}
	for _, r := range value {
		if r < '!' || r > '~' {
			return fmt.Errorf("%s contains unsafe characters", name)
		}
	}
	return nil
}

// RouteStatus is the transactional current projection of status events.
// Credit and billing incidents are sticky: ordinary inference/startup
// observations cannot clear them.  Only an explicit OK observation from the
// provider management API or an operator can clear an incident, and it must
// use the current configuration epoch.
type RouteStatus struct {
	ConfigDigest                [32]byte
	ConfigEpoch                 string
	RouteID                     string
	EndpointID                  string
	EndpointAccountHMAC         [32]byte
	Provider                    string
	EndpointFamily              string
	Availability                Availability
	Credit                      CreditState
	Billing                     BillingState
	Circuit                     CircuitState
	ConsecutiveDefiniteFailures int
	LastEventDigest             [32]byte
	ObservedAt                  time.Time
	StaleAfter                  time.Time
	CreditConfirmedAt           time.Time
}

// Apply updates a route projection and enforces the sticky incident rules.
// A false return means the event was stale or belonged to another route/config
// and therefore must not replace the current projection.
func (status *RouteStatus) Apply(event StatusEvent) bool {
	if status == nil || event.RouteID == "" || event.EventDigest == ([32]byte{}) {
		return false
	}
	if !status.ObservedAt.IsZero() && !event.ObservedAt.After(status.ObservedAt) {
		return false
	}
	if !status.ConfigDigestIsZero() && status.RouteID != event.RouteID {
		return false
	}
	if status.ConfigEpoch != "" && status.ConfigEpoch != event.ConfigEpoch {
		// A new epoch starts a fresh projection. It must not inherit an old
		// provider incident, but the new event itself may establish one.
		*status = RouteStatus{}
	}
	if !status.ConfigDigestIsZero() && status.ConfigDigest != event.ConfigDigest {
		return false
	}
	if status.RouteID != "" && status.EndpointID != event.EndpointID {
		return false
	}
	if status.RouteID != "" && (status.EndpointAccountHMAC != event.EndpointAccountHMAC || status.Provider != event.Provider) {
		return false
	}
	if status.Credit == CreditLow || status.Credit == CreditExhausted {
		if !(event.canClearIncident() && event.Credit == CreditOK) {
			event.Credit = status.Credit
		}
	}
	if status.Billing == BillingIssue {
		if !(event.canClearIncident() && event.Billing == BillingOK) {
			event.Billing = BillingIssue
		}
	}
	if event.Availability == AvailabilityAvailable && event.Credit == CreditOK && event.Billing == BillingOK {
		status.ConsecutiveDefiniteFailures = 0
	}
	status.ConfigDigest, status.ConfigEpoch = event.ConfigDigest, event.ConfigEpoch
	status.RouteID, status.EndpointID = event.RouteID, event.EndpointID
	status.EndpointAccountHMAC, status.Provider, status.EndpointFamily = event.EndpointAccountHMAC, event.Provider, event.EndpointFamily
	status.Availability, status.Credit, status.Billing = event.Availability, event.Credit, event.Billing
	if status.Circuit == "" {
		status.Circuit = CircuitClosed
	}
	status.LastEventDigest, status.ObservedAt, status.StaleAfter = event.EventDigest, event.ObservedAt, event.ExpiresAt
	if status.Credit == CreditLow || status.Credit == CreditExhausted {
		status.CreditConfirmedAt = event.ObservedAt
	} else if status.Credit == CreditOK && event.canClearIncident() {
		status.CreditConfirmedAt = time.Time{}
	}
	return true
}

func (status RouteStatus) ConfigDigestIsZero() bool { return status.ConfigDigest == ([32]byte{}) }

func (event StatusEvent) canClearIncident() bool {
	return (event.Source == SourceManagementAPI || event.Source == SourceOperator) && event.ConfigEpoch != ""
}

func (status RouteStatus) StaleAt(now time.Time) bool {
	return now.IsZero() || status.StaleAfter.IsZero() || !now.Before(status.StaleAfter)
}
