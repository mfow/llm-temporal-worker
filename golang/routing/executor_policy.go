package routing

import (
	"fmt"
	"time"
)

type Movement string

const (
	MovementStop     Movement = "stop"
	MovementNext     Movement = "next_route"
	MovementWait     Movement = "wait"
	MovementRetrieve Movement = "retrieve_existing"
)

type Decision struct {
	Movement    Movement
	CandidateID string
	RetryAfter  time.Duration
	Reason      string
}

func NextDecision(plan Plan, attempts AttemptView, outcome FailureKind, now, deadline time.Time) (Decision, error) {
	if len(plan.Candidates) == 0 {
		return Decision{Movement: MovementStop, Reason: "empty plan"}, fmt.Errorf("route plan is empty")
	}
	if attempts.Exhausted() {
		return Decision{Movement: MovementStop, Reason: "attempt limit exhausted"}, nil
	}
	switch outcome {
	case FailureSuccess:
		return Decision{Movement: MovementStop, Reason: "completed"}, nil
	case FailureAmbiguous:
		return Decision{Movement: MovementStop, Reason: "ambiguous dispatch cannot be resubmitted"}, nil
	case FailureAuthentication, FailureConfiguration:
		return Decision{Movement: MovementStop, Reason: "route requires configuration change"}, nil
	case FailureDefiniteTransient:
		if len(attempts.Attempts) >= len(plan.Candidates) {
			return Decision{Movement: MovementStop, Reason: "all candidates exhausted"}, nil
		}
		candidate := plan.Candidates[len(attempts.Attempts)]
		if !deadline.IsZero() && !now.Before(deadline) {
			return Decision{Movement: MovementStop, Reason: "deadline exhausted"}, nil
		}
		return Decision{Movement: MovementNext, CandidateID: candidate.ID, Reason: "definite transient failure"}, nil
	default:
		return Decision{Movement: MovementStop, Reason: "unclassified failure"}, nil
	}
}
