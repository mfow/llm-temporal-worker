package admission

import (
	"crypto/sha256"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/pricing"
	"github.com/mfow/llm-temporal-worker/golang/state"
)

type OperationState string

const (
	StateReserved       OperationState = "reserved"
	StateDispatching    OperationState = "dispatching"
	StateCompleted      OperationState = "completed"
	StateDefiniteFailed OperationState = "definite_failed"
	StateAmbiguous      OperationState = "ambiguous"
	StateCanceled       OperationState = "canceled"
)

func (state OperationState) Terminal() bool {
	return state == StateCompleted || state == StateDefiniteFailed || state == StateAmbiguous || state == StateCanceled
}

type DispatchCertainty string

const (
	NotDispatched DispatchCertainty = "not_dispatched"
	Rejected      DispatchCertainty = "rejected"
	Accepted      DispatchCertainty = "accepted"
	Ambiguous     DispatchCertainty = "ambiguous"
)

type WindowReservation struct {
	PolicyID      string
	WindowID      string
	Bucket        int64
	Amount        pricing.MicroUSD
	Limit         pricing.MicroUSD
	BucketNanos   int64
	DurationNanos int64
}

type AttemptFacts struct {
	RouteID           string
	EndpointID        string
	Provider          string
	ProviderRequestID string
	ServiceClass      string
	Dispatch          DispatchCertainty
	AttemptNumber     int
}

type Operation struct {
	ID               string
	ScopeKey         string
	RequestDigest    [32]byte
	State            OperationState
	ReservedMicroUSD pricing.MicroUSD
	IncurredMicroUSD pricing.MicroUSD
	FinalMicroUSD    pricing.MicroUSD
	Reservations     []WindowReservation
	ConfigVersion    string
	PriceVersion     string
	Attempt          AttemptFacts
	ResultRef        *state.BlobRef
	DispatchToken    string
	LeaseUntil       time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ExpiresAt        time.Time
}

func (operation Operation) Clone() Operation {
	operation.Reservations = append([]WindowReservation(nil), operation.Reservations...)
	if operation.ResultRef != nil {
		copyRef := *operation.ResultRef
		operation.ResultRef = &copyRef
	}
	return operation
}

type BeginRequest struct {
	ID            string
	ScopeKey      string
	RequestDigest [32]byte
	Reservation   pricing.MicroUSD
	Reservations  []WindowReservation
	ConfigVersion string
	PriceVersion  string
	LeaseUntil    time.Time
	ExpiresAt     time.Time
}

type BeginResult struct {
	Operation Operation
	Existing  bool
	Denied    *Denial
}

type Denial struct {
	RetryAfter time.Duration
	PolicyID   string
	WindowID   string
	Limit      pricing.MicroUSD
	Active     pricing.MicroUSD
	Requested  pricing.MicroUSD
}

type DispatchRequest struct {
	OperationID   string
	DispatchToken string
	Attempt       AttemptFacts
	LeaseUntil    time.Time
}

type AttemptOutcome struct {
	Certainty         DispatchCertainty
	Incurred          pricing.MicroUSD
	ProviderRequestID string
	Attempt           AttemptFacts
}

type ContinueRequest struct {
	OperationID   string
	DispatchToken string
	Outcome       AttemptOutcome
	Remaining     pricing.MicroUSD
	Reservations  []WindowReservation
	LeaseUntil    time.Time
	ExpiresAt     time.Time
}

type ContinueResult struct {
	Operation Operation
	Denied    *Denial
}

type CompleteRequest struct {
	OperationID   string
	DispatchToken string
	Actual        pricing.MicroUSD
	ResultRef     *state.BlobRef
	Attempt       AttemptFacts
}

type FailRequest struct {
	OperationID   string
	DispatchToken string
	Certainty     DispatchCertainty
	Incurred      pricing.MicroUSD
	Attempt       AttemptFacts
	Reason        string
}

func Digest(value []byte) [32]byte { return sha256.Sum256(value) }
