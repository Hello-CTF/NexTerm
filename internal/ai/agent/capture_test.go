package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// captureTestKey is the synthetic recorder key held by the seal side in
// this file; no real CLI output or secret is invented anywhere here.
var captureTestKey = []byte("synthetic-recorder-key")

// syntheticCapture builds a deterministic sealed capture plus its provenance
// anchor from synthetic fixtures only.
func syntheticCapture(t *testing.T) (*Capture, *ProvenanceAnchor) {
	t.Helper()
	capture := NewCapture("cap-1", "conv-1", "job-1", 1700000000000)
	if err := capture.RecordOutput(10, "stdout", []byte("synthetic line one")); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordOutput(25, "stderr", []byte("synthetic diagnostic")); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordSnapshot(40, []byte("synthetic screen state")); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordTiming(55, "tool-call", 1200); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordReviewBoundary(60, "approve-production-write"); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordOutput(80, "stdout", []byte("synthetic line two")); err != nil {
		t.Fatal(err)
	}
	anchor, err := capture.seal(captureTestKey)
	if err != nil {
		t.Fatal(err)
	}
	return capture, anchor
}

func TestCaptureRecordReplayDeterministic(t *testing.T) {
	capture, anchor := syntheticCapture(t)
	events, err := capture.replayAnchored(captureTestKey, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("replayed %d events, want 6", len(events))
	}
	wantKinds := []CaptureEventKind{CaptureEventOutput, CaptureEventOutput, CaptureEventSnapshot, CaptureEventTiming, CaptureEventReview, CaptureEventOutput}
	for index, event := range events {
		if event.Seq != uint64(index)+1 {
			t.Fatalf("event %d seq=%d", index, event.Seq)
		}
		if event.Kind != wantKinds[index] {
			t.Fatalf("event %d kind=%s want %s", index, event.Kind, wantKinds[index])
		}
	}
	// Replay of the same capture is byte-identical (deterministic protocol).
	first, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	again, err := capture.replayAnchored(captureTestKey, anchor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("replay is not deterministic")
	}
	// Replay must not alias the live event slice.
	events[0].Data = []byte("mutated")
	third, _ := capture.replayAnchored(captureTestKey, anchor)
	if string(third[0].Data) != "synthetic line one" {
		t.Fatal("replay aliased the capture's events")
	}
}

func TestCapturePreviewReplayIsNotEvidence(t *testing.T) {
	capture := NewCapture("cap-live", "conv-1", "job-1", 1700000000000)
	if err := capture.RecordOutput(5, "stdout", []byte("live chunk")); err != nil {
		t.Fatal(err)
	}
	events, err := capture.PreviewReplay()
	if err != nil || len(events) != 1 {
		t.Fatalf("preview replay=%d err=%v", len(events), err)
	}
	// The live preview is explicitly not evidence.
	if _, err := capture.replayAnchored(captureTestKey, nil); !errors.Is(err, ErrCaptureNotSealed) {
		t.Fatalf("evidence replay of unsealed capture err=%v", err)
	}
	if _, _, err := capture.sanitizeAnchored(captureTestKey, nil); !errors.Is(err, ErrCaptureNotSealed) {
		t.Fatalf("sanitize of unsealed capture err=%v", err)
	}
	// A sealed capture must not be preview-replayed.
	sealed, _ := syntheticCapture(t)
	if _, err := sealed.PreviewReplay(); !errors.Is(err, ErrCaptureSealed) {
		t.Fatalf("preview of sealed capture err=%v", err)
	}
}

func TestCaptureVerifyDetectsTampering(t *testing.T) {
	capture, _ := syntheticCapture(t)
	if err := capture.Verify(); err != nil {
		t.Fatalf("genuine capture failed verification: %v", err)
	}

	tampered, anchor := syntheticCapture(t)
	tampered.Events[1].Data = []byte("altered after recording")
	if err := tampered.Verify(); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("data tampering err=%v", err)
	}
	if _, err := tampered.replayAnchored(captureTestKey, anchor); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("replay of tampered capture err=%v", err)
	}
	if _, _, err := tampered.sanitizeAnchored(captureTestKey, anchor); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("sanitize of tampered capture err=%v", err)
	}

	reordered, _ := syntheticCapture(t)
	reordered.Events[1], reordered.Events[2] = reordered.Events[2], reordered.Events[1]
	if err := reordered.Verify(); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("reordering err=%v", err)
	}

	flagged, _ := syntheticCapture(t)
	flagged.Events[0].Redacted = true
	if err := flagged.Verify(); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("redaction flag tampering err=%v", err)
	}
}

func TestCaptureVerifyDetectsTruncationAndUnsealLaundering(t *testing.T) {
	// A sealed capture truncated to a prefix still has valid prefix links,
	// but the anchored envelope no longer matches.
	truncated, _ := syntheticCapture(t)
	truncated.Events = truncated.Events[:3]
	if err := truncated.Verify(); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("truncation err=%v", err)
	}
	// Clearing the seal alone breaks the envelope-bound seal state.
	unsealed, _ := syntheticCapture(t)
	unsealed.Sealed = false
	if err := unsealed.Verify(); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("unseal err=%v", err)
	}
}

func TestCaptureVerifyDetectsMetadataTampering(t *testing.T) {
	for name, mutate := range map[string]func(*Capture){
		"id":             func(c *Capture) { c.ID = "other" },
		"conversation":   func(c *Capture) { c.ConversationID = "other" },
		"job":            func(c *Capture) { c.JobID = "other" },
		"started at":     func(c *Capture) { c.StartedAt++ },
		"chain head":     func(c *Capture) { c.ChainHead = "f" + c.ChainHead[1:] },
		"envelope strip": func(c *Capture) { c.EnvelopeHash = "" },
	} {
		capture, _ := syntheticCapture(t)
		mutate(capture)
		if err := capture.Verify(); !errors.Is(err, ErrCaptureTampered) {
			t.Fatalf("%s tampering err=%v", name, err)
		}
	}
}

func TestCaptureBytePreservingRawOutput(t *testing.T) {
	capture := NewCapture("cap-bytes", "conv-1", "job-1", 1700000000000)
	raw := []byte{0xff, 0xfe, 0x00, 'a', '\n'}
	if err := capture.RecordOutput(1, "stdout", raw); err != nil {
		t.Fatal(err)
	}
	anchor, err := capture.seal(captureTestKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Capture
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Verify(); err != nil {
		t.Fatalf("byte capture does not verify: %v", err)
	}
	events, err := decoded.replayAnchored(captureTestKey, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(events[0].Data, raw) {
		t.Fatalf("raw bytes lost: %v", events[0].Data)
	}
	// Byte-level tampering with a different invalid-UTF-8 payload verifies as a break.
	decoded.Events[0].Data = []byte{0xfd, 0xfe, 0x00, 'a', '\n'}
	if err := decoded.Verify(); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("byte tampering err=%v", err)
	}
}

func TestCaptureSealIsNonResealable(t *testing.T) {
	capture, _ := syntheticCapture(t)
	// A second seal on the same value is rejected.
	if _, err := capture.seal(captureTestKey); !errors.Is(err, ErrCaptureSealed) {
		t.Fatalf("reseal err=%v", err)
	}
	// Clearing the exported Sealed flag on a copy does not re-arm Seal:
	// the ever-sealed boundary survives field-level mutation.
	forged := *capture
	forged.Sealed = false
	if _, err := forged.seal(captureTestKey); !errors.Is(err, ErrCaptureSealed) {
		t.Fatalf("reseal after clearing sealed err=%v", err)
	}
	// A JSON-decoded sealed capture stays non-resealable.
	encoded, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Capture
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err := decoded.seal(captureTestKey); !errors.Is(err, ErrCaptureSealed) {
		t.Fatalf("reseal of decoded capture err=%v", err)
	}
	// Sealing without a key is refused.
	fresh := NewCapture("cap-fresh", "conv-1", "job-1", 1700000000000)
	if _, err := fresh.seal(nil); err == nil {
		t.Fatal("keyless seal accepted")
	}
}

func TestCaptureForgedProvenanceIsRejected(t *testing.T) {
	capture, anchor := syntheticCapture(t)
	if !capture.RequiresReview() {
		t.Fatal("synthetic capture has no review gate")
	}

	// The attack: decode, clear the exported seal fields, truncate away the
	// review gate, and try to reseal. The attacker holds no recorder key, so
	// keyless sealing fails; an attacker-keyed seal mints only a self-signed
	// anchor the recorder side rejects.
	encoded, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	var forged Capture
	if err := json.Unmarshal(encoded, &forged); err != nil {
		t.Fatal(err)
	}
	forged.Events = forged.Events[:4]
	forged.Sealed = false
	forged.ChainHead = ""
	forged.EnvelopeHash = ""
	if _, err := forged.seal(nil); err == nil {
		t.Fatal("keyless forged reseal succeeded")
	}
	forgedAnchor, err := forged.seal([]byte("attacker-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := forged.Verify(); err != nil {
		t.Fatalf("forged value should self-verify: %v", err)
	}
	// The original anchor refuses the evidence operations: the forged chain
	// head no longer matches the independently held anchor.
	if _, err := forged.replayAnchored(captureTestKey, anchor); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("replay of forged capture with original anchor err=%v", err)
	}
	if _, _, err := forged.sanitizeAnchored(captureTestKey, anchor); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("sanitize of forged capture with original anchor err=%v", err)
	}
	// The self-head path is dead too: an anchor built from the forged
	// capture's own head has no valid MAC under the recorder's key.
	if _, err := forged.replayAnchored(captureTestKey, forgedAnchor); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("replay with self-head anchor err=%v", err)
	}
	if _, _, err := forged.sanitizeAnchored(captureTestKey, forgedAnchor); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("sanitize with self-head anchor err=%v", err)
	}
	// A nil anchor is refused outright.
	if _, err := forged.replayAnchored(captureTestKey, nil); !errors.Is(err, ErrCaptureAnchorMissing) {
		t.Fatalf("replay without anchor err=%v", err)
	}
	if _, _, err := forged.sanitizeAnchored(captureTestKey, nil); !errors.Is(err, ErrCaptureAnchorMissing) {
		t.Fatalf("sanitize without anchor err=%v", err)
	}
}

func TestCaptureSanitizeRedactsAndPreservesBoundaries(t *testing.T) {
	capture := NewCapture("cap-2", "conv-2", "job-2", 1700000000000)
	if err := capture.RecordOutput(5, "stdout", []byte("token=sk-syntheticsecretvalue1234567890 end")); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordReviewBoundary(6, "human-gate"); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordTiming(7, "wait", 50); err != nil {
		t.Fatal(err)
	}
	anchor, err := capture.seal(captureTestKey)
	if err != nil {
		t.Fatal(err)
	}

	sanitized, sanitizedAnchor, err := capture.sanitizeAnchored(captureTestKey, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if err := sanitized.Verify(); err != nil {
		t.Fatalf("sanitized copy does not verify: %v", err)
	}
	if !sanitized.Sealed {
		t.Fatal("sanitized copy is not sealed")
	}
	if sanitized.SourceChainHead != capture.ChainHead {
		t.Fatalf("source provenance = %q, want %q", sanitized.SourceChainHead, capture.ChainHead)
	}
	if bytes.Contains(sanitized.Events[0].Data, []byte("sk-syntheticsecretvalue1234567890")) {
		t.Fatal("secret survived sanitization")
	}
	if !sanitized.Events[0].Redacted {
		t.Fatal("redaction was not marked on the event")
	}
	if !sanitized.RequiresReview() || len(sanitized.ReviewGates()) != 1 || sanitized.ReviewGates()[0] != "human-gate" {
		t.Fatalf("review boundaries lost: %v", sanitized.ReviewGates())
	}
	if sanitized.Events[2].Kind != CaptureEventTiming || sanitized.Events[2].DurationMS != 50 {
		t.Fatal("timing marker lost")
	}
	// The original capture keeps its raw data.
	if !bytes.Contains(capture.Events[0].Data, []byte("sk-syntheticsecretvalue1234567890")) {
		t.Fatal("sanitize mutated the original capture")
	}
	// Sanitizing twice is explicitly rejected: a sanitized copy is a
	// terminal review artifact, and a second pass would orphan its anchor.
	if _, _, err := sanitized.sanitizeAnchored(captureTestKey, sanitizedAnchor); !errors.Is(err, ErrCaptureAlreadySanitized) {
		t.Fatalf("double sanitize err=%v", err)
	}
	// The sanitized copy's envelope binds the provenance: altering it verifies as a break.
	sanitized.SourceChainHead = "0" + sanitized.SourceChainHead[1:]
	if err := sanitized.Verify(); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("provenance tampering err=%v", err)
	}
}

func TestCaptureEmptyDataJSONRoundTrip(t *testing.T) {
	// nil and empty data must survive JSON persistence verifiably.
	capture := NewCapture("cap-empty", "conv-1", "job-1", 1700000000000)
	if err := capture.RecordOutput(1, "stdout", nil); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordOutput(2, "stdout", []byte{}); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordTiming(3, "wait", 10); err != nil {
		t.Fatal(err)
	}
	anchor, err := capture.seal(captureTestKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Capture
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Verify(); err != nil {
		t.Fatalf("raw-empty capture failed verification after JSON round trip: %v", err)
	}
	events, err := decoded.replayAnchored(captureTestKey, anchor)
	if err != nil || len(events) != 3 {
		t.Fatalf("replay=%d err=%v", len(events), err)
	}

	// Sanitized evidence with review/timing events also round-trips.
	sanitized, sanitizedAnchor, err := capture.sanitizeAnchored(captureTestKey, anchor)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(sanitized)
	if err != nil {
		t.Fatal(err)
	}
	var decodedSanitized Capture
	if err := json.Unmarshal(encoded, &decodedSanitized); err != nil {
		t.Fatal(err)
	}
	if err := decodedSanitized.Verify(); err != nil {
		t.Fatalf("sanitized capture failed verification after JSON round trip: %v", err)
	}
	if _, err := decodedSanitized.replayAnchored(captureTestKey, sanitizedAnchor); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureAnchorJSONPersistence(t *testing.T) {
	// The legitimate flow: capture and anchor are persisted independently,
	// and both evidence operations succeed after a JSON round trip.
	capture, anchor := syntheticCapture(t)
	captureJSON, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	anchorJSON, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Capture
	if err := json.Unmarshal(captureJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	var decodedAnchor ProvenanceAnchor
	if err := json.Unmarshal(anchorJSON, &decodedAnchor); err != nil {
		t.Fatal(err)
	}
	events, err := decoded.replayAnchored(captureTestKey, &decodedAnchor)
	if err != nil || len(events) != 6 {
		t.Fatalf("persisted replay=%d err=%v", len(events), err)
	}
	sanitized, sanitizedAnchor, err := decoded.sanitizeAnchored(captureTestKey, &decodedAnchor)
	if err != nil {
		t.Fatal(err)
	}
	if sanitized.SourceChainHead != capture.ChainHead {
		t.Fatal("sanitized provenance lost across persistence")
	}
	if _, err := sanitized.replayAnchored(captureTestKey, sanitizedAnchor); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sanitized.ReviewGates()[0], "approve-production-write") {
		t.Fatal("review gate lost across persistence")
	}
}
