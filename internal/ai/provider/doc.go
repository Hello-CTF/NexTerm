// Package provider implements OpenAI-compatible chat providers.
//
// Chat invokes a handler serially in provider order. Streaming requests emit
// reasoning, content and cumulative tool-argument sizes as fragments arrive.
// Block requests emit aggregated reasoning and content once, followed by one
// tool-argument item per call. A returned completion always contains the full
// aggregated response. Cancelling the context closes the active HTTP request
// and never starts a fallback request.
//
// Fallback is deliberately limited to HTTP responses that reject a streaming
// request before a response body is produced. Network failures, HTTP 200
// protocol errors, partial output and cancellation are not retried: their
// completion and charging status is ambiguous. This makes duplicate output or
// a second ambiguous charge impossible in the fallback path.
package provider
