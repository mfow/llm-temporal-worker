package routing

import (
	"testing"
	"time"
)

func TestNextDecisionStopsAmbiguousAndMovesTransient(t *testing.T) {
	plan := Plan{Candidates: []Candidate{{ID: "one"}, {ID: "two"}}}
	now := time.Unix(100, 0)
	decision, err := NextDecision(plan, AttemptView{Attempts: []Attempt{{CandidateID: "one"}}}, FailureDefiniteTransient, now, now.Add(time.Minute))
	if err != nil || decision.Movement != MovementNext || decision.CandidateID != "two" {
		t.Fatalf("transient decision = %#v %v", decision, err)
	}
	decision, err = NextDecision(plan, AttemptView{Attempts: []Attempt{{CandidateID: "one"}}}, FailureAmbiguous, now, now.Add(time.Minute))
	if err != nil || decision.Movement != MovementStop {
		t.Fatalf("ambiguous decision = %#v %v", decision, err)
	}
}
