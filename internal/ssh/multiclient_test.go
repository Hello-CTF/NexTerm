//go:build unix

package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type streamReader interface {
	io.Reader
	Close() error
}

func readStreamUntil(t *testing.T, reader streamReader, marker string) []byte {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	found := make(chan result, 1)
	go func() {
		var buffer strings.Builder
		chunk := make([]byte, 4096)
		for {
			count, err := reader.Read(chunk)
			if count > 0 {
				buffer.Write(chunk[:count])
				if strings.Contains(buffer.String(), marker) {
					found <- result{data: []byte(buffer.String())}
					return
				}
			}
			if err != nil {
				found <- result{data: []byte(buffer.String()), err: err}
				return
			}
		}
	}()
	select {
	case result := <-found:
		if result.err != nil {
			t.Fatalf("read until %q: %v (data so far %q)", marker, result.err, result.data)
		}
		return result.data
	case <-time.After(30 * time.Second):
		_ = reader.Close()
		t.Fatalf("timed out waiting for %q", marker)
		return nil
	}
}

func assertOrderedOnce(t *testing.T, output string, markers ...string) {
	t.Helper()
	position := 0
	for _, marker := range markers {
		index := strings.Index(output[position:], marker)
		if index < 0 {
			t.Fatalf("marker %q missing after byte %d in %q", marker, position, output)
		}
		absolute := position + index
		if count := strings.Count(output, marker); count != 1 {
			t.Fatalf("marker %q appears %d times in %q", marker, count, output)
		}
		position = absolute + len(marker)
	}
}

func attachmentInfo(t *testing.T, attachment base.DurableAttachment) supervisor.Info {
	t.Helper()
	withInfo, ok := attachment.(interface{ Info() supervisor.Info })
	if !ok {
		t.Fatalf("attachment %T does not expose session info", attachment)
	}
	return withInfo.Info()
}

func sharedClientHost(t *testing.T, dataRoot string, shared *sharedTarget) *fakeHost {
	t.Helper()
	return &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		pathBinary:   "/usr/bin/nexterm-server",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		shared:       shared,
	}
}

func TestEnsureDaemonTwoClientsShareSession(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	shared := newSharedTarget(fixture.socketPath)
	shared.running = true
	hostA := sharedClientHost(t, dataRoot, shared)
	hostB := sharedClientHost(t, dataRoot, shared)
	ctx := context.Background()

	daemonA, err := testResolver(hostA, "").EnsureDaemon(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	daemonB, err := testResolver(hostB, "").EnsureDaemon(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hostA.sawCommand("setsid") || hostB.sawCommand("setsid") {
		t.Fatal("a client spawned a second daemon although one was already running")
	}
	if daemonA.Digest != daemonB.Digest {
		t.Fatalf("daemon digests differ: %q vs %q", daemonA.Digest, daemonB.Digest)
	}
	if daemonA.Binary != daemonB.Binary || daemonA.StateDir != daemonB.StateDir {
		t.Fatalf("clients resolved different daemons: %+v vs %+v", daemonA, daemonB)
	}

	providerA, providerB := daemonA.Provider(), daemonB.Provider()
	tabID := ids.New()
	attA, err := providerA.Create(ctx, base.DurableCreateOptions{
		ID:      tabID,
		Command: []string{"/bin/sh"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attA.Write([]byte("echo boot-''sh\n")); err != nil {
		t.Fatal(err)
	}
	readStreamUntil(t, attA, "boot-sh")

	if _, err := providerB.Create(ctx, base.DurableCreateOptions{ID: tabID, Cols: 80, Rows: 24}); !errors.Is(err, durable.ErrAlreadyExists) {
		t.Fatalf("second client create = %v; want ErrAlreadyExists without a duplicate session", err)
	}
	listed, err := providerB.ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != tabID {
		t.Fatalf("second client listed %v; want exactly the shared session", listed)
	}

	attB, err := providerB.Attach(ctx, tabID)
	if err != nil {
		t.Fatal(err)
	}
	replay := readStreamUntil(t, attB, "boot-sh")
	if count := strings.Count(string(replay), "boot-sh"); count != 1 {
		t.Fatalf("late attach replayed the boot marker %d times: %q", count, replay)
	}
	if attachmentInfo(t, attA).Incarnation != attachmentInfo(t, attB).Incarnation ||
		!attachmentInfo(t, attA).CreatedAt.Equal(attachmentInfo(t, attB).CreatedAt) {
		t.Fatalf("clients see different session identities: %+v vs %+v", attachmentInfo(t, attA), attachmentInfo(t, attB))
	}

	if _, err := attA.Write([]byte("echo ping-from-''a\n")); err != nil {
		t.Fatal(err)
	}
	readStreamUntil(t, attA, "ping-from-a")
	readStreamUntil(t, attB, "ping-from-a")
	if _, err := attB.Write([]byte("echo pong-from-''b\n")); err != nil {
		t.Fatal(err)
	}
	readStreamUntil(t, attB, "pong-from-b")
	readStreamUntil(t, attA, "pong-from-b")

	if err := attA.Close(); err != nil {
		t.Fatal(err)
	}
	listed, err = providerB.ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != tabID {
		t.Fatalf("detach killed the session: %v", listed)
	}
	if _, err := attB.Write([]byte("echo after-detach-''x\n")); err != nil {
		t.Fatal(err)
	}
	readStreamUntil(t, attB, "after-detach-x")

	attA2, err := providerA.Attach(ctx, tabID)
	if err != nil {
		t.Fatal(err)
	}
	replay = readStreamUntil(t, attA2, "after-detach-x")
	assertOrderedOnce(t, string(replay), "boot-sh", "ping-from-a", "pong-from-b", "after-detach-x")

	if err := attB.Resize(ctx, 100, 30); err != nil {
		t.Fatal(err)
	}
	infos, err := fixture.supervisor.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Cols != 100 || infos[0].Rows != 30 {
		t.Fatalf("daemon grid after remote resize = %+v; want 100x30", infos)
	}

	if _, err := attB.Write([]byte("echo live-''y\n")); err != nil {
		t.Fatal(err)
	}
	readStreamUntil(t, attA2, "live-y")

	if err := attA2.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	listed, err = providerB.ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("session survived a kill: %v", listed)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var exitErr *base.ExitError
	if err := attB.Wait(waitCtx); !errors.As(err, &exitErr) || exitErr.Signal != "SIGKILL" {
		t.Fatalf("peer Wait after kill = %v; want a SIGKILL exit error", err)
	}
}

func TestEnsureDaemonConcurrentClientsShareUpload(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	shared := newSharedTarget(fixture.socketPath)
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	target := dataRoot + "/nexterm/bin/nexterm-" + digest[:12]

	const clients = 4
	daemons := make([]*Daemon, clients)
	errs := make([]error, clients)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for index := 0; index < clients; index++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			host := &fakeHost{
				discovery:    testDiscovery(t, dataRoot),
				shared:       shared,
				uploadDigest: digest,
			}
			resolver := testResolver(host, "/local/nexterm")
			resolver.inspect = func(string) (UploadSource, error) { return fakeUploadSource(digest), nil }
			daemons[index], errs[index] = resolver.EnsureDaemon(context.Background(), nil)
		}(index)
	}
	start.Done()
	done.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("ensure %d: %v", index, err)
		}
	}
	for index, daemon := range daemons {
		if daemon.Binary != target {
			t.Fatalf("daemon %d binary = %q; want %q", index, daemon.Binary, target)
		}
		if daemon.Digest != daemons[0].Digest {
			t.Fatalf("daemon %d digest differs from the first client", index)
		}
	}
	uploads, renames, spawns := shared.counts()
	if uploads != clients || renames != clients {
		t.Fatalf("shared target saw %d uploads and %d renames; want %d each", uploads, renames, clients)
	}
	if spawns < 1 {
		t.Fatal("no client spawned the daemon")
	}
	stagings := shared.uploadPaths()
	seen := make(map[string]struct{}, len(stagings))
	for _, staging := range stagings {
		if _, duplicate := seen[staging]; duplicate {
			t.Fatalf("concurrent uploads shared the staging path %q", staging)
		}
		seen[staging] = struct{}{}
	}
	for _, pair := range shared.renamePairs() {
		if pair[1] != target {
			t.Fatalf("rename committed to %q; want %q", pair[1], target)
		}
	}
}

func TestEnsureDaemonRefusesIncompatibleRunningDaemon(t *testing.T) {
	dataRoot := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-stale-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := runDir + "/daemon.sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				header := make([]byte, 5)
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				if _, err := io.ReadFull(conn, make([]byte, binary.BigEndian.Uint32(header))); err != nil {
					return
				}
				payload := []byte(`{"code":"version_mismatch","message":"protocol version 99 is not supported"}`)
				frame := make([]byte, 5+len(payload))
				binary.BigEndian.PutUint32(frame, uint32(len(payload)))
				frame[4] = 3
				copy(frame[5:], payload)
				_, _ = conn.Write(frame)
			}(conn)
		}
	}()

	host := sharedClientHost(t, dataRoot, newSharedTarget(socketPath))
	host.shared.running = true
	_, err = testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, durable.ErrUnavailable) || !errors.Is(err, supervisor.ErrVersionMismatch) {
		t.Fatalf("EnsureDaemon error = %v; want an explicit protocol refusal", err)
	}
	if host.sawCommand("setsid") {
		t.Fatal("the resolver tried to replace an incompatible running daemon")
	}
}

func TestEnsureDaemonRefusesForeignStateDaemon(t *testing.T) {
	dataRootA := t.TempDir()
	fixtureB := newDaemonFixture(t, t.TempDir())
	host := sharedClientHost(t, dataRootA, newSharedTarget(fixtureB.socketPath))
	host.shared.running = true
	_, err := testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, durable.ErrUnavailable) || !errors.Is(err, supervisor.ErrStateMismatch) {
		t.Fatalf("EnsureDaemon error = %v; want an explicit state mismatch refusal", err)
	}
	if host.sawCommand("setsid") {
		t.Fatal("the resolver tried to replace a daemon owning a different state directory")
	}
}

func TestEnsureDaemonHelperRestartRecoversSession(t *testing.T) {
	dataRoot := t.TempDir()
	fixture1 := newDaemonFixture(t, dataRoot)
	shared := newSharedTarget(fixture1.socketPath)
	shared.running = true
	host1 := sharedClientHost(t, dataRoot, shared)
	ctx := context.Background()

	daemon1, err := testResolver(host1, "").EnsureDaemon(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tabID := ids.New()
	attachment, err := daemon1.Provider().Create(ctx, base.DurableCreateOptions{
		ID:      tabID,
		Command: []string{"/bin/sh", "-c", "printf 'restart-marker\\n'; sleep 300"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	readStreamUntil(t, attachment, "restart-marker")
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := fixture1.server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if err := fixture1.supervisor.Close(); err != nil {
		t.Fatal(err)
	}

	fixture2 := newDaemonFixture(t, dataRoot)
	shared.mu.Lock()
	shared.socketPath = fixture2.socketPath
	shared.mu.Unlock()
	host2 := sharedClientHost(t, dataRoot, shared)
	daemon2, err := testResolver(host2, "").EnsureDaemon(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host2.sawCommand("setsid") {
		t.Fatal("the resolver respawned although the replacement daemon was already serving")
	}
	listed, err := daemon2.Provider().ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != tabID {
		t.Fatalf("sessions after the helper restart = %v; want the original session only", listed)
	}
	reattached, err := daemon2.Provider().Attach(ctx, tabID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reattached.Close() }()
	replay := readStreamUntil(t, reattached, "restart-marker")
	if count := strings.Count(string(replay), "restart-marker"); count != 1 {
		t.Fatalf("restart replay contains the marker %d times: %q", count, replay)
	}
	if !attachmentInfo(t, reattached).Dead {
		t.Fatalf("restarted session reported alive: %+v", attachmentInfo(t, reattached))
	}
}

func TestEnsureDaemonSecondRemoteUserIsolation(t *testing.T) {
	dataRootA := t.TempDir()
	dataRootB := t.TempDir()
	fixtureA := newDaemonFixture(t, dataRootA)
	fixtureB := newDaemonFixture(t, dataRootB)
	hostA := sharedClientHost(t, dataRootA, newSharedTarget(fixtureA.socketPath))
	hostB := sharedClientHost(t, dataRootB, newSharedTarget(fixtureB.socketPath))
	hostA.shared.running = true
	hostB.shared.running = true
	ctx := context.Background()

	daemonA, err := testResolver(hostA, "").EnsureDaemon(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	daemonB, err := testResolver(hostB, "").EnsureDaemon(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemonA.StateDir == daemonB.StateDir || daemonA.Digest == daemonB.Digest {
		t.Fatalf("two remote users share daemon state: %+v vs %+v", daemonA, daemonB)
	}

	tabID := ids.New()
	attachment, err := daemonA.Provider().Create(ctx, base.DurableCreateOptions{
		ID:      tabID,
		Command: []string{"/bin/sh", "-c", "sleep 300"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	listedB, err := daemonB.Provider().ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listedB) != 0 {
		t.Fatalf("the second remote user sees the first user's sessions: %v", listedB)
	}

	crossDial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", fixtureB.socketPath)
	}
	if _, err := supervisor.NewRemoteClient(crossDial, daemonA.Digest).List(ctx); !errors.Is(err, supervisor.ErrStateMismatch) {
		t.Fatalf("cross-user attach = %v; want ErrStateMismatch", err)
	}
}
