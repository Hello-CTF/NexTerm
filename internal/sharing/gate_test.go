package sharing

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type chunkReader struct {
	chunks      [][]byte
	index       int
	onRead      func()
	eofWithData bool
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	if r.onRead != nil {
		r.onRead()
	}
	chunk := r.chunks[r.index]
	r.index++
	if r.eofWithData && r.index == len(r.chunks) {
		return copy(p, chunk), io.EOF
	}
	return copy(p, chunk), nil
}

func testGrant(permission Permission, expiresAt int64) *Grant {
	return &Grant{
		ShareIDs:   []string{"share1"},
		DeviceID:   "device1",
		OwnerID:    "owner1",
		Permission: permission,
		ExpiresAt:  expiresAt,
	}
}

func TestGateReadOnlyBlocksInput(t *testing.T) {
	gate := NewGate(testGrant(PermissionRead, time.Now().Add(time.Hour).UnixMilli()))
	var dst bytes.Buffer
	err := gate.PipeInput(context.Background(), &dst, bytes.NewReader([]byte("ls\n")))
	if !errors.Is(err, ErrInputNotAllowed) {
		t.Fatalf("err = %v, want ErrInputNotAllowed", err)
	}
	if dst.Len() != 0 {
		t.Fatalf("read-only gate forwarded %d input bytes", dst.Len())
	}
}

func TestGateReadWriteInputCopies(t *testing.T) {
	gate := NewGate(testGrant(PermissionReadWrite, time.Now().Add(time.Hour).UnixMilli()))
	var dst bytes.Buffer
	if err := gate.PipeInput(context.Background(), &dst, bytes.NewReader([]byte("ls\n"))); err != nil {
		t.Fatal(err)
	}
	if dst.String() != "ls\n" {
		t.Fatalf("input = %q", dst.String())
	}
}

func TestGateOutputCopiesRegardlessOfPermission(t *testing.T) {
	gate := NewGate(testGrant(PermissionRead, time.Now().Add(time.Hour).UnixMilli()))
	var dst bytes.Buffer
	if err := gate.PipeOutput(context.Background(), &dst, bytes.NewReader([]byte("output"))); err != nil {
		t.Fatal(err)
	}
	if dst.String() != "output" {
		t.Fatalf("output = %q", dst.String())
	}
}

func TestGateExpiryDuringReadDiscardsChunkBothDirections(t *testing.T) {
	current := time.Now().UnixMilli()
	grant := testGrant(PermissionReadWrite, current+1000)
	reader := &chunkReader{chunks: [][]byte{[]byte("first"), []byte("second")}}
	reader.onRead = func() { current += 2000 }
	gate := NewGate(grant, WithGateNow(func() int64 { return current }))

	var output bytes.Buffer
	err := gate.PipeOutput(context.Background(), &output, reader)
	if !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("output err = %v, want ErrGrantExpired", err)
	}
	if output.Len() != 0 {
		t.Fatalf("chunk read during expiry must be discarded, got %q", output.String())
	}

	current = grant.ExpiresAt - 500
	reader = &chunkReader{chunks: [][]byte{[]byte("a"), []byte("b")}}
	reader.onRead = func() { current += 2000 }
	var input bytes.Buffer
	err = gate.PipeInput(context.Background(), &input, reader)
	if !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("input err = %v, want ErrGrantExpired", err)
	}
	if input.Len() != 0 {
		t.Fatalf("chunk read during expiry must be discarded, got %q", input.String())
	}
}

func TestGateRecheckRevokesBothDirections(t *testing.T) {
	recheckErr := errors.New("share revoked")
	calls := 0
	gate := NewGate(testGrant(PermissionReadWrite, time.Now().Add(time.Hour).UnixMilli()), WithRecheck(func(ctx context.Context, grant *Grant) (*Grant, error) {
		calls++
		if calls > 1 {
			return nil, recheckErr
		}
		return grant, nil
	}))

	var output bytes.Buffer
	err := gate.PipeOutput(context.Background(), &output, &chunkReader{chunks: [][]byte{[]byte("first"), []byte("second")}})
	if !errors.Is(err, recheckErr) {
		t.Fatalf("output err = %v, want recheck error", err)
	}
	if output.Len() != 0 {
		t.Fatalf("chunk read after revocation must be discarded, got %q", output.String())
	}

	calls = 0
	var input bytes.Buffer
	err = gate.PipeInput(context.Background(), &input, &chunkReader{chunks: [][]byte{[]byte("a"), []byte("b")}})
	if !errors.Is(err, recheckErr) {
		t.Fatalf("input err = %v, want recheck error", err)
	}
	if input.Len() != 0 {
		t.Fatalf("chunk read after revocation must be discarded, got %q", input.String())
	}
}

func TestGateEOFWithDataStillChecks(t *testing.T) {
	current := time.Now().UnixMilli()
	grant := testGrant(PermissionReadWrite, current+1000)
	reader := &chunkReader{chunks: [][]byte{[]byte("only")}, eofWithData: true}
	reader.onRead = func() { current += 2000 }
	gate := NewGate(grant, WithGateNow(func() int64 { return current }))
	var output bytes.Buffer
	err := gate.PipeOutput(context.Background(), &output, reader)
	if !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("err = %v, want ErrGrantExpired", err)
	}
	if output.Len() != 0 {
		t.Fatalf("(n>0, EOF) chunk read during expiry must be discarded, got %q", output.String())
	}

	current = time.Now().UnixMilli()
	grant = testGrant(PermissionRead, current+time.Hour.Milliseconds())
	gate = NewGate(grant, WithGateNow(func() int64 { return current }))
	output.Reset()
	if err := gate.PipeOutput(context.Background(), &output, &chunkReader{chunks: [][]byte{[]byte("only")}, eofWithData: true}); err != nil {
		t.Fatalf("valid grant must copy (n>0, EOF) chunk: %v", err)
	}
	if output.String() != "only" {
		t.Fatalf("output = %q, want %q", output.String(), "only")
	}
}

type ctxReader struct {
	ctx context.Context
}

func (r *ctxReader) Read(p []byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func TestGateCancelledContextStopsPipe(t *testing.T) {
	gate := NewGate(testGrant(PermissionReadWrite, time.Now().Add(time.Hour).UnixMilli()))

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if err := gate.PipeOutput(cancelled, &output, bytes.NewReader([]byte("x"))); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cancelled pipe wrote %q", output.String())
	}

	ctx, cancelBlocking := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var dst bytes.Buffer
		done <- gate.PipeOutput(ctx, &dst, &ctxReader{ctx: ctx})
	}()
	cancelBlocking()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked read err = %v, want context.Canceled", err)
	}
}

func TestGateNilGrantFailsClosed(t *testing.T) {
	gate := NewGate(nil)
	if gate.InputAllowed() {
		t.Fatal("nil grant must not allow input")
	}
	if err := gate.Check(context.Background()); err == nil {
		t.Fatal("nil grant must fail check")
	}
}

func TestGateRecheckRefreshesGrantBeforeExpiry(t *testing.T) {
	current := time.Now().UnixMilli()
	grant := testGrant(PermissionReadWrite, current+1000)
	shrunk := testGrant(PermissionRead, current+time.Hour.Milliseconds())
	gate := NewGate(grant, WithGateNow(func() int64 { return current }), WithRecheck(func(ctx context.Context, current *Grant) (*Grant, error) {
		return shrunk, nil
	}))

	current += 2000
	var output bytes.Buffer
	if err := gate.PipeOutput(context.Background(), &output, bytes.NewReader([]byte("still valid"))); err != nil {
		t.Fatalf("recheck refresh must replace the expired snapshot before the expiry check: %v", err)
	}
	if output.String() != "still valid" {
		t.Fatalf("output = %q", output.String())
	}
	if gate.InputAllowed() {
		t.Fatal("refreshed read grant must revoke input")
	}
	var input bytes.Buffer
	if err := gate.PipeInput(context.Background(), &input, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInputNotAllowed) {
		t.Fatalf("input err = %v, want ErrInputNotAllowed", err)
	}
	if input.Len() != 0 {
		t.Fatalf("shrunk grant forwarded %q", input.String())
	}
}

type cancelWithDataReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	data   []byte
}

func (r *cancelWithDataReader) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, r.data), r.ctx.Err()
}

func TestGateCancelledDuringReadWithDataDiscardsBothDirections(t *testing.T) {
	gate := NewGate(testGrant(PermissionReadWrite, time.Now().Add(time.Hour).UnixMilli()))

	ctx, cancel := context.WithCancel(context.Background())
	var output bytes.Buffer
	err := gate.PipeOutput(ctx, &output, &cancelWithDataReader{ctx: ctx, cancel: cancel, data: []byte("late")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("output err = %v, want context.Canceled", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cancelled output pipe wrote %q", output.String())
	}

	ctx, cancel = context.WithCancel(context.Background())
	var input bytes.Buffer
	err = gate.PipeInput(ctx, &input, &cancelWithDataReader{ctx: ctx, cancel: cancel, data: []byte("late")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("input err = %v, want context.Canceled", err)
	}
	if input.Len() != 0 {
		t.Fatalf("cancelled input pipe wrote %q", input.String())
	}
}

func TestGateRevalidateTTLSkipsRecheckWithinWindow(t *testing.T) {
	current := time.Now().UnixMilli()
	calls := 0
	gate := NewGate(testGrant(PermissionReadWrite, current+time.Hour.Milliseconds()),
		WithGateNow(func() int64 { return current }),
		WithRevalidateTTL(2*time.Second),
		WithRecheck(func(ctx context.Context, grant *Grant) (*Grant, error) {
			calls++
			return grant, nil
		}))
	if err := gate.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	current += 500
	if err := gate.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("recheck calls within TTL = %d, want 1", calls)
	}
	current += 1500
	if err := gate.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("recheck calls after TTL = %d, want 2", calls)
	}
}

func TestGateRevalidateTTLAppliesRevocationAfterTTL(t *testing.T) {
	current := time.Now().UnixMilli()
	calls := 0
	recheckErr := errors.New("share revoked")
	gate := NewGate(testGrant(PermissionReadWrite, current+time.Hour.Milliseconds()),
		WithGateNow(func() int64 { return current }),
		WithRevalidateTTL(2*time.Second),
		WithRecheck(func(ctx context.Context, grant *Grant) (*Grant, error) {
			calls++
			if calls > 1 {
				return nil, recheckErr
			}
			return grant, nil
		}))
	if err := gate.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	current += 500
	if err := gate.Check(context.Background()); err != nil {
		t.Fatalf("cached window must not surface the revocation early: %v", err)
	}
	current += 1500
	if err := gate.Check(context.Background()); !errors.Is(err, recheckErr) {
		t.Fatalf("err = %v, want recheck error once the TTL elapsed", err)
	}
}

func TestGateCheckFreshBypassesRevalidateTTL(t *testing.T) {
	current := time.Now().UnixMilli()
	calls := 0
	recheckErr := errors.New("share revoked")
	gate := NewGate(testGrant(PermissionReadWrite, current+time.Hour.Milliseconds()),
		WithGateNow(func() int64 { return current }),
		WithRevalidateTTL(2*time.Second),
		WithRecheck(func(ctx context.Context, grant *Grant) (*Grant, error) {
			calls++
			if calls > 1 {
				return nil, recheckErr
			}
			return grant, nil
		}))
	if err := gate.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	current += 500
	if err := gate.Check(context.Background()); err != nil {
		t.Fatalf("cached window must not surface the revocation early: %v", err)
	}
	if err := gate.CheckFresh(context.Background()); !errors.Is(err, recheckErr) {
		t.Fatalf("CheckFresh err = %v, want recheck error despite the TTL window", err)
	}
	if calls != 2 {
		t.Fatalf("recheck calls = %d, want 2", calls)
	}
}

func TestGateRevalidateTTLCachesInputCheck(t *testing.T) {
	current := time.Now().UnixMilli()
	inputCalls := 0
	gate := NewGate(testGrant(PermissionReadWrite, current+time.Hour.Milliseconds()),
		WithGateNow(func() int64 { return current }),
		WithRevalidateTTL(2*time.Second),
		WithInputCheck(func(ctx context.Context, grant *Grant) (*Grant, error) {
			inputCalls++
			return grant, nil
		}))
	var dst bytes.Buffer
	if err := gate.PipeInput(context.Background(), &dst, &chunkReader{chunks: [][]byte{[]byte("a"), []byte("b"), []byte("c")}}); err != nil {
		t.Fatal(err)
	}
	if inputCalls != 1 {
		t.Fatalf("input check calls within TTL = %d, want 1", inputCalls)
	}
}

func TestGateRevalidateTTLConcurrentAccess(t *testing.T) {
	var current atomic.Int64
	current.Store(time.Now().UnixMilli())
	gate := NewGate(testGrant(PermissionReadWrite, current.Load()+time.Hour.Milliseconds()),
		WithGateNow(func() int64 { return current.Load() }),
		WithRevalidateTTL(10*time.Millisecond),
		WithRecheck(func(ctx context.Context, grant *Grant) (*Grant, error) {
			copied := *grant
			return &copied, nil
		}),
		WithInputCheck(func(ctx context.Context, grant *Grant) (*Grant, error) {
			copied := *grant
			return &copied, nil
		}))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = gate.Check(context.Background())
				current.Add(2)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		var dst bytes.Buffer
		_ = gate.PipeOutput(context.Background(), &dst, bytes.NewReader(bytes.Repeat([]byte("x"), 128*1024)))
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		var dst bytes.Buffer
		_ = gate.PipeInput(context.Background(), &dst, bytes.NewReader(bytes.Repeat([]byte("y"), 128*1024)))
	}()
	wg.Wait()
}

func TestGateRefreshSerializationPreventsStaleOverwrite(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	stale := testGrant(PermissionReadWrite, expiresAt)
	current := testGrant(PermissionRead, expiresAt)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	gate := NewGate(testGrant(PermissionReadWrite, expiresAt), WithRecheck(func(ctx context.Context, grant *Grant) (*Grant, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return stale, nil
		}
		return current, nil
	}))
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	}()

	firstDone := make(chan error, 1)
	go func() { firstDone <- gate.Check(context.Background()) }()
	<-firstStarted
	secondDone := make(chan error, 1)
	go func() { secondDone <- gate.CheckFresh(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if permission := gate.Grant().Permission; permission != PermissionRead {
		t.Fatalf("stale refresh overwrote current grant: permission = %q", permission)
	}
}
