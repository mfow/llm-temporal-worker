package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/mfow/llm-temporal-worker/golang/llm"
)

// CursorCodec signs typed query positions.  A cursor is bound to the full
// caller scope, the canonical filter, and the result snapshot horizon; it is
// therefore not transferable between tenants, filters, or snapshots.
type CursorCodec struct {
	Key         []byte
	TTL         time.Duration
	Clock       func() time.Time
	MaxPosition int
}

type BoundCursorClaims struct {
	Kind         llm.QueryKind
	ScopeDigest  string
	FilterDigest string
	Position     string
	Horizon      time.Time
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

type boundCursorEnvelope struct {
	Kind         llm.QueryKind `json:"kind"`
	ScopeDigest  string        `json:"scope_digest"`
	FilterDigest string        `json:"filter_digest"`
	Position     string        `json:"position"`
	Horizon      int64         `json:"horizon"`
	IssuedAt     int64         `json:"issued_at"`
	ExpiresAt    int64         `json:"expires_at"`
}

func (codec *CursorCodec) Sign(request QueryRequest, position string, horizon, issuedAt time.Time) (QueryCursor, error) {
	if codec == nil || len(codec.Key) == 0 || request.Filter == nil || position == "" {
		return "", ErrQueryCursor
	}
	max := codec.MaxPosition
	if max <= 0 {
		max = 128
	}
	if len(position) > max || request.Filter.queryKind() != request.Kind || horizon.IsZero() || issuedAt.IsZero() {
		return "", ErrQueryCursor
	}
	issuedAt = issuedAt.UTC()
	horizon = horizon.UTC()
	ttl := codec.TTL
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 15 * time.Minute
	}
	scopeDigest, filterDigest, err := cursorDigests(request)
	if err != nil {
		return "", ErrQueryCursor
	}
	envelope := boundCursorEnvelope{Kind: request.Kind, ScopeDigest: scopeDigest, FilterDigest: filterDigest, Position: position, Horizon: horizon.Unix(), IssuedAt: issuedAt.Unix(), ExpiresAt: issuedAt.Add(ttl).Unix()}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", ErrQueryCursor
	}
	signature := hmac.New(sha256.New, codec.Key)
	_, _ = signature.Write(payload)
	encode := base64.RawURLEncoding
	token := encode.EncodeToString(payload) + "." + encode.EncodeToString(signature.Sum(nil))
	if len(token) > 512 {
		return "", ErrQueryCursor
	}
	return QueryCursor(token), nil
}

func (codec *CursorCodec) Decode(request QueryRequest, token QueryCursor, now time.Time) (BoundCursorClaims, error) {
	var claims BoundCursorClaims
	if codec == nil || len(codec.Key) == 0 || len(token) == 0 || len(token) > 512 || request.Filter == nil {
		return claims, ErrQueryCursor
	}
	parts := strings.Split(string(token), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return claims, ErrQueryCursor
	}
	decode := base64.RawURLEncoding
	payload, err := decode.DecodeString(parts[0])
	if err != nil {
		return claims, ErrQueryCursor
	}
	provided, err := decode.DecodeString(parts[1])
	if err != nil {
		return claims, ErrQueryCursor
	}
	signature := hmac.New(sha256.New, codec.Key)
	_, _ = signature.Write(payload)
	if !hmac.Equal(provided, signature.Sum(nil)) {
		return claims, ErrQueryCursor
	}
	var envelope boundCursorEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope.Kind != request.Kind || envelope.Position == "" {
		return claims, ErrQueryCursor
	}
	max := codec.MaxPosition
	if max <= 0 {
		max = 128
	}
	if len(envelope.Position) > max {
		return claims, ErrQueryCursor
	}
	scopeDigest, filterDigest, err := cursorDigests(request)
	if err != nil || envelope.ScopeDigest != scopeDigest || envelope.FilterDigest != filterDigest {
		return claims, ErrQueryCursor
	}
	now = now.UTC()
	if now.IsZero() || envelope.IssuedAt <= 0 || envelope.ExpiresAt <= envelope.IssuedAt || envelope.Horizon <= 0 || envelope.IssuedAt > now.Add(2*time.Minute).Unix() || envelope.ExpiresAt <= now.Unix() {
		return claims, ErrQueryCursor
	}
	return BoundCursorClaims{Kind: envelope.Kind, ScopeDigest: envelope.ScopeDigest, FilterDigest: envelope.FilterDigest, Position: envelope.Position, Horizon: time.Unix(envelope.Horizon, 0).UTC(), IssuedAt: time.Unix(envelope.IssuedAt, 0).UTC(), ExpiresAt: time.Unix(envelope.ExpiresAt, 0).UTC()}, nil
}

func cursorDigests(request QueryRequest) (string, string, error) {
	if request.Filter == nil || request.Filter.queryKind() != request.Kind {
		return "", "", errors.New("query filter kind mismatch")
	}
	scope := struct {
		Tenant  TenantID          `json:"tenant"`
		Project ProjectID         `json:"project"`
		Actor   ActorID           `json:"actor"`
		Tags    map[string]string `json:"tags,omitempty"`
	}{request.Scope.Tenant, request.Scope.Project, request.Scope.Actor, cloneTags(request.Scope.Tags)}
	data, err := json.Marshal(scope)
	if err != nil {
		return "", "", err
	}
	scopeHash := sha256.Sum256(data)
	filter := canonicalFilter(request.Filter)
	data, err = encodeFilter(filter)
	if err != nil {
		return "", "", err
	}
	filterHash := sha256.Sum256(data)
	return hex.EncodeToString(scopeHash[:]), hex.EncodeToString(filterHash[:]), nil
}

func canonicalFilter(filter QueryFilter) QueryFilter {
	switch value := filter.(type) {
	case BudgetStatusQuery:
		if value.ActiveAt != nil {
			normalized := value.ActiveAt.UTC()
			value.ActiveAt = &normalized
		}
		return value
	case *BudgetStatusQuery:
		if value == nil {
			return filter
		}
		copy := *value
		if copy.ActiveAt != nil {
			normalized := copy.ActiveAt.UTC()
			copy.ActiveAt = &normalized
		}
		return &copy
	case SpendSummaryQuery:
		value.StartTime = value.StartTime.UTC()
		value.EndTime = value.EndTime.UTC()
		value.GroupBy = append([]SpendDimension(nil), value.GroupBy...)
		value.OperationKinds = append([]OperationKind(nil), value.OperationKinds...)
		stableSortDimensions(value.GroupBy)
		stableSortOperationKinds(value.OperationKinds)
		return value
	case *SpendSummaryQuery:
		if value == nil {
			return filter
		}
		copy := *value
		copy.StartTime = copy.StartTime.UTC()
		copy.EndTime = copy.EndTime.UTC()
		copy.GroupBy = append([]SpendDimension(nil), value.GroupBy...)
		copy.OperationKinds = append([]OperationKind(nil), value.OperationKinds...)
		stableSortDimensions(copy.GroupBy)
		stableSortOperationKinds(copy.OperationKinds)
		return &copy
	default:
		return filter
	}
}

func stableSortDimensions(values []SpendDimension) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
func stableSortOperationKinds(values []OperationKind) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
