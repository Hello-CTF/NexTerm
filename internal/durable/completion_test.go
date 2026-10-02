package durable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func legacyCompletionMarker(id string) []byte {
	digest := sha256.Sum256([]byte("nexterm-durable-completion-v1\x00" + id))
	return []byte("\x1b]73733;" + hex.EncodeToString(digest[:]) + "\x07")
}

func TestLiteralLegacyMarkerRoundTrip(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	installRecords(backend, runner, current)
	payload := append([]byte("before-"), legacyCompletionMarker(current.info.ID)...)
	payload = append(payload, []byte("-after")...)
	session := newUnitSession(t, backend, current, payload)
	var live []byte
	buffer := make([]byte, 5)
	for len(live) < len(payload) {
		count, err := session.Read(buffer)
		if err != nil {
			t.Fatal(err)
		}
		live = append(live, buffer[:count]...)
	}
	if !bytes.Equal(live, payload) {
		t.Fatalf("live literal output = %q, want %q", live, payload)
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}

	current.info.Dead = true
	current.info.ExitCode = intPointer(0)
	current.deadStatus = "0"
	current.recordingLive = false
	installRecords(backend, runner, current)
	if err := os.WriteFile(backend.recorderDonePath(current.info.ID), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaySession, err := backend.Attach(context.Background(), current.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer replaySession.Detach()
	var replay []byte
	for {
		count, err := replaySession.Read(buffer)
		replay = append(replay, buffer[:count]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(replay, payload) {
		t.Fatalf("dead literal replay = %q, want %q", replay, payload)
	}
}

func TestRecorderCompletionFailureIsUnavailable(t *testing.T) {
	backend, _ := newUnitBackend(t)
	id := ids.New()
	if err := os.Mkdir(backend.sessionDir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderDonePath(id), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.recorderComplete(id); err == nil {
		t.Fatal("recorder failure was accepted as completion")
	}
}
