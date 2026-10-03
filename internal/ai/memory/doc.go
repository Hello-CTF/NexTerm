// Package memory provides opt-in, explicitly managed operational memory.
//
// Entries are owned by a Scope and persisted independently of conversations and
// checkpoints. Prompt injection produces a transient Eino system message on a
// fresh message slice; the package has no conversation-history writer.
//
// Secret handling is heuristic and assignment-oriented: content shaped like
// "key: value" or "key=value" is rejected or redacted when the key carries a
// credential keyword as a full underscore-delimited segment (password, secret,
// token, api_key, ...), optionally followed by credential-noun segments as in
// AWS_SECRET_ACCESS_KEY. Prose mentions, plurals, and configuration counters
// such as max_tokens or token_limit are deliberately left untouched.
package memory
