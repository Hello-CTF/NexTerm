package ssh

import "context"

// await runs call on its own goroutine and stops waiting when ctx ends. pkg/sftp requests take no context and a server
// blocked on a hung NFS or FUSE mount never answers them, so without this a stat could block its caller forever. The
// abandoned request keeps running until the server answers or the connection closes; its result is discarded, and
// discard (when set) releases a value that arrives after the caller has gone, such as an opened file handle.
//
// Only requests whose abandonment leaves nothing half-done go through here: lookups, and opens whose late handle
// discard closes. Mutating requests are waited for, so a write never reports cancellation while it may still commit.
func await[T any](ctx context.Context, call func() (T, error), discard func(T)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if ctx.Done() == nil {
		return call()
	}
	type outcome struct {
		value T
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := call()
		done <- outcome{value: value, err: err}
	}()
	select {
	case result := <-done:
		return result.value, result.err
	case <-ctx.Done():
		if discard != nil {
			go func() {
				if late := <-done; late.err == nil {
					discard(late.value)
				}
			}()
		}
		return zero, ctx.Err()
	}
}
