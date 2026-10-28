//go:build unix

package supervisor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func sessionInfoByID(t *testing.T, infos []Info, id string) Info {
	t.Helper()
	for _, info := range infos {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("session %s not listed", id)
	return Info{}
}

func waitForActivity(t *testing.T, client *Client, id string, done func(*SessionActivity) bool) Info {
	t.Helper()
	deadline := time.Now().Add(testReadTimeout)
	for {
		infos, err := client.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		info := sessionInfoByID(t, infos, id)
		if info.Activity != nil && done(info.Activity) {
			return info
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity of %s never matched: %+v", id, info.Activity)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListReportsSessionActivity(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	before := time.Now()
	session := testScriptSession(t, supervisor, "printf ready; sleep 30")

	info := waitForActivity(t, client, session.ID(), func(activity *SessionActivity) bool { return activity.LastOutputAt != nil })
	if info.CreatedAt.Before(before.Add(-time.Second)) {
		t.Fatalf("created at %v, before the test started at %v", info.CreatedAt, before)
	}
	if info.Activity.LastOutputAt.Before(info.CreatedAt) {
		t.Fatalf("last output %v precedes creation %v", info.Activity.LastOutputAt, info.CreatedAt)
	}
	if info.Activity.Attached || info.Activity.DetachedAt == nil || !info.Activity.DetachedAt.Equal(info.CreatedAt) {
		t.Fatalf("a never-attached session reports %+v, want detached since creation %v", info.Activity, info.CreatedAt)
	}

	first, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	attached := sessionInfoByID(t, mustList(t, client), session.ID())
	// The state may move on between the two reads; it must name a state.
	switch name := first.StateName(); name {
	case base.SessionRunning, base.SessionNeedsMe, base.SessionJustFinished, base.SessionIdle:
	default:
		t.Fatalf("attachment state name %q", name)
	}
	if attached.Activity == nil || !attached.Activity.Attached || attached.Activity.DetachedAt != nil || attached.Activity.DetachedMSAgo != nil {
		t.Fatalf("an attached session reports %+v", attached.Activity)
	}

	if err := first.Detach(); err != nil {
		t.Fatal(err)
	}
	if partial := sessionInfoByID(t, mustList(t, client), session.ID()); !partial.Activity.Attached || partial.Activity.DetachedAt != nil {
		t.Fatalf("with one client left the session reports %+v", partial.Activity)
	}

	beforeDetach := time.Now()
	if err := second.Detach(); err != nil {
		t.Fatal(err)
	}
	detached := sessionInfoByID(t, mustList(t, client), session.ID())
	if detached.Activity.Attached || detached.Activity.DetachedAt == nil {
		t.Fatalf("after the last client left the session reports %+v", detached.Activity)
	}
	if detached.Activity.DetachedAt.Before(beforeDetach) {
		t.Fatalf("detached at %v, before the last client left at %v", detached.Activity.DetachedAt, beforeDetach)
	}
	// Ages are measured on the helper's clock at AsOf, so they match its
	// times; the monotonic reading may differ from the wall one by under 1ms.
	activity := detached.Activity
	if activity.AsOf.IsZero() || activity.DetachedMSAgo == nil || activity.LastOutputMSAgo == nil {
		t.Fatalf("activity without ages: %+v", activity)
	}
	if want := activity.AsOf.Sub(*activity.DetachedAt).Milliseconds(); abs64(*activity.DetachedMSAgo-want) > 1 {
		t.Fatalf("detached age %d, want %d from as_of", *activity.DetachedMSAgo, want)
	}
	if want := activity.AsOf.Sub(*activity.LastOutputAt).Milliseconds(); abs64(*activity.LastOutputMSAgo-want) > 1 {
		t.Fatalf("output age %d, want %d from as_of", *activity.LastOutputMSAgo, want)
	}
	// A repeated detach of a gone attachment does not move the time.
	if err := first.Detach(); err != nil {
		t.Fatal(err)
	}
	again := sessionInfoByID(t, mustList(t, client), session.ID())
	if !again.Activity.DetachedAt.Equal(*detached.Activity.DetachedAt) {
		t.Fatalf("detached at moved from %v to %v without a detach", detached.Activity.DetachedAt, again.Activity.DetachedAt)
	}

	descriptors, err := NewRemoteProvider(client).ListDurableDescriptors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range descriptors {
		if descriptor.ID != session.ID() {
			continue
		}
		if descriptor.Activity == nil || descriptor.Activity.DetachedAt == nil || !descriptor.Activity.DetachedAt.Equal(*detached.Activity.DetachedAt) {
			t.Fatalf("descriptor activity %+v, want the listed %+v", descriptor.Activity, detached.Activity)
		}
		if descriptor.Activity.LastOutputAt == nil {
			t.Fatal("descriptor lost the last output time")
		}
		if descriptor.State == nil || descriptor.State.State == "" {
			t.Fatalf("descriptor carries no attention state: %+v", descriptor.State)
		}
		return
	}
	t.Fatalf("session %s missing from descriptors", session.ID())
}

func TestListActivityUnderConcurrentAttachAndOutput(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, "while :; do printf x; sleep 0.001; done")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				attachment, err := supervisor.Attach(context.Background(), session.ID())
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := supervisor.List(context.Background()); err != nil {
					t.Error(err)
				}
				_ = attachment.Detach()
			}
		}()
	}
	wg.Wait()
	info := sessionInfoByID(t, mustListSupervisor(t, supervisor), session.ID())
	if info.Activity == nil || info.Activity.Attached || info.Activity.DetachedAt == nil {
		t.Fatalf("after every client left the session reports %+v", info.Activity)
	}
}

func mustList(t *testing.T, client *Client) []Info {
	t.Helper()
	infos, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return infos
}

func mustListSupervisor(t *testing.T, supervisor *Supervisor) []Info {
	t.Helper()
	infos, err := supervisor.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return infos
}

// fakeListServer answers one List request with the given raw session list.
func fakeListServer(t *testing.T, stateDir string, listed []byte) string {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-sup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	path := filepath.Join(runDir, "supervisor.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if kind, _, err := readFrame(conn); err != nil || kind != frameHello {
			return
		}
		if err := writeFrame(conn, frameHelloAck, fakeHelloAck()); err != nil {
			return
		}
		if kind, _, err := readFrame(conn); err != nil || kind != frameList {
			return
		}
		_ = writeFrame(conn, frameListed, listed)
	}()
	return path
}

func TestListFromHelperWithoutActivityDecodesAsUnknown(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	// A protocol 3 helper that predates activity reporting.
	listed := []byte(`[{"id":"old","created_at":"2026-01-02T03:04:05Z","incarnation":"inc","cols":80,"rows":24,"dead":false,` +
		`"history_base":0,"history_next":12,"state":{"state":"needs_me","since":"2026-01-02T03:05:00Z","confidence":"signal"}}]`)
	client := NewClient(fakeListServer(t, stateDir, listed), stateDir)
	descriptors, err := NewRemoteProvider(client).ListDurableDescriptors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 {
		t.Fatalf("descriptors = %+v", descriptors)
	}
	descriptor := descriptors[0]
	if descriptor.Activity != nil {
		t.Fatalf("an old helper decoded activity %+v, want unknown", descriptor.Activity)
	}
	if descriptor.State == nil || descriptor.State.State != "needs_me" {
		t.Fatalf("state = %+v", descriptor.State)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !descriptor.CreatedAt.Equal(want) {
		t.Fatalf("created at %v, want %v", descriptor.CreatedAt, want)
	}
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
