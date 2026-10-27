package docker

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// replayLogSink records announcements and frames in delivery order.
type replayLogSink struct {
	*collectSink
	mu  sync.Mutex
	log []string
}

func (s *replayLogSink) Send(ctx context.Context, frame []byte) error {
	s.mu.Lock()
	s.log = append(s.log, string(frame))
	s.mu.Unlock()
	return s.collectSink.Send(ctx, frame)
}

func (s *replayLogSink) AnnounceReplay(_ context.Context, bytes int) error {
	s.mu.Lock()
	s.log = append(s.log, fmt.Sprintf("replay:%d", bytes))
	s.mu.Unlock()
	return nil
}

func (s *replayLogSink) entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

// A resumed exec announces its detached backlog before delivering it, and
// live output after the backlog is not part of the announcement.
func TestResumeExecAnnouncesDetachedBacklogAsReplay(t *testing.T) {
	exec := newPipeExecSession(true)
	backend := &stubBackend{openExec: func(context.Context, ExecOptions) (ExecSession, error) { return exec, nil }}
	service := NewService(&stubProvider{sdk: backend})
	id, err := service.AttachExec(t.Context(), ExecAttachRequest{SessionID: "s1", Container: "c1", Sink: newCollectSink()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseStream(id) })
	if err := service.DetachStream(id); err != nil {
		t.Fatal(err)
	}
	exec.emit(t, "\x1b[c")
	waitExecReplay(t, service, id, 3)

	resumed := &replayLogSink{collectSink: newCollectSink()}
	if err := service.ResumeExec(id, resumed); err != nil {
		t.Fatal(err)
	}
	resumed.waitFrame(t)
	exec.emit(t, "live")
	resumed.waitFrame(t)
	got := resumed.entries()
	want := []string{"replay:3", "\x1b[c", "live"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("delivery = %q, want %q", got, want)
	}

	// Nothing buffered: a resume announces nothing.
	if err := service.DetachStream(id); err != nil {
		t.Fatal(err)
	}
	empty := &replayLogSink{collectSink: newCollectSink()}
	if err := service.ResumeExec(id, empty); err != nil {
		t.Fatal(err)
	}
	if got := empty.entries(); len(got) != 0 {
		t.Fatalf("empty resume delivered %q", got)
	}
}
