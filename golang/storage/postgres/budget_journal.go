package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mfow/llm-temporal-worker/golang/budget"
	"github.com/mfow/llm-temporal-worker/golang/pricing"
)

var (
	ErrBudgetJournalConflict           = errors.New("budget journal idempotency conflict")
	ErrBudgetJournalMissingReservation = errors.New("budget journal reservation is missing")
)

// BudgetJournal is the durable write port used after Redis accepts a budget
// decision. It intentionally has no active-budget read method: normal
// admission and completion must not query PostgreSQL budget tables.
type BudgetJournal interface {
	AppendReservation(context.Context, budget.ReservationEvent) (JournalRecord, error)
	AppendCompletion(context.Context, budget.CompletionEvent) (JournalRecord, error)
}

// JournalRecord identifies the append result. Existing is true when a retry
// presented the same operation/window/revision and event identity; projection
// writes are skipped in that case.
type JournalRecord struct {
	JournalID int64
	EventID   uuid.UUID
	Existing  bool
}

// BudgetJournalRepository implements the write-only journal port. The caller
// must have already acquired the matching Redis reservation; this repository
// never calls Redis or dispatches a provider operation.
type BudgetJournalRepository struct {
	Pool      *pgxpool.Pool
	Namespace Namespace
	Now       func() time.Time
}

var _ BudgetJournal = (*BudgetJournalRepository)(nil)

func (repository *BudgetJournalRepository) validate() error {
	if repository == nil || repository.Pool == nil {
		return errors.New("budget journal PostgreSQL pool is nil")
	}
	return repository.Namespace.Validate()
}

func (repository *BudgetJournalRepository) AppendReservation(ctx context.Context, event budget.ReservationEvent) (JournalRecord, error) {
	if err := event.Validate(); err != nil {
		return JournalRecord{}, err
	}
	return repository.append(ctx, journalInput{
		eventID: event.EventID, generationID: event.GenerationID, operationID: event.OperationID, windowID: event.WindowID,
		bucketStart: event.BucketStart, revision: event.ReservationRevision, kind: budget.JournalReserve,
		reservedIncrease: event.AmountUSD, costStatus: budget.CostPending, occurredAt: event.OccurredAt,
		reserve: true,
	})
}

func (repository *BudgetJournalRepository) AppendCompletion(ctx context.Context, event budget.CompletionEvent) (JournalRecord, error) {
	if err := event.Validate(); err != nil {
		return JournalRecord{}, err
	}
	return repository.append(ctx, journalInput{
		eventID: event.EventID, generationID: event.GenerationID, operationID: event.OperationID, windowID: event.WindowID,
		bucketStart: event.BucketStart, revision: event.ReservationRevision, kind: event.Kind,
		reservedDecrease: event.ReservedDecreaseUSD, accountedIncrease: event.AccountedIncreaseUSD,
		accountedDecrease: event.AccountedDecreaseUSD, actual: event.ActualCostUSD, costStatus: event.CostStatus,
		unknownReason: event.UnknownReasonCode, occurredAt: event.OccurredAt,
	})
}

type journalInput struct {
	eventID, generationID, operationID, windowID string
	bucketStart                                  time.Time
	revision                                     int
	kind                                         budget.JournalEventKind
	reservedIncrease, reservedDecrease           pricing.USD
	accountedIncrease, accountedDecrease         pricing.USD
	actual                                       *pricing.USD
	costStatus                                   budget.ActualCostStatus
	unknownReason                                string
	occurredAt                                   time.Time
	reserve                                      bool
}

func (repository *BudgetJournalRepository) append(ctx context.Context, input journalInput) (result JournalRecord, err error) {
	if err := repository.validate(); err != nil {
		return result, err
	}
	eventID, generationID, operationID, windowID, err := parseJournalUUIDs(input)
	if err != nil {
		return result, err
	}
	journalTable, err := repository.Namespace.Render("budget_journal_events")
	if err != nil {
		return result, err
	}
	bucketTable, err := repository.Namespace.Render("budget_buckets")
	if err != nil {
		return result, err
	}
	reservationTable, err := repository.Namespace.Render("operation_budget_reservations")
	if err != nil {
		return result, err
	}
	reservedIncrease, err := EncodeUSD(input.reservedIncrease)
	if err != nil {
		return result, fmt.Errorf("encode reserved increase: %w", err)
	}
	reservedDecrease, err := EncodeUSD(input.reservedDecrease)
	if err != nil {
		return result, fmt.Errorf("encode reserved decrease: %w", err)
	}
	accountedIncrease, err := EncodeUSD(input.accountedIncrease)
	if err != nil {
		return result, fmt.Errorf("encode accounted increase: %w", err)
	}
	accountedDecrease, err := EncodeUSD(input.accountedDecrease)
	if err != nil {
		return result, fmt.Errorf("encode accounted decrease: %w", err)
	}
	var actual any
	if input.actual != nil {
		actual, err = EncodeUSD(*input.actual)
		if err != nil {
			return result, fmt.Errorf("encode actual cost: %w", err)
		}
	}
	now := input.occurredAt
	if repository.Now != nil {
		now = repository.Now()
	}
	err = WithTransaction(ctx, repository.Pool, func(ctx context.Context, tx pgx.Tx) error {
		var journalID int64
		insertErr := tx.QueryRow(ctx, "INSERT INTO "+journalTable+
			" (event_id, redis_generation_id, operation_id, window_id, bucket_start, reservation_revision, event_kind,"+
			" reserved_increase_usd, reserved_decrease_usd, accounted_increase_usd, accounted_decrease_usd,"+
			" actual_cost_usd, actual_cost_status, actual_cost_unknown_reason_code, occurred_at)"+
			" VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)"+
			" ON CONFLICT (operation_id, window_id, reservation_revision) DO NOTHING RETURNING journal_id",
			eventID, generationID, operationID, windowID, input.bucketStart.UTC(), input.revision, string(input.kind),
			reservedIncrease, reservedDecrease, accountedIncrease, accountedDecrease, actual, string(input.costStatus), input.unknownReason, input.occurredAt.UTC()).Scan(&journalID)
		if errors.Is(insertErr, pgx.ErrNoRows) {
			var existingID int64
			var existingEvent uuid.UUID
			if err := tx.QueryRow(ctx, "SELECT journal_id, event_id FROM "+journalTable+
				" WHERE operation_id = $1 AND window_id = $2 AND reservation_revision = $3",
				operationID, windowID, input.revision).Scan(&existingID, &existingEvent); err != nil {
				return redactPostgresError(fmt.Errorf("resolve budget journal idempotency: %w", err))
			}
			if existingEvent != eventID {
				return ErrBudgetJournalConflict
			}
			result = JournalRecord{JournalID: existingID, EventID: existingEvent, Existing: true}
			return nil
		}
		if insertErr != nil {
			return redactPostgresError(fmt.Errorf("append budget journal event: %w", insertErr))
		}
		result = JournalRecord{JournalID: journalID, EventID: eventID}
		if _, err := tx.Exec(ctx, "INSERT INTO "+bucketTable+
			" (window_id, bucket_start, reserved_cost_usd, accounted_cost_usd, last_journal_id)"+
			" VALUES ($1,$2,$3,$4,$5) ON CONFLICT (window_id, bucket_start) DO UPDATE SET"+
			" reserved_cost_usd = "+bucketTable+".reserved_cost_usd + EXCLUDED.reserved_cost_usd - $6,"+
			" accounted_cost_usd = "+bucketTable+".accounted_cost_usd + EXCLUDED.accounted_cost_usd - $7,"+
			" last_journal_id = EXCLUDED.last_journal_id, updated_at = clock_timestamp()",
			windowID, input.bucketStart.UTC(), reservedIncrease, accountedIncrease, journalID, reservedDecrease, accountedDecrease); err != nil {
			return redactPostgresError(fmt.Errorf("update budget bucket projection: %w", err))
		}
		if input.reserve {
			tag, err := tx.Exec(ctx, "INSERT INTO "+reservationTable+
				" (operation_id, window_id, bucket_start, state, reserved_cost_usd, actual_cost_status,"+
				" budget_charge_usd, budget_charge_basis, reservation_revision, last_journal_id, created_at)"+
				" VALUES ($1,$2,$3,'reserved',$4,'pending',0,'reserved',$5,$6,$7) ON CONFLICT (operation_id, window_id) DO NOTHING",
				operationID, windowID, input.bucketStart.UTC(), reservedIncrease, input.revision, journalID, now.UTC())
			if err != nil {
				return redactPostgresError(fmt.Errorf("insert budget reservation projection: %w", err))
			}
			if tag.RowsAffected() != 1 {
				return ErrBudgetJournalConflict
			}
			return nil
		}
		state, basis, finalizedAt := completionProjection(input)
		tag, err := tx.Exec(ctx, "UPDATE "+reservationTable+" SET state=$3, actual_cost_usd=$4, actual_cost_status=$5,"+
			" actual_cost_unknown_reason_code=$6, budget_charge_usd=CASE WHEN $7 IS NULL THEN reserved_cost_usd ELSE $7 END, budget_charge_basis=$8,"+
			" reservation_revision=$9, last_journal_id=$10, finalized_at=$11"+
			" WHERE operation_id=$1 AND window_id=$2", operationID, windowID, state, actual, string(input.costStatus), input.unknownReason, journalCharge(input), basis, input.revision, journalID, finalizedAt)
		if err != nil {
			return redactPostgresError(fmt.Errorf("update budget reservation projection: %w", err))
		}
		if tag.RowsAffected() != 1 {
			return ErrBudgetJournalMissingReservation
		}
		return nil
	})
	if err != nil {
		return JournalRecord{}, err
	}
	return result, nil
}

func parseJournalUUIDs(input journalInput) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, error) {
	eventID, err := uuid.Parse(input.eventID)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, fmt.Errorf("event ID: %w", err)
	}
	generationID, err := uuid.Parse(input.generationID)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, fmt.Errorf("generation ID: %w", err)
	}
	operationID, err := uuid.Parse(input.operationID)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, fmt.Errorf("operation ID: %w", err)
	}
	windowID, err := uuid.Parse(input.windowID)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, fmt.Errorf("window ID: %w", err)
	}
	return eventID, generationID, operationID, windowID, nil
}

func completionProjection(input journalInput) (state, basis string, finalizedAt any) {
	switch input.kind {
	case budget.JournalRetainAmbiguous:
		return "retained_ambiguous", "retained_bound", input.occurredAt.UTC()
	case budget.JournalRelease:
		return "released", "released", input.occurredAt.UTC()
	case budget.JournalFinalizeUnknown:
		return "finalized", "retained_bound", input.occurredAt.UTC()
	default:
		return "finalized", "exact_actual", input.occurredAt.UTC()
	}
}

// journalCharge is passed as a nullable numeric value. Unknown/ambiguous
// transitions use NULL so SQL retains the original reservation bound; exact
// and release transitions pass their explicit actual amount.
func journalCharge(input journalInput) any {
	if input.kind == budget.JournalRetainAmbiguous || input.kind == budget.JournalFinalizeUnknown {
		return nil
	}
	if input.actual == nil {
		return "0.000000000000000000"
	}
	value, _ := EncodeUSD(*input.actual)
	return value
}
