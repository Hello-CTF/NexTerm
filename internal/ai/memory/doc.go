// Package memory provides opt-in, explicitly managed operational memory.
//
// Entries are owned by a Scope and persisted independently of conversations and
// checkpoints. Prompt injection produces a transient Eino system message on a
// fresh message slice; the package has no conversation-history writer.
package memory
