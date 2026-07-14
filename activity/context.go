package activity

import (
	"context"

	sdkactivity "go.temporal.io/sdk/activity"
)

type ExecutionContext struct {
	ActivityID string
	WorkflowID string
	RunID      string
	Attempt    int32
}

func ExecutionInfo(ctx context.Context) ExecutionContext {
	info := sdkactivity.GetInfo(ctx)
	return ExecutionContext{ActivityID: info.ActivityID, WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID, Attempt: info.Attempt}
}
