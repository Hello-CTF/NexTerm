// Package cron manages durable AI jobs across sessions.
//
// The store is authoritative. A scheduler claims an occurrence with an
// expiring durable lease and a durable run record in the same compare-and-swap
// write. In-memory execution state only suppresses duplicate local work; it is
// never used to decide whether another scheduler owns a job.
//
// Due occurrences are coalesced into one execution. An execution is not
// automatically repeated after a restart: after its durable deadline and lease
// expire, another scheduler records it as interrupted and subsequent occurrences
// can proceed. Executors must honor context cancellation so timeout and lease
// loss also bound their side effects.
//
// Listing another session does not activate, unregister, or delete anything.
// Application and RPC composition belongs in adapters.
package cron
