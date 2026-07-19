// Package postgres contains the PostgreSQL schema and namespace boundary for
// durable worker state. Repository operations intentionally live elsewhere.
package postgres

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	MaxIdentifierBytes  = 63
	MaxTablePrefixBytes = 24
)

var (
	namespaceIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	tablePrefix         = regexp.MustCompile(`^$|^[a-z][a-z0-9_]{0,22}_$`)
	logicalIdentifier   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Namespace identifies one worker-owned PostgreSQL namespace. Values are
// validated on construction; callers must use Relation/Identifier rather
// than interpolating them into SQL.
type Namespace struct {
	Database    string
	Schema      string
	TablePrefix string
}

func NewNamespace(database, schema, prefix string) (Namespace, error) {
	n := Namespace{Database: database, Schema: schema, TablePrefix: prefix}
	if err := n.Validate(); err != nil {
		return Namespace{}, err
	}
	return n, nil
}

func (n Namespace) Validate() error {
	if !namespaceIdentifier.MatchString(n.Database) {
		return fmt.Errorf("postgres database must match [a-z][a-z0-9_]{0,62}")
	}
	if !namespaceIdentifier.MatchString(n.Schema) {
		return fmt.Errorf("postgres schema must match [a-z][a-z0-9_]{0,62}")
	}
	if !tablePrefix.MatchString(n.TablePrefix) || len(n.TablePrefix) > MaxTablePrefixBytes {
		return fmt.Errorf("postgres table prefix must be empty or match [a-z][a-z0-9_]{0,22}_")
	}
	return nil
}

// Relation returns a safely quoted schema-qualified relation identifier.
// pgx.Identifier.Sanitize performs PostgreSQL identifier quoting; this method
// never uses search_path or SQL string concatenation.
func (n Namespace) Relation(logical string) (pgx.Identifier, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	if !logicalIdentifier.MatchString(logical) {
		return nil, fmt.Errorf("invalid worker relation %q", logical)
	}
	name := n.TablePrefix + logical
	if len(name) > MaxIdentifierBytes {
		return nil, fmt.Errorf("worker relation %q exceeds PostgreSQL's %d-byte identifier limit", name, MaxIdentifierBytes)
	}
	return pgx.Identifier{n.Schema, name}, nil
}

// Object returns a safely quoted schema-qualified object identifier. It is
// used for indexes, sequences, and constraints, which also carry the worker
// prefix and must never be silently truncated.
func (n Namespace) Object(logical string) (pgx.Identifier, error) {
	return n.Relation(logical)
}

func (n Namespace) SchemaIdentifier() pgx.Identifier {
	return pgx.Identifier{n.Schema}
}

func (n Namespace) Render(logical string) (string, error) {
	id, err := n.Relation(logical)
	if err != nil {
		return "", err
	}
	return id.Sanitize(), nil
}

func (n Namespace) PrefixName(logical string) (string, error) {
	if err := n.Validate(); err != nil {
		return "", err
	}
	if !logicalIdentifier.MatchString(logical) {
		return "", fmt.Errorf("invalid worker object %q", logical)
	}
	name := n.TablePrefix + logical
	if len(name) > MaxIdentifierBytes {
		return "", fmt.Errorf("worker object %q exceeds PostgreSQL's %d-byte identifier limit", name, MaxIdentifierBytes)
	}
	return name, nil
}

func (n Namespace) ContractRelation() (pgx.Identifier, error) {
	return n.Relation("schema_contract")
}

func (n Namespace) String() string {
	return strings.Join([]string{n.Database, n.Schema, n.TablePrefix}, "/")
}
