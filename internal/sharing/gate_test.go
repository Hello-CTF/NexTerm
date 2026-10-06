package sharing

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	gate := NewGate(testGrant(PermissionReadWrite, time.Now().Add(time.Hour).UnixMilli()), WithRecheck(func(ctx context.Context, grant *Grant) error {
		calls++
		if calls > 1 {
			return recheckErr
		}
		return nil
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
