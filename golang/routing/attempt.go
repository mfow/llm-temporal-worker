package routing

import "time"

type Attempt struct {
	CandidateID string
	StartedAt   time.Time
	FinishedAt  time.Time
	Outcome     FailureKind
}

type AttemptView struct {
	Attempts []Attempt
	Max      int
}

func (view AttemptView) Exhausted() bool {
	return view.Max > 0 && len(view.Attempts) >= view.Max
}
