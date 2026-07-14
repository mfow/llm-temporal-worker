package openairesponses

import (
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3/responses"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

func liftResponse(call provider.Call, response *responses.Response, requestID string) (llm.Response, error) {
	if response == nil {
		return llm.Response{}, invalidResponseError(call, requestID, "provider returned an empty response")
	}
	actual, err := serviceClassForTier(response.ServiceTier)
	if err != nil {
		mapped := invalidResponseError(call, requestID, err.Error())
		mapped.Provider.ResponseID = response.ID
		return llm.Response{}, mapped
	}
	output, hasToolCalls, hasRefusal, err := liftOutput(response.Output)
	if err != nil {
		mapped := invalidResponseError(call, requestID, err.Error())
		mapped.Provider.ResponseID = response.ID
		return llm.Response{}, mapped
	}
	status, err := liftStatus(response.Status, response.IncompleteDetails.Reason, hasToolCalls, hasRefusal)
	if err != nil {
		mapped := invalidResponseError(call, requestID, err.Error())
		mapped.Provider.ResponseID = response.ID
		return llm.Response{}, mapped
	}
	providerRaw := map[string]json.RawMessage{}
	ids := make([]string, 0, len(response.Output))
	for _, item := range response.Output {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) > 0 {
		encoded, _ := json.Marshal(ids)
		providerRaw["output_item_ids"] = encoded
	}
	providerFacts := llm.ProviderFacts{
		ResponseID:   response.ID,
		RequestID:    requestID,
		FinishReason: string(response.Status),
		Raw:          providerRaw,
	}
	usage := llm.Usage{
		InputTokens:      response.Usage.InputTokens,
		OutputTokens:     response.Usage.OutputTokens,
		ReasoningTokens:  response.Usage.OutputTokensDetails.ReasoningTokens,
		CacheReadTokens:  response.Usage.InputTokensDetails.CachedTokens,
		CacheWriteTokens: response.Usage.InputTokensDetails.CacheWriteTokens,
	}
	if response.Usage.TotalTokens > 0 {
		encoded, _ := json.Marshal(response.Usage.TotalTokens)
		usage.ProviderRaw = map[string]json.RawMessage{"total_tokens": encoded}
	}
	service := llm.ServiceFacts{
		Requested:     call.ServiceClass,
		Attempted:     call.ServiceClass,
		Actual:        actual,
		ProviderValue: string(response.ServiceTier),
		FallbackIndex: 0,
	}
	result := llm.Response{
		APIVersion:   llm.APIVersion,
		OperationKey: call.OperationKey,
		Status:       status,
		Output:       output,
		Route: llm.RouteFacts{
			EndpointID:     call.EndpointID,
			APIFamily:      string(provider.FamilyOpenAIResponses),
			RequestedModel: call.Model,
			ResolvedModel:  string(response.Model),
		},
		Service:      service,
		Usage:        usage,
		Provider:     providerFacts,
		Continuation: continuationForResponse(call, response),
	}
	return result, nil
}

func serviceClassForTier(tier responses.ResponseServiceTier) (*llm.ServiceClass, error) {
	var class llm.ServiceClass
	switch tier {
	case responses.ResponseServiceTierFlex:
		class = llm.ServiceClassEconomy
	case responses.ResponseServiceTierDefault:
		class = llm.ServiceClassStandard
	case responses.ResponseServiceTierPriority:
		class = llm.ServiceClassPriority
	case "":
		return nil, fmt.Errorf("provider response omitted service tier")
	default:
		return nil, fmt.Errorf("provider returned unsupported service tier %q", tier)
	}
	return &class, nil
}

func liftStatus(status responses.ResponseStatus, incompleteReason string, hasToolCalls, hasRefusal bool) (llm.ResponseStatus, error) {
	switch status {
	case responses.ResponseStatusCompleted:
		if hasToolCalls {
			return llm.ResponseStatusToolCalls, nil
		}
		if hasRefusal {
			return llm.ResponseStatusRefused, nil
		}
		return llm.ResponseStatusCompleted, nil
	case responses.ResponseStatusIncomplete:
		if incompleteReason == "content_filter" {
			return llm.ResponseStatusContentFiltered, nil
		}
		return llm.ResponseStatusLength, nil
	case responses.ResponseStatusFailed, responses.ResponseStatusInProgress, responses.ResponseStatusCancelled, responses.ResponseStatusQueued:
		return "", fmt.Errorf("provider response status %q is not a terminal semantic response", status)
	default:
		return "", fmt.Errorf("provider returned unknown response status %q", status)
	}
}

func liftOutput(items []responses.ResponseOutputItemUnion) ([]llm.Item, bool, bool, error) {
	output := make([]llm.Item, 0, len(items))
	toolCalls := false
	refusal := false
	for index, item := range items {
		switch item.Type {
		case "message":
			message := item.AsMessage()
			content := make([]llm.Part, 0, len(message.Content))
			for contentIndex, part := range message.Content {
				switch part.Type {
				case "output_text":
					content = append(content, llm.TextPart{Text: part.Text})
				case "refusal":
					refusal = true
					content = append(content, llm.RefusalPart{Text: part.Refusal, ProviderCode: "openai.refusal"})
				default:
					return nil, false, false, fmt.Errorf("output item %d content %d has unsupported type %q", index, contentIndex, part.Type)
				}
			}
			output = append(output, llm.Message{Actor: llm.ActorModel, Content: content})
		case "function_call":
			call := item.AsFunctionCall()
			if call.CallID == "" {
				call.CallID = call.ID
			}
			if call.CallID == "" || call.Name == "" {
				return nil, false, false, fmt.Errorf("function call output item %d is missing call ID or name", index)
			}
			if !json.Valid([]byte(call.Arguments)) {
				return nil, false, false, fmt.Errorf("function call %q arguments are invalid JSON", call.CallID)
			}
			toolCalls = true
			output = append(output, llm.ToolCall{ID: call.CallID, Name: call.Name, Arguments: []byte(call.Arguments)})
		case "function_call_output":
			callOutput := item.AsFunctionCallOutput()
			if callOutput.CallID == "" {
				return nil, false, false, fmt.Errorf("function call output item %d is missing call ID", index)
			}
			text := callOutput.Output.OfString
			if text == "" && len(callOutput.Output.OfOutputContentList) > 0 {
				encoded, err := json.Marshal(callOutput.Output.OfOutputContentList)
				if err != nil {
					return nil, false, false, err
				}
				text = string(encoded)
			}
			output = append(output, llm.ToolResult{CallID: callOutput.CallID, Content: []llm.Part{llm.TextPart{Text: text}}})
		case "reasoning":
			reasoning := item.AsReasoning()
			raw := []byte(reasoning.RawJSON())
			if len(raw) == 0 {
				raw = []byte(item.RawJSON())
			}
			output = append(output, llm.ProviderState{Provider: "openai", EndpointFamily: "responses", MediaType: "application/vnd.openai.reasoning+json", Opaque: raw})
		default:
			return nil, false, false, fmt.Errorf("output item %d has unsupported type %q", index, item.Type)
		}
	}
	return output, toolCalls, refusal, nil
}

func continuationForResponse(call provider.Call, response *responses.Response) *llm.Continuation {
	if response.ID == "" || !statefulContinuationEnabled(call) {
		return nil
	}
	return &llm.Continuation{
		Handle:     "openai-responses:" + response.ID,
		EndpointID: call.EndpointID,
		Model:      string(response.Model),
		Pinned:     true,
		ProviderStates: []llm.ProviderState{{
			Provider:       "openai",
			EndpointFamily: "responses",
			MediaType:      "application/vnd.openai.response+json",
			Opaque:         []byte(response.ID),
		}},
	}
}

func statefulContinuationEnabled(call provider.Call) bool {
	params, ok := call.SDKParams.(responses.ResponseNewParams)
	if !ok {
		if pointer, pointerOK := call.SDKParams.(*responses.ResponseNewParams); pointerOK && pointer != nil {
			params = *pointer
			ok = true
		}
	}
	if !ok {
		return true
	}
	return !params.Store.Valid() || params.Store.Value
}

func invalidResponseError(call provider.Call, requestID, message string) *provider.Error {
	mapped := provider.NewError(provider.CodeProviderInvalidResponse, provider.PhaseLift, provider.DispatchAccepted, provider.RetryNever, message)
	mapped.Provider.RequestID = requestID
	mapped.OperationID = call.OperationKey
	return mapped
}
