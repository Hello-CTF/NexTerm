package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func evidenceTestPair(t *testing.T) (*EvidenceRecorder, *EvidenceVerifier, *store.Store, context.Context) {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	return NewEvidenceRecorder(db), NewEvidenceVerifier(db), db, ctx
}

func recordSynthetic(t *testing.T, recorder *EvidenceRecorder, id string) *Capture {
	t.Helper()
	capture := recorder.NewRecording(id, "conv-1", "job-1", 1700000000000)
	if err := capture.RecordOutput(10, "stdout", []byte("token=sk-syntheticsecretvalue1234567890")); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordReviewBoundary(20, "approve-production-write"); err != nil {
		t.Fatal(err)
	}
	if err := capture.RecordOutput(30, "stdout", []byte("synthetic tail")); err != nil {
		t.Fatal(err)
	}
	return capture
}

func TestEvidenceRecorderVerifierLifecycle(t *testing.T) {
	recorder, verifier, _, ctx := evidenceTestPair(t)
	capture := recordSynthetic(t, recorder, "cap-1")
	anchor, err := recorder.Seal(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.CaptureID != "cap-1" || anchor.ChainHead != capture.ChainHead || anchor.MAC == "" {
		t.Fatalf("anchor = %+v", anchor)
	}
	events, err := verifier.Replay(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("replayed %d events", len(events))
	}
	sanitized, sanitizedAnchor, err := verifier.Sanitize(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if sanitized.SourceChainHead != capture.ChainHead || sanitizedAnchor.ChainHead != sanitized.ChainHead {
		t.Fatal("sanitized provenance chain broken")
	}
	if string(sanitized.Events[0].Data) == string(capture.Events[0].Data) {
		t.Fatal("sanitize did not redact")
	}
	// The sanitized copy is independently replayable through the verifier.
	events, err = verifier.Replay(ctx, sanitized)
	if err != nil || len(events) != 3 {
		t.Fatalf("sanitized replay=%d err=%v", len(events), err)
	}
	if !sanitized.RequiresReview() {
		t.Fatal("review gate lost")
	}
}

func TestEvidenceVerifierCannotMintProvenance(t *testing.T) {
	recorder, verifier, db, ctx := evidenceTestPair(t)
	capture := recordSynthetic(t, recorder, "cap-1")
	anchor, err := recorder.Seal(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}

	// The review-side authority has no sealing capability: the only way to
	// produce anchored evidence is the recorder, which refuses decoded
	// captures outright.
	encoded, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Capture
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Events = decoded.Events[:1]
	decoded.Sealed = false
	decoded.ChainHead = ""
	decoded.EnvelopeHash = ""
	if _, err := recorder.Seal(ctx, &decoded); !errors.Is(err, ErrCaptureUntrusted) {
		t.Fatalf("reseal of decoded capture err=%v", err)
	}
	// Forging through a fresh recording requires the recorder: the original
	// anchor still resolves the original capture, and the forgery's anchor
	// is a separate record that never displaces it.
	forged := recorder.NewRecording("cap-1", "conv-1", "job-1", 1700000000000)
	forged.Events = decoded.Events
	forgedAnchor, err := recorder.Seal(ctx, forged)
	if err != nil {
		t.Fatal(err)
	}
	if forgedAnchor.ChainHead == anchor.ChainHead {
		t.Fatal("forgery reproduced the original chain head")
	}
	if err := capture.verifyAnchor(keyFrom(t, db, ctx), anchor); err != nil {
		t.Fatalf("original anchor displaced by the forgery: %v", err)
	}
	events, err := verifier.Replay(ctx, capture)
	if err != nil || len(events) != 3 {
		t.Fatalf("original replay=%d err=%v", len(events), err)
	}
	// The verifier rejects the truncated decoded value everywhere else.
	if _, err := verifier.Replay(ctx, &decoded); !errors.Is(err, ErrCaptureNotSealed) {
		t.Fatalf("replay of decoded capture err=%v", err)
	}
	if _, _, err := verifier.Sanitize(ctx, &decoded); !errors.Is(err, ErrCaptureNotSealed) {
		t.Fatalf("sanitize of decoded capture err=%v", err)
	}
}

func keyFrom(t *testing.T, db *store.Store, ctx context.Context) []byte {
	t.Helper()
	key, err := db.EvidenceSignerKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestEvidenceDoubleSanitizeIsRejected(t *testing.T) {
	recorder, verifier, db, ctx := evidenceTestPair(t)
	capture := recordSynthetic(t, recorder, "cap-1")
	if _, err := recorder.Seal(ctx, capture); err != nil {
		t.Fatal(err)
	}
	first, firstAnchor, err := verifier.Sanitize(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	// Re-sanitizing a sanitized copy is rejected instead of orphaning the
	// persisted anchor.
	if _, _, err := verifier.Sanitize(ctx, first); !errors.Is(err, ErrCaptureAlreadySanitized) {
		t.Fatalf("double sanitize err=%v", err)
	}
	// The first sanitized copy still replays with its own anchor.
	events, err := verifier.Replay(ctx, first)
	if err != nil || len(events) != 3 {
		t.Fatalf("first sanitized replay=%d err=%v", len(events), err)
	}
	if err := first.verifyAnchor(keyFrom(t, db, ctx), firstAnchor); err != nil {
		t.Fatalf("first sanitized anchor does not verify: %v", err)
	}
}

func TestEvidenceRecorderRefusesUntrustedInput(t *testing.T) {
	recorder, verifier, _, ctx := evidenceTestPair(t)
	// A capture built outside the trusted recording path cannot be sealed.
	plain := NewCapture("cap-x", "conv-1", "job-1", 1700000000000)
	if err := plain.RecordOutput(1, "stdout", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Seal(ctx, plain); !errors.Is(err, ErrCaptureUntrusted) {
		t.Fatalf("seal of plain capture err=%v", err)
	}
	if _, err := recorder.Seal(ctx, nil); !errors.Is(err, ErrCaptureUntrusted) {
		t.Fatalf("seal of nil capture err=%v", err)
	}
	// An unsealed trusted capture has no evidence operations.
	live := recorder.NewRecording("cap-live", "conv-1", "job-1", 1700000000000)
	if _, err := verifier.Replay(ctx, live); !errors.Is(err, ErrCaptureNotSealed) {
		t.Fatalf("replay of live capture err=%v", err)
	}
	if _, _, err := verifier.Sanitize(ctx, live); !errors.Is(err, ErrCaptureNotSealed) {
		t.Fatalf("sanitize of live capture err=%v", err)
	}
}

func TestEvidenceRestartPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	recorder := NewEvidenceRecorder(db)
	verifier := NewEvidenceVerifier(db)
	capture := recordSynthetic(t, recorder, "cap-1")
	anchor, err := recorder.Seal(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	sanitized, sanitizedAnchor, err := verifier.Sanitize(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedRecorder := NewEvidenceRecorder(reopened)
	restartedVerifier := NewEvidenceVerifier(reopened)

	// The key and anchors survived the restart: pre-restart evidence
	// verifies, and the recorder did not mint a replacement key.
	loadedKey, err := reopened.EvidenceSignerKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loadedKey == nil {
		t.Fatal("recorder key missing after restart")
	}
	if err := capture.verifyAnchor(loadedKey, anchor); err != nil {
		t.Fatalf("pre-restart anchor does not verify with the persisted key: %v", err)
	}
	events, err := restartedVerifier.Replay(ctx, capture)
	if err != nil || len(events) != 3 {
		t.Fatalf("post-restart replay=%d err=%v", len(events), err)
	}
	again, againAnchor, err := restartedVerifier.Sanitize(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if again.SourceChainHead != capture.ChainHead || againAnchor.ChainHead != again.ChainHead {
		t.Fatal("post-restart sanitize provenance broken")
	}
	events, err = restartedVerifier.Replay(ctx, sanitized)
	if err != nil || len(events) != 3 {
		t.Fatalf("post-restart sanitized replay=%d err=%v", len(events), err)
	}
	if err := sanitized.verifyAnchor(loadedKey, sanitizedAnchor); err != nil {
		t.Fatalf("sanitized anchor does not verify with the persisted key: %v", err)
	}
	_ = restartedRecorder
}
