package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestCaptureRecordingIncarnationsSealIndependentAnchors covers the restart
// boundary: a job's recording incarnation before a restart and the restored
// incarnation afterwards are separate recordings. Two no-output incarnations
// of the same job produce the same deterministic lifecycle event, so sharing
// one capture identity would make the second legitimate seal collide with
// the first anchor (same capture and chain head, different envelope). Each
// incarnation must seal under its own identity, leaving both anchors — the
// pre-restart one and the restored one — persisted and usable for replay,
// sanitize, and verify.
func TestCaptureRecordingIncarnationsSealIndependentAnchors(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	ctx := ipc.WithUserID(context.Background(), "owner-a")

	first := NewRunner(Config{Store: storage, Runs: storage})
	t.Cleanup(func() { _ = first.Close() })
	cap1 := first.newJobCapture("job-1", "conv-1")
	if cap1 == nil {
		t.Fatal("evidence recorder not wired")
	}
	first.sealJobCapture(ctx, &job{id: "job-1", capture: cap1})
	if !cap1.Sealed {
		t.Fatal("pre-restart incarnation capture not sealed")
	}

	// The restart rebuilds the runner over the same store; the restored job
	// records a fresh incarnation of the same job.
	second := NewRunner(Config{Store: storage, Runs: storage})
	t.Cleanup(func() { _ = second.Close() })
	cap2 := second.newJobCapture("job-1", "conv-1")
	if cap2 == nil {
		t.Fatal("evidence recorder not wired after restart")
	}
	second.sealJobCapture(ctx, &job{id: "job-1", capture: cap2})
	if !cap2.Sealed {
		t.Fatal("restored incarnation capture not sealed")
	}
	if cap1.ID == cap2.ID {
		t.Fatalf("recording incarnations share the capture identity %q", cap1.ID)
	}

	// Both anchors persist under their own identities.
	if _, _, err := storage.EvidenceAnchor(ctx, cap1.ID, cap1.ChainHead); err != nil {
		t.Fatalf("pre-restart anchor unusable: %v", err)
	}
	if _, _, err := storage.EvidenceAnchor(ctx, cap2.ID, cap2.ChainHead); err != nil {
		t.Fatalf("restored anchor unusable: %v", err)
	}

	// Both captures stay usable for replay, sanitize, and verify through the
	// review-side authority backed by the shared store.
	verifier := second.evidenceVerifier()
	if verifier == nil {
		t.Fatal("evidence verifier not wired")
	}
	for _, recorded := range []*Capture{cap1, cap2} {
		if err := recorded.Verify(); err != nil {
			t.Fatalf("capture %s does not verify: %v", recorded.ID, err)
		}
		events, err := verifier.Replay(ctx, recorded)
		if err != nil {
			t.Fatalf("capture %s replay: %v", recorded.ID, err)
		}
		if len(events) != 1 || events[0].Kind != CaptureEventTiming || events[0].Name != "lifecycle" {
			t.Fatalf("capture %s empty events = %+v", recorded.ID, events)
		}
		sanitized, anchor, err := verifier.Sanitize(ctx, recorded)
		if err != nil {
			t.Fatalf("capture %s sanitize: %v", recorded.ID, err)
		}
		if sanitized.ID != recorded.ID+"#sanitized" || anchor.CaptureID != sanitized.ID {
			t.Fatalf("capture %s sanitized identity = %q anchor=%q", recorded.ID, sanitized.ID, anchor.CaptureID)
		}
		if _, err := verifier.Replay(ctx, sanitized); err != nil {
			t.Fatalf("sanitized %s replay: %v", sanitized.ID, err)
		}
	}
}
