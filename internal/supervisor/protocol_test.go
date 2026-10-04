package supervisor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	payload := []byte(`{"version":1}`)
	if err := writeFrame(&buffer, frameHello, payload); err != nil {
		t.Fatal(err)
	}
	kind, received, err := readFrame(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if kind != frameHello || !bytes.Equal(received, payload) {
		t.Fatalf("round trip = %d, %q", kind, received)
	}
}

func TestReadFrameRejectsOversize(t *testing.T) {
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header, maxFramePayload+1)
	header[4] = byte(frameInput)
	_, _, err := readFrame(bytes.NewReader(header))
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversize read = %v, want ErrProtocol", err)
	}
}

func TestWriteFrameRejectsOversize(t *testing.T) {
	var buffer bytes.Buffer
	if err := writeFrame(&buffer, frameInput, make([]byte, maxFramePayload+1)); !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversize write = %v, want ErrProtocol", err)
	}
}

func TestUnmarshalFrameRejectsMalformed(t *testing.T) {
	var msg helloMsg
	if err := unmarshalFrame([]byte("{not-json"), &msg); !errors.Is(err, ErrProtocol) {
		t.Fatalf("malformed payload = %v, want ErrProtocol", err)
	}
}

func TestOutputPayloadRoundTrip(t *testing.T) {
	payload := outputPayload(42, []byte("data"))
	seq, data, err := parseOutput(payload)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 42 || string(data) != "data" {
		t.Fatalf("parse = %d, %q", seq, data)
	}
	if _, _, err := parseOutput([]byte{1, 2, 3}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("short output frame = %v, want ErrProtocol", err)
	}
}

func TestErrorCodeMapping(t *testing.T) {
	pairs := map[string]error{
		codeNotFound:      ErrNotFound,
		codeAlreadyExists: ErrAlreadyExists,
		codeIdentity:      ErrIdentity,
		codeExited:        ErrExited,
		codeInvalidInput:  ErrInvalidInput,
		codeUnavailable:   ErrUnavailable,
		codeClosed:        ErrClosed,
		codeProtocol:      ErrProtocol,
	}
	for code, sentinel := range pairs {
		if got := errorCode(sentinel); got != code {
			t.Fatalf("errorCode(%v) = %q, want %q", sentinel, got, code)
		}
		if !errors.Is(codedError(code, "detail"), sentinel) {
			t.Fatalf("codedError(%q) does not map to %v", code, sentinel)
		}
	}
	if got := errorCode(codedError(codeVersionMismatch, "v2")); got != codeProtocol {
		t.Fatalf("version mismatch maps to %q, want %q", got, codeProtocol)
	}
}
