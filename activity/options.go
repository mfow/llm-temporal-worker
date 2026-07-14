package activity

import (
	"fmt"
	"math"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type ActivityPolicy struct {
	StartToClose       time.Duration
	ScheduleToClose    time.Duration
	HeartbeatTimeout   time.Duration
	InitialRetry       time.Duration
	BackoffCoefficient float64
	MaximumRetry       time.Duration
	MaximumAttempts    int32
	RetryHorizon       time.Duration
	OperationRetention time.Duration
	ProviderTimeout    time.Duration
}

func (policy ActivityPolicy) Validate() error {
	if policy.StartToClose <= 0 || policy.ScheduleToClose <= 0 {
		return fmt.Errorf("start-to-close and schedule-to-close must be positive")
	}
	if policy.ScheduleToClose <= policy.StartToClose {
		return fmt.Errorf("schedule-to-close must exceed start-to-close")
	}
	if policy.HeartbeatTimeout <= 0 || policy.HeartbeatTimeout >= policy.StartToClose {
		return fmt.Errorf("heartbeat timeout must be positive and shorter than start-to-close")
	}
	if policy.InitialRetry < 0 || policy.MaximumRetry < 0 {
		return fmt.Errorf("retry intervals must not be negative")
	}
	if policy.BackoffCoefficient != 0 && (math.IsNaN(policy.BackoffCoefficient) || math.IsInf(policy.BackoffCoefficient, 0) || policy.BackoffCoefficient < 1) {
		return fmt.Errorf("backoff coefficient must be at least one")
	}
	if policy.MaximumAttempts < 1 || policy.MaximumAttempts > 100 {
		return fmt.Errorf("maximum attempts must be between 1 and 100")
	}
	if policy.OperationRetention <= 0 {
		return fmt.Errorf("operation retention must be positive")
	}
	if policy.RetryHorizon < 0 || policy.RetryHorizon > policy.OperationRetention {
		return fmt.Errorf("retry horizon must not exceed operation retention")
	}
	if policy.ProviderTimeout > 0 && policy.ProviderTimeout >= policy.StartToClose {
		return fmt.Errorf("provider timeout must leave time for Activity finalization")
	}
	return nil
}

func (policy ActivityPolicy) TemporalOptions() (workflow.ActivityOptions, error) {
	if err := policy.Validate(); err != nil {
		return workflow.ActivityOptions{}, err
	}
	backoff := policy.BackoffCoefficient
	if backoff == 0 {
		backoff = 2
	}
	return workflow.ActivityOptions{
		StartToCloseTimeout:    policy.StartToClose,
		ScheduleToCloseTimeout: policy.ScheduleToClose,
		HeartbeatTimeout:       policy.HeartbeatTimeout,
		WaitForCancellation:    true,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        policy.InitialRetry,
			BackoffCoefficient:     backoff,
			MaximumInterval:        policy.MaximumRetry,
			MaximumAttempts:        policy.MaximumAttempts,
			NonRetryableErrorTypes: []string{ErrorTypeInvalidArgument, ErrorTypeAuthentication, ErrorTypeAmbiguous, ErrorTypeOperationConflict, ErrorTypeStateCorrupt},
		},
	}, nil
}
