// Package tasks runs persistent background execution tasks with stable IDs,
// bounded head-plus-tail output spooling, and restart reconciliation.
//
// A command that finishes inside its synchronous window returns its result
// directly and leaves no task record or spool files behind. A command that
// outlives the window detaches: its record is persisted, output keeps
// spooling, and the same single execution is observable through List, Get,
// Output, and ReadOutput until it reaches exactly one terminal state.
// Tasks are never re-executed automatically; after a restart, records still
// marked running are reconciled to StateInterrupted.
//
// Every task belongs to one Owner object. All reads and kills require the
// owning object, so a bare task ID never grants access across owners.
// Session, RPC, and UI composition is intentionally left to integration.
package tasks
