package hitl

import (
	"context"
	"testing"
	"time"
)

func TestCancelDoesNotWaitForBlockedResumeStart(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	h.resumer.entered = make(chan struct{})
	h.resumer.release = make(chan struct{})
	resumeResult := make(chan error, 1)
	go func() {
		_, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer", request))
		resumeResult <- err
	}()
	<-h.resumer.entered
	cancelResult := make(chan error, 1)
	go func() {
		_, err := h.manager.Cancel("run")
		cancelResult <- err
	}()
	select {
	case err := <-cancelResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(h.resumer.release)
		<-resumeResult
		t.Fatal("cancellation waited for the blocked resume start")
	}
	executionContext := h.resumer.call(0).ctx
	select {
	case <-executionContext.Done():
	default:
		t.Fatal("execution was not canceled while resume start was blocked")
	}
	close(h.resumer.release)
	if err := <-resumeResult; err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.manager.Snapshot("run")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != RunStatusCanceled || snapshot.Terminal == nil || snapshot.Terminal.Reason != TerminalCanceled {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
