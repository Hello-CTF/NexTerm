package store

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type incomingValue struct{ name string }

func (v incomingValue) Build(builder clause.Builder) {
	if statement, ok := builder.(*gorm.Statement); ok && statement.Dialector.Name() == "mysql" {
		builder.WriteString("VALUES(")
		builder.WriteQuoted(clause.Column{Name: v.name})
		builder.WriteByte(')')
	} else {
		builder.WriteQuoted(clause.Column{Table: "excluded", Name: v.name})
	}
}

func MaxExpression(db *gorm.DB, table, column string, incoming any) clause.Expr {
	name := "max"
	if db.Dialector.Name() != "sqlite" {
		name = "GREATEST"
	}
	if incoming == nil {
		incoming = incomingValue{name: column}
	}
	return gorm.Expr(name+"(?, ?)", clause.Column{Table: table, Name: column}, incoming)
}

type conditionalAssignment struct {
	condition clause.Where
	value     any
	column    clause.Column
}

func (a conditionalAssignment) Build(builder clause.Builder) {
	builder.WriteString("CASE WHEN ")
	a.condition.Build(builder)
	builder.WriteString(" THEN ")
	if column, ok := a.value.(clause.Column); ok && column.Table == "excluded" {
		incomingValue{name: column.Name}.Build(builder)
	} else {
		builder.AddVar(builder, a.value)
	}
	builder.WriteString(" ELSE ")
	builder.WriteQuoted(a.column)
	builder.WriteString(" END")
}

func preserveConflictConditions(db *gorm.DB) {
	raw, ok := db.Statement.Clauses["ON CONFLICT"]
	if !ok {
		return
	}
	conflict, ok := raw.Expression.(clause.OnConflict)
	if !ok || len(conflict.Where.Exprs) == 0 {
		return
	}
	table := db.Statement.Table
	if db.Dialector.Name() == "postgres" {
		for index, expr := range conflict.Where.Exprs {
			if eq, ok := expr.(clause.Eq); ok {
				if column, ok := eq.Column.(clause.Column); ok && column.Table == "" {
					column.Table = table
					eq.Column = column
					conflict.Where.Exprs[index] = eq
				}
			}
		}
	} else if db.Dialector.Name() == "mysql" {
		assignments := make(clause.Set, len(conflict.DoUpdates))
		for index, assignment := range conflict.DoUpdates {
			assignments[index] = clause.Assignment{Column: assignment.Column, Value: conditionalAssignment{condition: conflict.Where, value: assignment.Value, column: assignment.Column}}
		}
		conflict.DoUpdates = assignments
		conflict.Where = clause.Where{}
	}
	raw.Expression = conflict
	db.Statement.Clauses["ON CONFLICT"] = raw
}
