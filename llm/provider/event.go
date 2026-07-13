package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mfow/llm-temporal-worker/llm"
)

type Event interface{ event() }

type OutputStarted struct{ Index int }

func (OutputStarted) event() {}

type TextDelta struct {
	Index int
	Text  string
}

func (TextDelta) event() {}

type ToolArgumentsDelta struct {
	Index    int
	CallID   string
	Name     string
	Fragment string
}

func (ToolArgumentsDelta) event() {}

type ReasoningDelta struct {
	Index  int
	Opaque []byte
}

func (ReasoningDelta) event() {}

type UsageUpdated struct{ Usage llm.Usage }

func (UsageUpdated) event() {}

type OutputFinished struct {
	Index int
	Item  llm.Item
}

func (OutputFinished) event() {}

type StreamCompleted struct{ Response llm.Response }

func (StreamCompleted) event() {}

type StreamErrored struct{ Err error }

func (StreamErrored) event() {}

type StreamFailed = StreamErrored

type outputState struct {
	text      strings.Builder
	arguments strings.Builder
	callID    string
	name      string
	finished  bool
}

type Assembler struct {
	operationKey string
	nextIndex    int
	active       map[int]*outputState
	outputs      []llm.Item
	usage        llm.Usage
	terminal     bool
	result       llm.Response
	terminalErr  error
}

func NewAssembler(operationKey string) *Assembler {
	return &Assembler{operationKey: operationKey, active: make(map[int]*outputState)}
}

func (assembler *Assembler) Add(event Event) error {
	if assembler == nil {
		return fmt.Errorf("event assembler is nil")
	}
	if assembler.terminal {
		return fmt.Errorf("stream already has a terminal event")
	}
	switch event := event.(type) {
	case OutputStarted:
		if event.Index < 0 || event.Index != assembler.nextIndex {
			return fmt.Errorf("output start index %d is out of order", event.Index)
		}
		assembler.active[event.Index] = &outputState{}
		assembler.nextIndex++
	case TextDelta:
		state, err := assembler.state(event.Index)
		if err != nil {
			return err
		}
		if !utf8.ValidString(event.Text) {
			return fmt.Errorf("text delta at index %d is not valid UTF-8", event.Index)
		}
		state.text.WriteString(event.Text)
	case ToolArgumentsDelta:
		state, err := assembler.state(event.Index)
		if err != nil {
			return err
		}
		if !utf8.ValidString(event.Fragment) {
			return fmt.Errorf("tool argument delta at index %d is not valid UTF-8", event.Index)
		}
		if state.callID != "" && event.CallID != "" && state.callID != event.CallID {
			return fmt.Errorf("tool call ID changed at index %d", event.Index)
		}
		if state.name != "" && event.Name != "" && state.name != event.Name {
			return fmt.Errorf("tool name changed at index %d", event.Index)
		}
		if event.CallID != "" {
			state.callID = event.CallID
		}
		if event.Name != "" {
			state.name = event.Name
		}
		state.arguments.WriteString(event.Fragment)
	case ReasoningDelta:
		state, err := assembler.state(event.Index)
		if err != nil {
			return err
		}
		state.text.Write(event.Opaque)
	case OutputFinished:
		state, err := assembler.state(event.Index)
		if err != nil {
			return err
		}
		if state.finished {
			return fmt.Errorf("output index %d finished twice", event.Index)
		}
		item := event.Item
		if item == nil {
			item, err = state.item()
			if err != nil {
				return err
			}
		}
		state.finished = true
		assembler.outputs = append(assembler.outputs, item)
		delete(assembler.active, event.Index)
	case UsageUpdated:
		assembler.usage = event.Usage
	case StreamCompleted:
		if len(assembler.active) > 0 {
			return fmt.Errorf("stream completed with unfinished output")
		}
		response := event.Response
		if response.APIVersion == "" {
			response.APIVersion = llm.APIVersion
		}
		if response.OperationKey == "" {
			response.OperationKey = assembler.operationKey
		}
		if response.OperationKey == "" {
			return fmt.Errorf("stream response operation key is empty")
		}
		if response.Status == "" {
			response.Status = llm.ResponseStatusCompleted
		}
		if !response.Status.Valid() {
			return fmt.Errorf("stream response status %q is invalid", response.Status)
		}
		if response.Output == nil {
			response.Output = append([]llm.Item(nil), assembler.outputs...)
		}
		if usageEmpty(response.Usage) {
			response.Usage = assembler.usage
		}
		assembler.result = response
		assembler.terminal = true
	case StreamErrored:
		assembler.terminal = true
		assembler.terminalErr = event.Err
		if assembler.terminalErr == nil {
			assembler.terminalErr = fmt.Errorf("provider stream failed")
		}
	default:
		return fmt.Errorf("unsupported provider event %T", event)
	}
	return nil
}

func usageEmpty(usage llm.Usage) bool {
	return usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.ReasoningTokens == 0 && usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 && len(usage.ProviderRaw) == 0
}

func (assembler *Assembler) state(index int) (*outputState, error) {
	if index < 0 {
		return nil, fmt.Errorf("output index %d is invalid", index)
	}
	state, ok := assembler.active[index]
	if !ok {
		return nil, fmt.Errorf("output index %d was not started or already finished", index)
	}
	return state, nil
}

func (state *outputState) item() (llm.Item, error) {
	if state.arguments.Len() > 0 {
		arguments := []byte(state.arguments.String())
		if !json.Valid(arguments) {
			return nil, fmt.Errorf("tool argument stream is incomplete JSON")
		}
		if state.callID == "" || state.name == "" {
			return nil, fmt.Errorf("tool argument stream is missing call ID or name")
		}
		return llm.ToolCall{ID: state.callID, Name: state.name, Arguments: append(json.RawMessage(nil), arguments...)}, nil
	}
	return llm.Message{Actor: llm.ActorModel, Content: []llm.Part{llm.TextPart{Text: state.text.String()}}}, nil
}

func (assembler *Assembler) Result() (llm.Response, error) {
	if assembler == nil {
		return llm.Response{}, fmt.Errorf("event assembler is nil")
	}
	if !assembler.terminal {
		return llm.Response{}, fmt.Errorf("stream has no terminal event")
	}
	if assembler.terminalErr != nil {
		return llm.Response{}, assembler.terminalErr
	}
	return assembler.result, nil
}
