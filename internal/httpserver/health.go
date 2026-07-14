package httpserver

import "sync/atomic"

// HealthState is the process health state shared by the probe handlers and
// the worker lifecycle. Readiness starts false so a process cannot receive
// traffic before its dependencies and Temporal worker have started.
type HealthState struct {
	live  atomic.Bool
	ready atomic.Bool
}

func NewHealthState() *HealthState {
	state := &HealthState{}
	state.live.Store(true)
	return state
}

func (state *HealthState) Live() bool {
	return state != nil && state.live.Load()
}

func (state *HealthState) Ready() bool {
	return state != nil && state.ready.Load()
}

func (state *HealthState) SetLive(value bool) {
	if state != nil {
		state.live.Store(value)
	}
}

func (state *HealthState) SetReady(value bool) {
	if state != nil {
		state.ready.Store(value)
	}
}
