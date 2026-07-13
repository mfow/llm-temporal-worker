package admission

import "testing"

func TestValidateTransition(t *testing.T) {
	for _, pair := range [][2]OperationState{{StateReserved, StateDispatching}, {StateDispatching, StateCompleted}, {StateDispatching, StateAmbiguous}, {StateDispatching, StateReserved}} {
		if err := ValidateTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("%s -> %s: %v", pair[0], pair[1], err)
		}
	}
	if err := ValidateTransition(StateCompleted, StateReserved); err == nil {
		t.Fatal("terminal transition accepted")
	}
}
