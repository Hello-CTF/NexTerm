package sync

import (
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm/clause"
)

// scalarMax returns the backend's scalar max function name: PostgreSQL lacks SQLite's scalar max(), using GREATEST instead.
func scalarMax(backend store.Backend) string {
	if backend == store.BackendPostgres {
		return "GREATEST"
	}
	return "max"
}

// conflictOwnerColumn returns the owner_id column for upsert WHERE clauses: PostgreSQL reports ambiguity in ON CONFLICT DO UPDATE
// WHERE without qualification (target table and the excluded pseudo-row share the name), while SQLite forbids table qualification.
func conflictOwnerColumn(backend store.Backend, table string) clause.Column {
	if backend == store.BackendPostgres {
		return clause.Column{Table: table, Name: "owner_id"}
	}
	return clause.Column{Name: "owner_id"}
}

// forUpdate returns the row-lock clause: PostgreSQL defaults to READ COMMITTED, so head/seq allocation must lock rows explicitly;
// SQLite single-writer transactions are naturally serialized and need no lock clause.
func forUpdate(backend store.Backend) string {
	if backend == store.BackendPostgres {
		return " FOR UPDATE"
	}
	return ""
}
