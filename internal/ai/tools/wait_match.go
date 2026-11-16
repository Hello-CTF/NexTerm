package tools

import (
	"context"
	"regexp"
	"strings"
)

// CompileWaitPattern compiles a wait_for pattern in multi-line mode: the
// pattern is matched against several terminal lines joined with "\n", so ^
// and $ must anchor at line boundaries for a line-anchored pattern to ever
// match a single line. Unanchored patterns match exactly as before.
func CompileWaitPattern(pattern string) (*regexp.Regexp, error) {
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, err
	}
	return regexp.Compile("(?m)" + pattern)
}

// MatchWaitLines reports whether regex matches the joined lines, either as
// rendered or with each line's trailing blanks and carriage return removed,
// so $ also matches a space-padded terminal row while a pattern that relies
// on trailing spaces keeps matching the unmodified text.
func MatchWaitLines(regex *regexp.Regexp, lines []string) bool {
	if regex.MatchString(strings.Join(lines, "\n")) {
		return true
	}
	trimmed := make([]string, len(lines))
	changed := false
	for index, line := range lines {
		trimmed[index] = strings.TrimRight(line, " \t\r")
		changed = changed || len(trimmed[index]) != len(line)
	}
	return changed && regex.MatchString(strings.Join(trimmed, "\n"))
}

// boundedCall runs a terminal read that does not itself honor ctx and stops
// waiting for it once ctx ends, so a read blocked on a terminal or tab lock
// cannot outlive the caller's deadline. The abandoned read finishes in the
// background and its result is dropped; a panic it raises while the caller
// still waits is re-raised to the caller.
func boundedCall[T any](ctx context.Context, read func() (T, error)) (T, error) {
	if ctx.Done() == nil {
		return read()
	}
	type result struct {
		value     T
		err       error
		recovered any
	}
	done := make(chan result, 1)
	go func() {
		var outcome result
		defer func() {
			outcome.recovered = recover()
			done <- outcome
		}()
		outcome.value, outcome.err = read()
	}()
	finish := func(outcome result) (T, error) {
		if outcome.recovered != nil {
			panic(outcome.recovered)
		}
		return outcome.value, outcome.err
	}
	select {
	case outcome := <-done:
		return finish(outcome)
	case <-ctx.Done():
	}
	select {
	case outcome := <-done:
		return finish(outcome)
	default:
		var zero T
		return zero, ctx.Err()
	}
}
