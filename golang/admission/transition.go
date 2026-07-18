package admission

import "fmt"

func ValidateTransition(from, to OperationState) error {
	if from.Terminal() {
		if from == to {
			return nil
		}
		return fmt.Errorf("terminal operation %s cannot transition to %s", from, to)
	}
	switch from {
	case StateReserved:
		if to == StateDispatching || to == StateDefiniteFailed || to == StateCanceled || to == StateAmbiguous {
			return nil
		}
	case StateDispatching:
		if to == StateCompleted || to == StateDefiniteFailed || to == StateAmbiguous || to == StateCanceled || to == StateReserved {
			return nil
		}
	}
	return fmt.Errorf("invalid operation transition %s -> %s", from, to)
}

func ValidateOutcome(outcome AttemptOutcome) error {
	if outcome.Certainty != NotDispatched && outcome.Certainty != Rejected && outcome.Certainty != Accepted && outcome.Certainty != Ambiguous {
		return fmt.Errorf("invalid dispatch certainty %q", outcome.Certainty)
	}
	if outcome.Incurred < 0 || !outcome.Incurred.Valid() {
		return fmt.Errorf("invalid incurred cost")
	}
	return nil
}
