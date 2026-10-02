// Package durable provides terminals whose processes and raw output survive a
// daemon restart. It never falls back to a volatile shell: an unavailable tmux
// executable or recording makes the operation fail explicitly.
//
// A Session is an attachment, not the lifetime of the remote process. Close and
// Detach release only local resources. Kill destroys the owned tmux session.
// Attach returns the retained raw output followed by live output in one stream.
// Recordings are retained for the lifetime of the durable session and are
// removed after a successful Kill. Completion is published by the recorder
// only after it reaches real EOF; terminal output is never a lifecycle
// instruction.
package durable
