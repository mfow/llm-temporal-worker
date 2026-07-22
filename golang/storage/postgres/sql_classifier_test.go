package postgres

import (
	"context"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// SQLStatementKind is deliberately conservative: unknown statements are not
// treated as writes. The integration allowlist therefore fails closed if a
// budget path starts using a CTE, a stored procedure, or another statement
// shape that this small fixture does not classify explicitly.
type SQLStatementKind uint8

const (
	SQLStatementUnknown SQLStatementKind = iota
	SQLStatementSelect
	SQLStatementInsert
	SQLStatementUpdate
	SQLStatementDelete
)

// ClassifiedSQL is the redacted metadata retained by the integration fixture.
// SQL text is kept only in process so a failing test can identify the
// statement; no query arguments or result data are recorded.
type ClassifiedSQL struct {
	Kind         SQLStatementKind
	BudgetTable  bool
	BudgetRead   bool
	StatementSQL string
}

// ClassifySQL identifies the top-level command and whether the statement
// names one of the PostgreSQL budget relations. It intentionally strips
// comments and single-quoted values before matching relation names, while
// retaining double-quoted identifiers emitted by Namespace.Render.
func ClassifySQL(statement string) ClassifiedSQL {
	return classifySQL(statement, nil)
}

// ClassifySQLWithRelations applies the rendered, schema-qualified relation
// names for an active Namespace. This is required for prefixed installations:
// matching only the logical suffix would miss names such as tenant_budget_buckets.
func ClassifySQLWithRelations(statement string, relations ...string) ClassifiedSQL {
	return classifySQL(statement, relations)
}

func classifySQL(statement string, relations []string) ClassifiedSQL {
	clean := stripSQLCommentsAndLiterals(statement)
	trimmed := strings.TrimSpace(clean)
	kind := SQLStatementUnknown
	if token := firstSQLToken(trimmed); token != "" {
		switch strings.ToUpper(token) {
		case "SELECT":
			kind = SQLStatementSelect
		case "INSERT":
			kind = SQLStatementInsert
		case "UPDATE":
			kind = SQLStatementUpdate
		case "DELETE":
			kind = SQLStatementDelete
		}
	}
	budgetTable := mentionsBudgetTable(clean, relations)
	return ClassifiedSQL{
		Kind:         kind,
		BudgetTable:  budgetTable,
		BudgetRead:   mentionsBudgetRead(clean, relations),
		StatementSQL: trimmed,
	}
}

func firstSQLToken(statement string) string {
	for index, value := range statement {
		if value == ' ' || value == '\t' || value == '\r' || value == '\n' {
			continue
		}
		end := index
		for end < len(statement) {
			value := statement[end]
			if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || value == '_' {
				end++
				continue
			}
			break
		}
		return statement[index:end]
	}
	return ""
}

var budgetRelationNames = []string{
	"budget_policies",
	"budget_windows",
	"budget_redis_generations",
	"budget_journal_events",
	"budget_buckets",
	"operation_budget_reservations",
}

func mentionsBudgetTable(statement string, renderedRelations []string) bool {
	if len(renderedRelations) > 0 {
		for _, relation := range renderedRelations {
			if hasRenderedSQLIdentifier(statement, relation) {
				return true
			}
		}
		return false
	}
	for _, relation := range budgetRelationNames {
		if hasSQLIdentifier(statement, relation) {
			return true
		}
	}
	return false
}

func hasRenderedSQLIdentifier(statement, relation string) bool {
	lower := strings.ToLower(statement)
	relation = strings.ToLower(relation)
	for offset := 0; ; {
		index := strings.Index(lower[offset:], relation)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !isSQLIdentifierChar(lower[index-1])
		after := index + len(relation)
		afterOK := after == len(lower) || !isSQLIdentifierChar(lower[after])
		if beforeOK && afterOK {
			return true
		}
		offset = after
	}
}

func mentionsBudgetRead(statement string, renderedRelations []string) bool {
	for _, keyword := range []string{"FROM", "JOIN"} {
		if hasSQLKeywordWithBudgetRelation(statement, keyword, renderedRelations) {
			return true
		}
	}
	return false
}

func hasSQLKeywordWithBudgetRelation(statement, keyword string, renderedRelations []string) bool {
	lower := strings.ToLower(statement)
	keyword = strings.ToLower(keyword)
	for offset := 0; ; {
		index := strings.Index(lower[offset:], keyword)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !isSQLIdentifierChar(lower[index-1])
		after := index + len(keyword)
		afterOK := after == len(lower) || !isSQLIdentifierChar(lower[after])
		if beforeOK && afterOK && sourceStartsWithBudgetRelation(statement[after:], renderedRelations) {
			return true
		}
		offset = after
	}
}

func sourceStartsWithBudgetRelation(source string, renderedRelations []string) bool {
	source = strings.TrimSpace(source)
	for {
		lower := strings.ToLower(source)
		switch {
		case strings.HasPrefix(lower, "only") && (len(source) == len("only") || !isSQLIdentifierChar(lower[len("only")])),
			strings.HasPrefix(lower, "lateral") && (len(source) == len("lateral") || !isSQLIdentifierChar(lower[len("lateral")])):
			space := strings.IndexAny(source, " \t\r\n")
			if space < 0 {
				return false
			}
			source = strings.TrimSpace(source[space:])
		default:
			goto modifiersDone
		}
	}

modifiersDone:
	if source == "" || source[0] == '(' {
		return false
	}
	end := len(source)
	if index := strings.IndexAny(source, " \t\r\n,);\n"); index >= 0 {
		end = index
	}
	token := source[:end]
	if len(renderedRelations) > 0 {
		for _, relation := range renderedRelations {
			if strings.EqualFold(token, relation) {
				return true
			}
		}
		return false
	}
	for _, relation := range budgetRelationNames {
		if hasSQLIdentifier(token, relation) {
			return true
		}
	}
	return false
}

func hasSQLIdentifier(statement, identifier string) bool {
	lower := strings.ToLower(statement)
	for offset := 0; ; {
		index := strings.Index(lower[offset:], identifier)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !isSQLIdentifierChar(lower[index-1])
		after := index + len(identifier)
		afterOK := after == len(lower) || !isSQLIdentifierChar(lower[after])
		if beforeOK && afterOK {
			return true
		}
		offset = after
	}
}

func isSQLIdentifierChar(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '_'
}

func stripSQLCommentsAndLiterals(statement string) string {
	var out strings.Builder
	out.Grow(len(statement))
	for index := 0; index < len(statement); {
		switch {
		case statement[index] == '\'':
			out.WriteByte(' ')
			index++
			for index < len(statement) {
				if statement[index] == '\'' {
					if index+1 < len(statement) && statement[index+1] == '\'' {
						index += 2
						continue
					}
					index++
					break
				}
				if statement[index] == '\n' {
					out.WriteByte('\n')
				} else {
					out.WriteByte(' ')
				}
				index++
			}
		case statement[index] == '-' && index+1 < len(statement) && statement[index+1] == '-':
			index += 2
			for index < len(statement) && statement[index] != '\n' {
				index++
			}
			if index < len(statement) {
				out.WriteByte('\n')
				index++
			}
		case statement[index] == '/' && index+1 < len(statement) && statement[index+1] == '*':
			index += 2
			for index+1 < len(statement) && !(statement[index] == '*' && statement[index+1] == '/') {
				if statement[index] == '\n' {
					out.WriteByte('\n')
				} else {
					out.WriteByte(' ')
				}
				index++
			}
			if index+1 < len(statement) {
				out.WriteString("  ")
				index += 2
			}
		default:
			out.WriteByte(statement[index])
			index++
		}
	}
	return out.String()
}

// SQLTraceRecorder implements pgx.QueryTracer for integration fixtures. It
// records statement metadata only and is safe for concurrent pool activity.
type SQLTraceRecorder struct {
	mu                sync.Mutex
	statements        []ClassifiedSQL
	renderedRelations []string
}

func (recorder *SQLTraceRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	recorder.mu.Lock()
	recorder.statements = append(recorder.statements, classifySQL(data.SQL, recorder.renderedRelations))
	recorder.mu.Unlock()
	return ctx
}

func (recorder *SQLTraceRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (recorder *SQLTraceRecorder) Snapshot() []ClassifiedSQL {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]ClassifiedSQL(nil), recorder.statements...)
}

func (recorder *SQLTraceRecorder) Reset() {
	recorder.mu.Lock()
	recorder.statements = nil
	recorder.mu.Unlock()
}

func (recorder *SQLTraceRecorder) SetBudgetRelations(namespace Namespace) error {
	rendered := make([]string, 0, len(budgetRelationNames))
	for _, logical := range budgetRelationNames {
		relation, err := namespace.Render(logical)
		if err != nil {
			return err
		}
		rendered = append(rendered, relation)
	}
	recorder.mu.Lock()
	recorder.renderedRelations = rendered
	recorder.mu.Unlock()
	return nil
}
