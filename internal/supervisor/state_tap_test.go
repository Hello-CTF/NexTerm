package supervisor

import (
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// shownPrompt runs the idle inspection and returns the published prompt.
func (h *trackerHarness) shownPrompt() *SessionPrompt {
	h.t.Helper()
	h.idle()
	got := h.expect(base.SessionNeedsMe, base.ConfidenceHeuristic)
	if got.Prompt == nil {
		h.t.Fatal("no prompt published")
	}
	return got.Prompt
}

func TestStateTapSendsTheOptionOnceAndConsumesTheEpoch(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Apply? [y/N] ")
	prompt := h.shownPrompt()
	if !prompt.Tappable {
		t.Fatalf("prompt = %+v, want tappable", prompt)
	}
	sent, err := h.tracker.tap(prompt.Epoch, prompt.Fingerprint, 0)
	if err != nil || sent != "y\r" {
		t.Fatalf("tap = %q, %v; want y and Enter in line mode", sent, err)
	}
	// A double tap with the same epoch fails, though the screen is unchanged.
	if _, err := h.tracker.tap(prompt.Epoch, prompt.Fingerprint, 0); !errors.Is(err, ErrStalePrompt) {
		t.Fatalf("second tap = %v, want ErrStalePrompt", err)
	}
}

func TestStateTapRejectsAPromptChangedByInputOrOutput(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Apply? [y/N] ")
	prompt := h.shownPrompt()

	// Any input advances the epoch: the prompt the user saw may be answered.
	h.in(10 * time.Millisecond)
	if _, err := h.tracker.tap(prompt.Epoch, prompt.Fingerprint, 0); !errors.Is(err, ErrStalePrompt) {
		t.Fatalf("tap after input = %v, want ErrStalePrompt", err)
	}

	// A different prompt on screen has another fingerprint.
	h.out(time.Second, "\r\nDelete? [y/N] ")
	next := h.shownPrompt()
	if next.Fingerprint == prompt.Fingerprint || next.Epoch <= prompt.Epoch {
		t.Fatalf("new prompt %+v did not change identity from %+v", next, prompt)
	}
	if _, err := h.tracker.tap(next.Epoch, prompt.Fingerprint, 0); !errors.Is(err, ErrStalePrompt) {
		t.Fatalf("tap with the old fingerprint = %v, want ErrStalePrompt", err)
	}

	// The prompt scrolled away after it was shown.
	h.out(0, "\r\n$ ")
	if _, err := h.tracker.tap(next.Epoch, next.Fingerprint, 0); !errors.Is(err, ErrStalePrompt) {
		t.Fatalf("tap after the prompt went away = %v, want ErrStalePrompt", err)
	}
}

func TestStateTapRefusesUntappablePrompts(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Password: ")
	prompt := h.shownPrompt()
	if prompt.Tappable || prompt.Blocked != base.TapBlockedQuestion {
		t.Fatalf("question prompt = %+v", prompt)
	}
	if _, err := h.tracker.tap(prompt.Epoch, prompt.Fingerprint, 0); !errors.Is(err, ErrStalePrompt) {
		t.Fatalf("question tap = %v, want ErrStalePrompt", err)
	}
}

func TestStateTapRejectsAMissingOption(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Apply? [y/N] ")
	prompt := h.shownPrompt()
	if _, err := h.tracker.tap(prompt.Epoch, prompt.Fingerprint, len(prompt.Options)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("tap on a missing option = %v, want ErrInvalidInput", err)
	}
	// A refused tap leaves the prompt answerable.
	if _, err := h.tracker.tap(prompt.Epoch, prompt.Fingerprint, 1); err != nil {
		t.Fatalf("tap after a refused one = %v", err)
	}
}
