package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

// TestRunnerEvidenceEmptyAnswerFinish covers a real job that finishes with
// an empty answer: the sealed capture carries exactly one deterministic
// lifecycle timing marker with no data, and it replays and sanitizes.
func TestRunnerEvidenceEmptyAnswerFinish(t *testing.T) {
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{schema.AssistantMessage("", nil)}}}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "empty answer")
	waitClosed(t, stream)

	capture := runner.lookupCapture(response.JobID)
	if capture == nil || !capture.Sealed {
		t.Fatalf("capture not sealed: %v", capture)
	}
	verifier := runner.evidenceVerifier()
	if verifier == nil {
		t.Fatal("evidence verifier not wired")
	}
	ctx := context.Background()
	events, err := verifier.Replay(ctx, capture)
	if err != nil {
		t.Fatalf("empty finish replay err=%v", err)
	}
	if len(events) != 1 || events[0].Kind != CaptureEventTiming || events[0].Name != "lifecycle" || len(events[0].Data) != 0 {
		t.Fatalf("empty finish events = %+v", events)
	}
	sanitized, sanitizedAnchor, err := verifier.Sanitize(ctx, capture)
	if err != nil {
		t.Fatalf("empty finish sanitize err=%v", err)
	}
	if sanitized.ID != capture.ID+"#sanitized" || sanitizedAnchor.CaptureID != sanitized.ID {
		t.Fatalf("sanitized identity = %q anchor=%q", sanitized.ID, sanitizedAnchor.CaptureID)
	}
	if _, err := verifier.Replay(ctx, sanitized); err != nil {
		t.Fatalf("sanitized empty finish replay err=%v", err)
	}
	// The raw anchor is preserved independently of the sanitized one.
	if _, _, err := runner.store.(EvidenceStore).EvidenceAnchor(ctx, capture.ID, capture.ChainHead); err != nil {
		t.Fatalf("raw anchor missing: %v", err)
	}
	// Re-sealing and re-sanitizing are both refused.
	if _, err := runner.evidenceRecorder().Seal(ctx, capture); !errors.Is(err, ErrCaptureSealed) {
		t.Fatalf("re-seal err=%v", err)
	}
	if _, _, err := verifier.Sanitize(ctx, sanitized); !errors.Is(err, ErrCaptureAlreadySanitized) {
		t.Fatalf("double sanitize err=%v", err)
	}
}

// TestRunnerEvidenceNoOutputShutdown covers a job that shuts down without
// any output: the sealed capture carries exactly the lifecycle marker and
// replays and sanitizes.
func TestRunnerEvidenceNoOutputShutdown(t *testing.T) {
	runner, _, _ := recoveryRunner(t)
	current := &job{
		id:          "job-shutdown",
		ctx:         context.Background(),
		cancel:      func() {},
		forceCancel: func() {},
		stream:      &SliceStream{},
		cancelSync:  newCancelSync(),
		doneCh:      make(chan struct{}),
	}
	current.capture = runner.newJobCapture(current.id, "conv-1")
	runner.wg.Add(1)
	runner.parkForShutdown(current)

	capture := runner.lookupCapture(current.id)
	if capture == nil || !capture.Sealed {
		t.Fatalf("shutdown capture not sealed: %v", capture)
	}
	verifier := runner.evidenceVerifier()
	ctx := context.Background()
	events, err := verifier.Replay(ctx, capture)
	if err != nil {
		t.Fatalf("shutdown replay err=%v", err)
	}
	if len(events) != 1 || events[0].Kind != CaptureEventTiming || events[0].Name != "lifecycle" || len(events[0].Data) != 0 {
		t.Fatalf("shutdown events = %+v", events)
	}
	sanitized, _, err := verifier.Sanitize(ctx, capture)
	if err != nil {
		t.Fatalf("shutdown sanitize err=%v", err)
	}
	if _, err := verifier.Replay(ctx, sanitized); err != nil {
		t.Fatalf("sanitized shutdown replay err=%v", err)
	}
	// Tampering and unsealed captures still fail closed.
	capture.Events[0].Data = []byte("tampered")
	if _, _, err := verifier.Sanitize(ctx, capture); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("tampered shutdown sanitize err=%v", err)
	}
	unsealed := runner.newJobCapture("job-unsealed", "conv-1")
	runner.rememberCapture("job-unsealed", unsealed)
	if _, _, err := runner.SanitizeRunCapture(ctx, "job-unsealed"); !errors.Is(err, ErrCaptureNotSealed) {
		t.Fatalf("unsealed sanitize err=%v", err)
	}
}
