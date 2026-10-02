// Package provider implements OpenAI-compatible chat providers with Eino.
//
// ChatModel exposes native Eino Generate and Stream calls. BaseChatModel also
// returns the active profile's context window for direct ModelFactory use. Native
// frames carry model, run_id, call_id and context_window in Message.Extra.
// Chat invokes a handler serially in provider order and returns the same actual
// serving model and correlation metadata in Completion and Usage.
//
// RetryPolicy bounds retries independently for the configured model and its
// optional alternate. HTTP 429 and 5xx responses are replayable before any SSE
// data frame. Transport failures are replayable only before request headers are
// written and before a response byte arrives. Backoff waits honor cancellation.
// HTTP 400, cancellation, HTTP 200 protocol errors and post-frame failures never
// select the alternate model or replay output.
//
// A streaming request rejected by selected 4xx/501 responses can still use the
// existing block compatibility fallback on the same model. Native Stream reports
// terminal errors through Recv; callers cancel the invocation through its context.
package provider
