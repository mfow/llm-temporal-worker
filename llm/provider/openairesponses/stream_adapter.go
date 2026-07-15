package openairesponses

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/mfow/llm-temporal-worker/llm/provider"
)

// responseStream is the small portion of the official SDK stream used at the
// adapter boundary. Keeping it narrow makes body ownership explicit here.
type responseStream interface {
	Next() bool
	Current() responses.ResponseStreamEventUnion
	Err() error
	Close() error
}

// OpenStream opens a direct OpenAI Responses SSE stream through the official
// SDK and returns provider-neutral events. Azure clients deliberately remain
// gated because their streaming availability is endpoint- and model-specific.
func (adapter *Adapter) OpenStream(ctx context.Context, call provider.Call, observer provider.Observer) (provider.StreamResult, error) {
	if adapter == nil {
		return provider.StreamResult{}, dispatchError("adapter is nil", provider.DispatchNotDispatched)
	}
	if adapter.client == nil {
		return provider.StreamResult{}, dispatchError("adapter client is nil", provider.DispatchNotDispatched)
	}
	if adapter.client.profile != endpointProfileDirect {
		return provider.StreamResult{}, streamingUnsupportedError(adapter.endpointID)
	}
	if err := ctx.Err(); err != nil {
		return provider.StreamResult{}, dispatchContextError(err)
	}
	if call.Family != provider.FamilyOpenAIResponses || call.EndpointID != adapter.endpointID {
		return provider.StreamResult{}, dispatchError("call does not belong to this adapter", provider.DispatchNotDispatched)
	}
	params, ok := call.SDKParams.(responses.ResponseNewParams)
	if !ok {
		if pointer, pointerOK := call.SDKParams.(*responses.ResponseNewParams); pointerOK && pointer != nil {
			params = *pointer
			ok = true
		}
	}
	if !ok {
		return provider.StreamResult{}, dispatchError("call SDK parameters have unexpected type", provider.DispatchNotDispatched)
	}
	if observer == nil {
		observer = provider.NopObserver{}
	}
	callContext, egressOutcome := provider.WithEgressOutcome(ctx)
	if err := observer.BeforePossibleWrite(callContext); err != nil {
		return provider.StreamResult{}, dispatchObserverError(err, provider.DispatchNotDispatched)
	}
	var rawResponse *http.Response
	stream := adapter.client.sdk.Responses.NewStreaming(callContext, params, option.WithResponseInto(&rawResponse))
	if rawResponse != nil && provider.IsRedirectStatus(rawResponse.StatusCode) {
		_ = stream.Close()
		return provider.StreamResult{}, provider.WithEndpointID(provider.NewRedirectResponseError(rawResponse.StatusCode), adapter.endpointID)
	}
	if err := stream.Err(); err != nil {
		_ = stream.Close()
		if mapped := provider.ClassifyEgressOutcome(egressOutcome, err); mapped != nil {
			return provider.StreamResult{}, provider.WithEndpointID(mapped, adapter.endpointID)
		}
		return provider.StreamResult{}, provider.WithEndpointID(mapError(err), adapter.endpointID)
	}
	metadata := provider.ResponseMetadata{}
	if rawResponse != nil {
		metadata.Status = rawResponse.StatusCode
		metadata.RequestID = rawResponse.Header.Get("x-request-id")
	}
	return provider.StreamResult{
		Source: &responsesEventSource{
			stream:    stream,
			call:      call,
			requestID: metadata.RequestID,
			toolCalls: make(map[int]streamToolCall),
		},
		Metadata: metadata,
		Dispatch: provider.DispatchAccepted,
	}, nil
}

func streamingUnsupportedError(endpointID string) *provider.Error {
	mapped := provider.NewError(provider.CodeUnsupportedCapability, provider.PhaseStream, provider.DispatchNotDispatched, provider.RetryNever, "streaming is not enabled for this endpoint profile")
	return provider.WithEndpointID(mapped, endpointID)
}

type streamToolCall struct {
	callID string
	name   string
}

type responsesEventSource struct {
	stream    responseStream
	call      provider.Call
	requestID string
	toolCalls map[int]streamToolCall
	terminal  bool
	closeOnce sync.Once
	closeErr  error
}

func (source *responsesEventSource) Next(ctx context.Context) (provider.Event, error) {
	if source == nil || source.stream == nil {
		return nil, fmt.Errorf("openai responses: stream source is unavailable")
	}
	if source.terminal {
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, source.streamError(err)
	}
	for source.stream.Next() {
		event, terminal, err := source.decode(source.stream.Current())
		if err != nil {
			return nil, err
		}
		if event == nil {
			continue
		}
		if terminal {
			source.terminal = true
		}
		return event, nil
	}
	if err := source.stream.Err(); err != nil {
		return nil, source.streamError(err)
	}
	return nil, io.EOF
}

func (source *responsesEventSource) Close() error {
	if source == nil || source.stream == nil {
		return nil
	}
	source.closeOnce.Do(func() { source.closeErr = source.stream.Close() })
	return source.closeErr
}

func (source *responsesEventSource) decode(event responses.ResponseStreamEventUnion) (provider.Event, bool, error) {
	switch event.Type {
	case "response.output_item.added":
		index, err := source.outputIndex(event.Type, event.OutputIndex)
		if err != nil {
			return nil, false, err
		}
		if event.Item.Type == "function_call" {
			call := event.Item.AsFunctionCall()
			if call.CallID == "" {
				call.CallID = call.ID
			}
			if call.CallID == "" || call.Name == "" {
				return nil, false, source.invalidResponse("function call stream item is missing call ID or name")
			}
			source.toolCalls[index] = streamToolCall{callID: call.CallID, name: call.Name}
		}
		return provider.OutputStarted{Index: index}, false, nil
	case "response.output_text.delta":
		index, err := source.outputIndex(event.Type, event.OutputIndex)
		if err != nil {
			return nil, false, err
		}
		return provider.TextDelta{Index: index, Text: event.Delta}, false, nil
	case "response.function_call_arguments.delta":
		index, err := source.outputIndex(event.Type, event.OutputIndex)
		if err != nil {
			return nil, false, err
		}
		call, ok := source.toolCalls[index]
		if !ok || call.callID == "" || call.name == "" {
			return nil, false, source.invalidResponse("function call argument delta has no known call identity")
		}
		return provider.ToolArgumentsDelta{Index: index, CallID: call.callID, Name: call.name, Fragment: event.Delta}, false, nil
	case "response.output_item.done":
		index, err := source.outputIndex(event.Type, event.OutputIndex)
		if err != nil {
			return nil, false, err
		}
		items, _, _, err := liftOutput([]responses.ResponseOutputItemUnion{event.Item})
		if err != nil {
			return nil, false, source.invalidResponse(fmt.Sprintf("stream output item could not be lifted: %v", err))
		}
		if len(items) != 1 {
			return nil, false, source.invalidResponse("stream output item produced an invalid lifted item count")
		}
		delete(source.toolCalls, index)
		return provider.OutputFinished{Index: index, Item: items[0]}, false, nil
	case "response.completed", "response.incomplete":
		lifted, err := liftResponse(source.call, &event.Response, source.requestID)
		if err != nil {
			return nil, false, err
		}
		return provider.StreamCompleted{Response: lifted}, true, nil
	case "response.failed", "error":
		return provider.StreamErrored{Err: source.terminalError()}, true, nil
	case "response.created", "response.in_progress", "response.content_part.added", "response.content_part.done", "response.output_text.done", "response.function_call_arguments.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_text.delta", "response.reasoning_text.done", "response.refusal.delta", "response.refusal.done":
		// These transport markers do not add a public event beyond the output
		// item lifecycle or the completed semantic response.
		return nil, false, nil
	default:
		return nil, false, source.invalidResponse(fmt.Sprintf("unsupported Responses stream event %q", event.Type))
	}
}

func (source *responsesEventSource) outputIndex(eventType string, value int64) (int, error) {
	if value < 0 || int64(int(value)) != value {
		return 0, source.invalidResponse(fmt.Sprintf("Responses stream event %q has invalid output index", eventType))
	}
	return int(value), nil
}

func (source *responsesEventSource) invalidResponse(message string) *provider.Error {
	return invalidResponseError(source.call, source.requestID, message)
}

func (source *responsesEventSource) terminalError() *provider.Error {
	mapped := provider.NewError(provider.CodeProviderUnavailable, provider.PhaseStream, provider.DispatchAccepted, provider.RetryNever, "OpenAI Responses stream did not complete")
	mapped.OperationID = source.call.OperationKey
	mapped.Provider.RequestID = source.requestID
	return provider.WithEndpointID(mapped, source.call.EndpointID)
}

func (source *responsesEventSource) streamError(err error) *provider.Error {
	code := provider.CodeProviderUnavailable
	message := "OpenAI Responses stream failed"
	if errors.Is(err, context.Canceled) {
		code = provider.CodeCanceled
		message = "OpenAI Responses stream canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = provider.CodeDeadlineExceeded
		message = "OpenAI Responses stream deadline exceeded"
	}
	mapped := provider.NewError(code, provider.PhaseStream, provider.DispatchAccepted, provider.RetryNever, message)
	mapped.Cause = err
	mapped.OperationID = source.call.OperationKey
	mapped.Provider.RequestID = source.requestID
	return provider.WithEndpointID(mapped, source.call.EndpointID)
}
