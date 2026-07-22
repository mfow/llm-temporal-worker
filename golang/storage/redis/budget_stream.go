package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

// BudgetStreamEventKind is the bounded coordination vocabulary for the Redis
// budget Stream. Events invalidate local hints and wake rebuild waiters; they
// never authorize a request (the atomic budget Function remains authoritative).
type BudgetStreamEventKind string

const (
	BudgetEventReserve          BudgetStreamEventKind = "reserve"
	BudgetEventReconcile        BudgetStreamEventKind = "reconcile"
	BudgetEventRelease          BudgetStreamEventKind = "release"
	BudgetEventPolicyRefresh    BudgetStreamEventKind = "policy_refresh"
	BudgetEventHorizonAdvance   BudgetStreamEventKind = "horizon_advance"
	BudgetEventGenerationSwitch BudgetStreamEventKind = "generation_switch"
	BudgetEventDenial           BudgetStreamEventKind = "denial"
)

const (
	BudgetActiveGenerationSuffix = "budget:active-generation"
	BudgetEventsSuffix           = "budget:events"
	BudgetWorkersSuffix          = "budget:workers"
	MaxBudgetStreamEventBytes    = 8 << 10
)

var redisStreamIDPattern = regexp.MustCompile(`^[0-9]+-[0-9]+$`)

// BudgetStreamEvent contains only opaque digests and bounded accounting
// metadata. Raw tenant, operation, provider, and prompt identifiers must not
// enter Redis coordination values or observability output.
type BudgetStreamEvent struct {
	Schema        string                `json:"schema"`
	Kind          BudgetStreamEventKind `json:"kind"`
	GenerationID  BudgetGenerationID    `json:"generation_id"`
	OperationHash string                `json:"operation_hash,omitempty"`
	MemberHash    string                `json:"member_hash,omitempty"`
	Revision      int64                 `json:"revision"`
	NanoDelta     int64                 `json:"nano_delta"`
	OccurredAt    time.Time             `json:"occurred_at"`
}

const budgetStreamEventSchema = "budget-event/v1"

func (event BudgetStreamEvent) Validate() error {
	if event.Schema != budgetStreamEventSchema {
		return fmt.Errorf("unsupported budget stream event schema %q", event.Schema)
	}
	switch event.Kind {
	case BudgetEventReserve, BudgetEventReconcile, BudgetEventRelease, BudgetEventPolicyRefresh, BudgetEventHorizonAdvance, BudgetEventGenerationSwitch, BudgetEventDenial:
	default:
		return fmt.Errorf("unsupported budget stream event kind %q", event.Kind)
	}
	if err := validateOpaqueID("generation_id", string(event.GenerationID)); err != nil {
		return err
	}
	for name, value := range map[string]string{"operation_hash": event.OperationHash, "member_hash": event.MemberHash} {
		if value != "" && !sha256HexPattern.MatchString(value) {
			return fmt.Errorf("%s must be a lowercase SHA-256 digest", name)
		}
	}
	if event.Revision < 0 || event.NanoDelta < 0 || event.NanoDelta > int64(pricing.NanoUSDSafeLimit) {
		return errors.New("budget stream event counters must be non-negative")
	}
	if event.OccurredAt.IsZero() {
		return errors.New("budget stream event timestamp is required")
	}
	if event.Kind == BudgetEventReserve || event.Kind == BudgetEventReconcile || event.Kind == BudgetEventRelease {
		if event.OperationHash == "" || event.MemberHash == "" {
			return errors.New("accounting event requires operation and member digests")
		}
	}
	return nil
}

func (event BudgetStreamEvent) Marshal() ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("marshal budget stream event: %w", err)
	}
	if len(data) > MaxBudgetStreamEventBytes {
		return nil, fmt.Errorf("budget stream event exceeds %d bytes", MaxBudgetStreamEventBytes)
	}
	return data, nil
}

// BudgetGenerationPort is the explicit boundary used by readiness, adoption,
// and the future fenced bootstrap coordinator. It does not expose a PostgreSQL
// fallback: callers must prove an allowed rebuild condition before invoking a
// recovery implementation.
type BudgetGenerationPort interface {
	ActiveGeneration(context.Context) (ActiveBudgetGeneration, error)
	LoadManifest(context.Context, ActiveBudgetGeneration) (BudgetManifest, error)
	PublishGeneration(context.Context, BudgetManifest) (ActiveBudgetGeneration, error)
}

// BudgetEventPort is the broadcast coordination boundary. A cursor is a
// Redis Stream ID; each worker reads independently rather than using a shared
// consumer group.
type BudgetEventPort interface {
	Append(context.Context, BudgetStreamEvent) (string, error)
	Read(context.Context, string, int) ([]BudgetStreamRecord, error)
}

type BudgetStreamRecord struct {
	ID    string
	Event BudgetStreamEvent
}

// BudgetKeySpace exposes only the stable generation/Stream key families. The
// underlying HMAC secret remains private to keySpace, so callers cannot turn
// raw identifiers into Redis keys accidentally.
type BudgetKeySpace struct{ space keySpace }

func NewBudgetKeySpace(options KeyOptions) (BudgetKeySpace, error) {
	space, err := newKeySpace(options)
	if err != nil {
		return BudgetKeySpace{}, err
	}
	return BudgetKeySpace{space: space}, nil
}

func (keys BudgetKeySpace) ActiveGenerationKey() string {
	return keys.space.admissionPrefix() + BudgetActiveGenerationSuffix
}
func (keys BudgetKeySpace) EventsKey() string {
	return keys.space.admissionPrefix() + BudgetEventsSuffix
}
func (keys BudgetKeySpace) WorkersKey() string {
	return keys.space.admissionPrefix() + BudgetWorkersSuffix
}

func (keys BudgetKeySpace) ManifestKey(generation BudgetGenerationID) string {
	return keys.space.admissionPrefix() + "budget:g:" + keys.space.digest("budget-generation", string(generation)) + ":manifest"
}

// MemoryBudgetEventPort is a deterministic contract implementation for unit
// tests. Production uses a Redis Stream adapter; keeping this implementation in
// the same package lets conformance tests exercise the port without a server.
type MemoryBudgetEventPort struct {
	mu      sync.Mutex
	next    int64
	records []BudgetStreamRecord
}

var _ BudgetEventPort = (*MemoryBudgetEventPort)(nil)

func (port *MemoryBudgetEventPort) Append(ctx context.Context, event BudgetStreamEvent) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := event.Validate(); err != nil {
		return "", err
	}
	port.mu.Lock()
	defer port.mu.Unlock()
	port.next++
	id := fmt.Sprintf("%d-0", port.next)
	port.records = append(port.records, BudgetStreamRecord{ID: id, Event: event})
	return id, nil
}

func (port *MemoryBudgetEventPort) Read(ctx context.Context, cursor string, limit int) ([]BudgetStreamRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cursor != "" && !redisStreamIDPattern.MatchString(cursor) {
		return nil, fmt.Errorf("invalid Redis Stream cursor")
	}
	if limit <= 0 || limit > 10_000 {
		return nil, fmt.Errorf("stream read limit must be between 1 and 10000")
	}
	port.mu.Lock()
	defer port.mu.Unlock()
	var after int64
	if cursor != "" {
		_, err := fmt.Sscanf(cursor, "%d-0", &after)
		if err != nil {
			return nil, fmt.Errorf("invalid Redis Stream cursor")
		}
	}
	result := make([]BudgetStreamRecord, 0, limit)
	for _, record := range port.records {
		var sequence int64
		_, _ = fmt.Sscanf(record.ID, "%d-0", &sequence)
		if sequence <= after {
			continue
		}
		result = append(result, record)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}
