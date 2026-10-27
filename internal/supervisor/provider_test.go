//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// unknownDurableIdentity is a well-formed identity that no session carries.
func unknownDurableIdentity() base.DurableIdentity {
	return base.DurableIdentity{CreatedAt: time.Now(), Incarnation: ids.New()}
}

func attachmentIdentity(t *testing.T, attachment base.DurableAttachment) base.DurableIdentity {
	t.Helper()
	source, ok := attachment.(interface{ Identity() Identity })
	if !ok {
		t.Fatalf("%T does not expose its identity", attachment)
	}
	identity := source.Identity()
	return base.DurableIdentity{CreatedAt: identity.CreatedAt, Incarnation: identity.Incarnation}
}

func TestIPCKillDuringBlockedInput(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `stty raw -echo; exec sleep 60`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}

	writeDone := make(chan error, 1)
	go func() {
		for {
			_, err := stream.Write(make([]byte, 256*1024))
			if err == nil || errors.Is(err, ErrUnavailable) {
				continue
			}
			writeDone <- err
			return
		}
	}()

	select {
	case err := <-writeDone:
		t.Fatalf("write stream ended before kill: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	killDone := make(chan error, 1)
	go func() { killDone <- stream.Kill(ctx) }()
	select {
	case err := <-killDone:
		if err != nil {
			t.Fatalf("Kill during blocked input = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kill did not complete while PTY input was blocked")
	}
	select {
	case <-writeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked writer was not woken by Kill")
	}
}

func TestIPCRecordingErrorTerminatesStream(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `while true; do printf 'tick\n'; sleep 0.05; done`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}

	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "tick")

	session, err := supervisor.resolveSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeRecordingWriter(session); err != nil {
		t.Fatal(err)
	}

	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		for {
			if _, err := stream.Read(buffer); err != nil {
				readDone <- err
				return
			}
		}
	}()
	select {
	case err := <-readDone:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Read after recording failure = %v, want ErrUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Read did not wake after recording failure")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- stream.Wait(ctx) }()
	select {
	case err := <-waitDone:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Wait after recording failure = %v, want ErrUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not wake after recording failure")
	}

	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestIPCRecordingErrorUsesFatalFrame(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	ctx := context.Background()

	id := ids.New()
	if _, err := testClient(t, server).Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `while true; do printf 'tick\n'; sleep 0.05; done`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}

	conn := dialServer(t, server.SocketPath())
	handshakeConn(t, conn, supervisor.StateDir())
	payload, err := marshalFrame(frameAttach, attachMsg{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameAttach, payload); err != nil {
		t.Fatal(err)
	}
	awaitFrame(t, conn, frameAttached)
	awaitFrame(t, conn, frameOutput)

	session, err := supervisor.resolveSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeRecordingWriter(session); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		kind, _, err := readFrame(conn)
		if err != nil {
			t.Fatalf("read frames after recording failure: %v", err)
		}
		if kind == frameFatal {
			break
		}
		if kind == frameError {
			t.Fatal("async recording errors must use the fatal frame, not the request error frame")
		}
		if time.Now().After(deadline) {
			t.Fatal("no fatal frame arrived after recording failure")
		}
	}
	_ = conn.Close()

	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestIPCConcurrentStaleSocketRecovery(t *testing.T) {
	supervisor := testSupervisor(t)
	ctx := context.Background()
	runDir, err := os.MkdirTemp("/tmp", "nx-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := filepath.Join(runDir, "supervisor.sock")
	if err := os.WriteFile(socketPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	const starters = 8
	var wg sync.WaitGroup
	servers := make([]*Server, starters)
	errs := make([]error, starters)
	for index := 0; index < starters; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			servers[slot], errs[slot] = NewServer(supervisor, socketPath)
		}(index)
	}
	wg.Wait()

	succeeded := 0
	var winner *Server
	for slot, err := range errs {
		if err == nil {
			succeeded++
			winner = servers[slot]
			continue
		}
		if !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("starter %d = %v, want ErrAlreadyExists", slot, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent stale recovery started %d servers, want exactly 1", succeeded)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("live socket must not be deleted by losing starters: %v", err)
	}
	if _, err := testClient(t, winner).List(ctx); err != nil {
		t.Fatalf("winner must serve: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := winner.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewServer(supervisor, socketPath)
	if err != nil {
		t.Fatalf("restart after concurrent recovery = %v", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = restarted.Shutdown(shutdownCtx)
	}()
}

func TestProviderErrorTranslation(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	ctx := context.Background()

	providers := map[string]base.DurableProvider{
		"embedded": NewProvider(supervisor),
		"remote":   NewRemoteProvider(testClient(t, server)),
	}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			if _, err := provider.AttachWithIdentity(ctx, ids.New(), unknownDurableIdentity()); !errors.Is(err, durable.ErrNotFound) || errors.Is(err, ErrNotFound) {
				t.Fatalf("Attach unknown = %v, want durable.ErrNotFound only", err)
			}
			if _, err := provider.Create(ctx, base.DurableCreateOptions{Cols: 5000, Rows: 24}); !errors.Is(err, durable.ErrInvalidInput) {
				t.Fatalf("Create invalid = %v, want durable.ErrInvalidInput", err)
			}

			id := ids.New()
			attachment, err := provider.Create(ctx, base.DurableCreateOptions{
				ID:      id,
				Command: []string{"/bin/sh", "-c", "sleep 30"},
				Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = attachment.Close() }()
			if _, err := provider.Create(ctx, base.DurableCreateOptions{
				ID:      id,
				Command: []string{"/bin/sh", "-c", "sleep 30"},
			}); !errors.Is(err, durable.ErrAlreadyExists) {
				t.Fatalf("duplicate Create = %v, want durable.ErrAlreadyExists", err)
			}
			if err := attachment.Kill(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRemoteProviderCreateCompensatesOnAttachFailure(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	provider := NewRemoteProvider(testClient(t, server))

	provider.createAttach = nil // these tests replace the separate create or attach step
	realAttach := provider.attach
	t.Cleanup(func() { provider.attach = realAttach })

	assertCompensated := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("Create must fail when attach fails")
		}
		infos, listErr := supervisor.List(context.Background())
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(infos) != 0 {
			t.Fatalf("compensating kill must remove the session, still listed: %+v", infos)
		}
		dirents, readErr := os.ReadDir(sessionsRoot(supervisor.StateDir()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(dirents) != 0 {
			t.Fatalf("compensating kill must remove artifacts, found %d entries", len(dirents))
		}
	}

	t.Run("context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		provider.attach = func(attachCtx context.Context, client *Client, id string, _ *Identity) (*Stream, error) {
			cancel()
			return realAttach(attachCtx, client, id, nil)
		}
		_, err := provider.Create(ctx, base.DurableCreateOptions{
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		assertCompensated(t, err)
	})

	t.Run("protocol failure", func(t *testing.T) {
		provider.attach = func(context.Context, *Client, string, *Identity) (*Stream, error) {
			return nil, errors.New("attach: malformed attach response")
		}
		_, err := provider.Create(context.Background(), base.DurableCreateOptions{
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		assertCompensated(t, err)
	})
}

func TestClientKillSessionControl(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	info, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := Identity{CreatedAt: info.CreatedAt, Incarnation: ids.New()}
	if err := client.Kill(ctx, id, &wrong); !errors.Is(err, ErrIdentity) {
		t.Fatalf("kill with wrong identity = %v, want ErrIdentity", err)
	}
	identity := Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation}
	if err := client.Kill(ctx, id, &identity); err != nil {
		t.Fatalf("kill with expected identity = %v", err)
	}
	if _, err := os.Lstat(sessionDir(supervisor.StateDir(), id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifacts remain after control kill: %v", err)
	}
	if err := client.Kill(ctx, id, nil); err != nil {
		t.Fatalf("repeated control kill = %v, want nil", err)
	}
	if err := client.Kill(ctx, ids.New(), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("control kill unknown = %v, want ErrNotFound", err)
	}
}

func requireDurableDescriptorLister(t *testing.T, provider base.DurableProvider) base.DurableDescriptorLister {
	t.Helper()
	lister, ok := provider.(base.DurableDescriptorLister)
	if !ok {
		t.Fatalf("%T does not expose durable descriptors", provider)
	}
	return lister
}

func requireDurableDescriptorMatchesInfo(t *testing.T, got base.DurableDescriptor, want Info) {
	t.Helper()
	if got.ID != want.ID || got.Incarnation != want.Incarnation || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("descriptor identity = %q/%q/%s, want %q/%q/%s", got.ID, got.Incarnation, got.CreatedAt, want.ID, want.Incarnation, want.CreatedAt)
	}
	if got.Cols != want.Cols || got.Rows != want.Rows || got.Dead != want.Dead || got.Signal != want.Signal {
		t.Fatalf("descriptor state = %+v, want %+v", got, want)
	}
	if (got.ExitCode == nil) != (want.ExitCode == nil) || got.ExitCode != nil && *got.ExitCode != *want.ExitCode {
		t.Fatalf("descriptor exit code = %v, want %v", got.ExitCode, want.ExitCode)
	}
	if (got.FinishedAt == nil) != (want.FinishedAt == nil) || got.FinishedAt != nil && !got.FinishedAt.Equal(*want.FinishedAt) {
		t.Fatalf("descriptor finished at = %v, want %v", got.FinishedAt, want.FinishedAt)
	}
}

func TestDurableDescriptorProvidersEmptyAndAttachDoesNotCreate(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	providers := map[string]base.DurableProvider{
		"embedded": NewProvider(supervisor),
		"remote":   NewRemoteProvider(testClient(t, server)),
	}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			lister := requireDurableDescriptorLister(t, provider)
			descriptors, err := lister.ListDurableDescriptors(ctx)
			if err != nil || len(descriptors) != 0 {
				t.Fatalf("ListDurableDescriptors empty = %+v, %v", descriptors, err)
			}
			if _, err := provider.AttachWithIdentity(ctx, ids.New(), unknownDurableIdentity()); !errors.Is(err, durable.ErrNotFound) {
				t.Fatalf("Attach unknown = %v, want durable.ErrNotFound", err)
			}
			descriptors, err = lister.ListDurableDescriptors(ctx)
			if err != nil || len(descriptors) != 0 {
				t.Fatalf("Attach unknown created a session: %+v, %v", descriptors, err)
			}
			infos, err := supervisor.List(ctx)
			if err != nil || len(infos) != 0 {
				t.Fatalf("supervisor inventory after discovery = %+v, %v", infos, err)
			}
		})
	}
}

func TestRemoteProviderDescriptorsSharedAcrossClients(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	providerA := NewRemoteProvider(testClient(t, server))
	providerB := NewRemoteProvider(testClient(t, server))
	listerA := requireDurableDescriptorLister(t, providerA)
	listerB := requireDurableDescriptorLister(t, providerB)
	ctx := context.Background()
	id := ids.New()
	attachment, err := providerA.Create(ctx, base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    117,
		Rows:    39,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = attachment.Kill(context.Background())
		_ = attachment.Close()
	})
	if err := attachment.Resize(ctx, 123, 41); err != nil {
		t.Fatal(err)
	}

	infos, err := supervisor.List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("supervisor inventory = %+v, %v", infos, err)
	}
	for name, fixture := range map[string]struct {
		provider base.DurableProvider
		lister   base.DurableDescriptorLister
	}{
		"creator":     {provider: providerA, lister: listerA},
		"independent": {provider: providerB, lister: listerB},
	} {
		t.Run(name, func(t *testing.T) {
			descriptors, err := fixture.lister.ListDurableDescriptors(ctx)
			if err != nil || len(descriptors) != 1 {
				t.Fatalf("ListDurableDescriptors with active attachment = %+v, %v", descriptors, err)
			}
			requireDurableDescriptorMatchesInfo(t, descriptors[0], infos[0])
			legacyIDs, err := fixture.provider.(interface {
				ListDurable(context.Context) ([]string, error)
			}).ListDurable(ctx)
			if err != nil || len(legacyIDs) != 1 || legacyIDs[0] != id {
				t.Fatalf("ListDurable compatibility = %v, %v", legacyIDs, err)
			}
		})
	}
	after, err := supervisor.List(ctx)
	if err != nil || len(after) != 1 || after[0].ID != id {
		t.Fatalf("descriptor discovery changed inventory = %+v, %v", after, err)
	}
}

func TestDurableDescriptorProvidersReportExitAndExcludeExec(t *testing.T) {
	for _, name := range []string{"embedded", "remote"} {
		t.Run(name, func(t *testing.T) {
			supervisor := testSupervisor(t)
			server := testServer(t, supervisor)
			var provider base.DurableProvider
			if name == "embedded" {
				provider = NewProvider(supervisor)
			} else {
				provider = NewRemoteProvider(testClient(t, server))
			}
			lister := requireDurableDescriptorLister(t, provider)
			ctx := context.Background()
			id := ids.New()
			attachment, err := provider.Create(ctx, base.DurableCreateOptions{
				ID:      id,
				Command: []string{"/bin/sh", "-c", "exit 7"},
				Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
				Cols:    91,
				Rows:    27,
			})
			if err != nil {
				t.Fatal(err)
			}
			err = attachment.Wait(ctx)
			var exitErr *base.ExitError
			if !errors.As(err, &exitErr) || exitErr.Code != 7 {
				t.Fatalf("Wait = %v, want exit code 7", err)
			}
			if err := attachment.Close(); err != nil {
				t.Fatal(err)
			}
			infos, err := supervisor.List(ctx)
			if err != nil || len(infos) != 1 {
				t.Fatalf("supervisor inventory = %+v, %v", infos, err)
			}
			descriptors, err := lister.ListDurableDescriptors(ctx)
			if err != nil || len(descriptors) != 1 {
				t.Fatalf("ListDurableDescriptors after exit = %+v, %v", descriptors, err)
			}
			requireDurableDescriptorMatchesInfo(t, descriptors[0], infos[0])
			if !descriptors[0].Dead || descriptors[0].ExitCode == nil || *descriptors[0].ExitCode != 7 || descriptors[0].FinishedAt == nil {
				t.Fatalf("exit descriptor = %+v", descriptors[0])
			}

			execCtx, execCancel := context.WithCancel(context.Background())
			defer execCancel()
			execClient := testClient(t, server)
			execDone := make(chan error, 1)
			go func() {
				_, _, _, _, err := execClient.Exec(execCtx, ExecSpec{
					Command: []string{"/bin/sh", "-c", "exec sleep 30"},
				})
				execDone <- err
			}()
			awaitSessionCount(t, supervisor, 2)
			descriptors, err = lister.ListDurableDescriptors(ctx)
			if err != nil || len(descriptors) != 1 || descriptors[0].ID != id {
				t.Fatalf("one-shot exec entered durable descriptors = %+v, %v", descriptors, err)
			}
			execCancel()
			select {
			case <-execDone:
			case <-time.After(10 * time.Second):
				t.Fatal("one-shot exec did not stop")
			}
			awaitSessionCount(t, supervisor, 1)
		})
	}
}

func TestDurableDescriptorsDeepCopyExitMetadata(t *testing.T) {
	firstExitCode := 7
	secondExitCode := 11
	firstFinishedAt := time.Unix(100, 0).UTC()
	secondFinishedAt := time.Unix(200, 0).UTC()
	originalFirstFinishedAt := firstFinishedAt
	originalSecondFinishedAt := secondFinishedAt
	infos := []Info{
		{ID: "first", ExitCode: &firstExitCode, FinishedAt: &firstFinishedAt},
		{ID: "second", ExitCode: &secondExitCode, FinishedAt: &secondFinishedAt},
	}
	descriptors := durableDescriptors(infos)
	if len(descriptors) != 2 {
		t.Fatalf("durableDescriptors returned %d entries, want 2", len(descriptors))
	}
	for index := range infos {
		if descriptors[index].ExitCode == nil || descriptors[index].FinishedAt == nil {
			t.Fatalf("descriptor %d lost exit metadata: %+v", index, descriptors[index])
		}
		if descriptors[index].ExitCode == infos[index].ExitCode || descriptors[index].FinishedAt == infos[index].FinishedAt {
			t.Fatalf("descriptor %d aliases its input", index)
		}
	}
	if descriptors[0].ExitCode == descriptors[1].ExitCode || descriptors[0].FinishedAt == descriptors[1].FinishedAt {
		t.Fatal("descriptor elements share exit metadata pointers")
	}
	if *descriptors[0].ExitCode != 7 || *descriptors[1].ExitCode != 11 ||
		!descriptors[0].FinishedAt.Equal(originalFirstFinishedAt) || !descriptors[1].FinishedAt.Equal(originalSecondFinishedAt) {
		t.Fatalf("initial descriptors = %+v", descriptors)
	}

	firstExitCode = 70
	secondExitCode = 110
	firstFinishedAt = firstFinishedAt.Add(time.Hour)
	secondFinishedAt = secondFinishedAt.Add(time.Hour)
	if *descriptors[0].ExitCode != 7 || *descriptors[1].ExitCode != 11 ||
		!descriptors[0].FinishedAt.Equal(originalFirstFinishedAt) || !descriptors[1].FinishedAt.Equal(originalSecondFinishedAt) {
		t.Fatalf("input mutation changed descriptors = %+v", descriptors)
	}

	*descriptors[0].ExitCode = 701
	*descriptors[0].FinishedAt = originalFirstFinishedAt.Add(2 * time.Hour)
	if firstExitCode != 70 || !firstFinishedAt.Equal(originalFirstFinishedAt.Add(time.Hour)) {
		t.Fatalf("first descriptor mutation changed its input: code=%d finishedAt=%s", firstExitCode, firstFinishedAt)
	}
	if *descriptors[1].ExitCode != 11 || !descriptors[1].FinishedAt.Equal(originalSecondFinishedAt) {
		t.Fatalf("first descriptor mutation changed second descriptor: %+v", descriptors[1])
	}

	*descriptors[1].ExitCode = 1101
	*descriptors[1].FinishedAt = originalSecondFinishedAt.Add(2 * time.Hour)
	if secondExitCode != 110 || !secondFinishedAt.Equal(originalSecondFinishedAt.Add(time.Hour)) {
		t.Fatalf("second descriptor mutation changed its input: code=%d finishedAt=%s", secondExitCode, secondFinishedAt)
	}
	if *descriptors[0].ExitCode != 701 || !descriptors[0].FinishedAt.Equal(originalFirstFinishedAt.Add(2*time.Hour)) {
		t.Fatalf("second descriptor mutation changed first descriptor: %+v", descriptors[0])
	}
}

func TestDurableDescriptorProvidersReportSignal(t *testing.T) {
	for _, name := range []string{"embedded", "remote"} {
		t.Run(name, func(t *testing.T) {
			supervisor := testSupervisor(t)
			server := testServer(t, supervisor)
			var provider base.DurableProvider
			if name == "embedded" {
				provider = NewProvider(supervisor)
			} else {
				provider = NewRemoteProvider(testClient(t, server))
			}
			attachment, err := provider.Create(context.Background(), base.DurableCreateOptions{
				ID:      ids.New(),
				Command: []string{"/bin/sh", "-c", "kill -KILL $$"},
				Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
			})
			if err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = attachment.Wait(waitCtx)
			var exitErr *base.ExitError
			if !errors.As(err, &exitErr) || exitErr.Signal == "" {
				t.Fatalf("Wait = %v, want a non-empty signal", err)
			}
			if err := attachment.Close(); err != nil {
				t.Fatal(err)
			}
			infos, err := supervisor.List(context.Background())
			if err != nil || len(infos) != 1 || infos[0].Signal == "" {
				t.Fatalf("authoritative signal Info = %+v, %v", infos, err)
			}
			descriptors, err := requireDurableDescriptorLister(t, provider).ListDurableDescriptors(context.Background())
			if err != nil || len(descriptors) != 1 {
				t.Fatalf("ListDurableDescriptors after signal = %+v, %v", descriptors, err)
			}
			requireDurableDescriptorMatchesInfo(t, descriptors[0], infos[0])
			if descriptors[0].Signal == "" {
				t.Fatalf("descriptor lost signal: %+v", descriptors[0])
			}
		})
	}
}

func TestDurableDescriptorProvidersPropagateListErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	embedded := NewProvider(testSupervisor(t))
	descriptors, err := requireDurableDescriptorLister(t, embedded).ListDurableDescriptors(ctx)
	if descriptors != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled embedded ListDurableDescriptors = %+v, %v", descriptors, err)
	}

	dialErr := errors.New("discovery unavailable")
	remote := NewRemoteProvider(NewRemoteClient(func(context.Context) (net.Conn, error) {
		return nil, dialErr
	}, "test"))
	descriptors, err = requireDurableDescriptorLister(t, remote).ListDurableDescriptors(context.Background())
	if descriptors != nil || !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("failed remote ListDurableDescriptors = %+v, %v", descriptors, err)
	}
	legacyIDs, legacyErr := remote.ListDurable(context.Background())
	if legacyIDs != nil || !errors.Is(legacyErr, durable.ErrUnavailable) {
		t.Fatalf("failed legacy ListDurable = %v, %v", legacyIDs, legacyErr)
	}
}

func requireDurableMetadataProvider(t *testing.T, provider base.DurableProvider) base.DurableMetadataProvider {
	t.Helper()
	metadata, ok := provider.(base.DurableMetadataProvider)
	if !ok {
		t.Fatalf("%T does not expose durable metadata", provider)
	}
	return metadata
}

func TestEmbeddedProviderCreateRechecksIdentityBeforeAttach(t *testing.T) {
	supervisor := testSupervisor(t)
	provider := NewProvider(supervisor)
	ctx := context.Background()
	id := ids.New()
	t.Cleanup(func() { _ = supervisor.Kill(context.Background(), id) })
	realAttach := provider.attach
	t.Cleanup(func() { provider.attach = realAttach })
	var replacement *Session
	provider.attach = func(attachCtx context.Context, current *Supervisor, original *Session) (*Attachment, error) {
		if err := current.Kill(attachCtx, id); err != nil {
			return nil, err
		}
		var err error
		replacement, err = current.Create(attachCtx, CreateOptions{
			ID:      id,
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		if err != nil {
			return nil, err
		}
		return realAttach(attachCtx, current, original)
	}

	attachment, err := provider.Create(ctx, base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if attachment != nil {
		defer func() { _ = attachment.Close() }()
	}
	if !errors.Is(err, durable.ErrIdentity) {
		t.Fatalf("Create across kill/recreate = %v, want identity changed", err)
	}
	infos, listErr := supervisor.List(ctx)
	if listErr != nil || len(infos) != 1 || replacement == nil || infos[0].Incarnation != replacement.Identity().Incarnation {
		t.Fatalf("replacement must survive create compensation: infos=%+v replacement=%p err=%v", infos, replacement, listErr)
	}
}

func TestDurableMetadataProvidersRenameConflictAndRetry(t *testing.T) {
	for _, name := range []string{"embedded", "remote"} {
		t.Run(name, func(t *testing.T) {
			supervisor := testSupervisor(t)
			server := testServer(t, supervisor)
			var provider base.DurableProvider
			if name == "embedded" {
				provider = NewProvider(supervisor)
			} else {
				provider = NewRemoteProvider(testClient(t, server))
			}
			ctx := context.Background()
			id := ids.New()
			attachment, err := provider.Create(ctx, base.DurableCreateOptions{
				ID:      id,
				Command: []string{"/bin/sh", "-c", "sleep 30"},
				Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = attachment.Close()
				_ = supervisor.Kill(context.Background(), id)
			})

			metadata := requireDurableMetadataProvider(t, provider)
			supported, err := metadata.DurableMetadataSupported(ctx)
			if err != nil || !supported {
				t.Fatalf("DurableMetadataSupported = %v, %v", supported, err)
			}
			descriptors, err := requireDurableDescriptorLister(t, provider).ListDurableDescriptors(ctx)
			if err != nil || len(descriptors) != 1 {
				t.Fatalf("initial descriptors = %+v, %v", descriptors, err)
			}
			request := base.DurableRenameOptions{
				ID:                       id,
				Incarnation:              descriptors[0].Incarnation,
				RequestID:                ids.New(),
				ExpectedMetadataRevision: descriptors[0].MetadataRevision,
				Title:                    "builder",
			}
			result, err := metadata.RenameDurable(ctx, request)
			if err != nil || result.Title != "builder" || result.MetadataRevision != 1 {
				t.Fatalf("RenameDurable = %+v, %v", result, err)
			}
			retry, err := metadata.RenameDurable(ctx, request)
			if err != nil || retry != result {
				t.Fatalf("same request retry = %+v, %v, want %+v", retry, err, result)
			}

			conflictRequest := base.DurableRenameOptions{
				ID:                       id,
				Incarnation:              request.Incarnation,
				RequestID:                ids.New(),
				ExpectedMetadataRevision: 0,
				Title:                    "stale-draft",
			}
			_, err = metadata.RenameDurable(ctx, conflictRequest)
			var conflict *base.DurableMetadataConflictError
			if !errors.As(err, &conflict) || !errors.Is(err, base.ErrMetadataConflict) {
				t.Fatalf("stale rename = %v, want metadata conflict", err)
			}
			if conflict.CurrentTitle != "builder" || conflict.CurrentMetadataRevision != 1 {
				t.Fatalf("conflict current metadata = %+v", conflict)
			}
			_, err = metadata.RenameDurable(ctx, conflictRequest)
			var conflictRetry *base.DurableMetadataConflictError
			if !errors.As(err, &conflictRetry) || conflictRetry.CurrentTitle != conflict.CurrentTitle || conflictRetry.CurrentMetadataRevision != conflict.CurrentMetadataRevision {
				t.Fatalf("same conflict request retry = %+v, %v", conflictRetry, err)
			}
			descriptors, err = requireDurableDescriptorLister(t, provider).ListDurableDescriptors(ctx)
			if err != nil || len(descriptors) != 1 || descriptors[0].Title != "builder" || descriptors[0].MetadataRevision != 1 {
				t.Fatalf("conflict changed canonical metadata = %+v, %v", descriptors, err)
			}

			mismatchedRetry := request
			mismatchedRetry.Title = "different-request"
			if _, err := metadata.RenameDurable(ctx, mismatchedRetry); !errors.Is(err, durable.ErrInvalidInput) {
				t.Fatalf("request ID reuse with another title = %v, want invalid input", err)
			}
			result, err = metadata.RenameDurable(ctx, base.DurableRenameOptions{
				ID:                       id,
				Incarnation:              request.Incarnation,
				RequestID:                ids.New(),
				ExpectedMetadataRevision: conflict.CurrentMetadataRevision,
				Title:                    "stale-draft",
			})
			if err != nil || result.Title != "stale-draft" || result.MetadataRevision != 2 {
				t.Fatalf("rename after conflict refresh = %+v, %v", result, err)
			}

			if err := supervisor.Kill(ctx, id); err != nil {
				t.Fatal(err)
			}
			replacement, err := supervisor.Create(ctx, CreateOptions{
				ID:      id,
				Command: []string{"/bin/sh", "-c", "sleep 30"},
				Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := metadata.RenameDurable(ctx, request); !errors.Is(err, durable.ErrIdentity) {
				t.Fatalf("old request renamed replacement incarnation = %v, want identity changed", err)
			}
			descriptors, err = requireDurableDescriptorLister(t, provider).ListDurableDescriptors(ctx)
			if err != nil || len(descriptors) != 1 || descriptors[0].Incarnation != replacement.Identity().Incarnation || descriptors[0].Title != "" || descriptors[0].MetadataRevision != 0 {
				t.Fatalf("old request changed replacement metadata = %+v, %v", descriptors, err)
			}
		})
	}
}

func TestRemoteProviderConcurrentRenameHasOneWinner(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	clientA := testClient(t, server)
	clientC := testClient(t, server)
	providerA := NewRemoteProvider(clientA)
	providerB := NewRemoteProvider(testClient(t, server))
	ctx := context.Background()
	id := ids.New()
	info, err := clientA.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Kill(context.Background(), id) })
	requests := []base.DurableRenameOptions{
		{ID: id, Incarnation: info.Incarnation, RequestID: ids.New(), Title: "from-A"},
		{ID: id, Incarnation: info.Incarnation, RequestID: ids.New(), Title: "from-B"},
	}
	providers := []base.DurableMetadataProvider{providerA, providerB}
	start := make(chan struct{})
	type outcome struct {
		result base.DurableRenameResult
		err    error
	}
	outcomes := make([]outcome, len(requests))
	var wg sync.WaitGroup
	for index := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := providers[index].RenameDurable(ctx, requests[index])
			outcomes[index] = outcome{result: result, err: err}
		}()
	}
	close(start)
	wg.Wait()

	succeeded := -1
	conflicts := 0
	for index, outcome := range outcomes {
		if outcome.err == nil {
			if succeeded >= 0 {
				t.Fatalf("both concurrent renames succeeded: %+v", outcomes)
			}
			succeeded = index
			if outcome.result.Title != requests[index].Title || outcome.result.MetadataRevision != 1 {
				t.Fatalf("winner %d = %+v", index, outcome.result)
			}
			continue
		}
		var conflict *base.DurableMetadataConflictError
		if !errors.As(outcome.err, &conflict) {
			t.Fatalf("loser %d = %v, want metadata conflict", index, outcome.err)
		}
		conflicts++
		if conflict.CurrentTitle != requests[1-index].Title || conflict.CurrentMetadataRevision != 1 {
			t.Fatalf("loser %d conflict = %+v", index, conflict)
		}
	}
	if succeeded < 0 || conflicts != 1 {
		t.Fatalf("concurrent outcomes = %+v", outcomes)
	}
	infos, err := clientC.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].Title != requests[succeeded].Title || infos[0].MetadataRevision != 1 {
		t.Fatalf("C metadata after concurrent rename = %+v, %v", infos, err)
	}
}

func TestClientRenameRetryAfterLostResponse(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()
	id := ids.New()
	info, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Kill(context.Background(), id) })
	request := RenameRequest{ID: id, Incarnation: info.Incarnation, RequestID: ids.New(), Title: "persisted"}
	conn := dialServer(t, server.SocketPath())
	handshakeConn(t, conn, supervisor.StateDir())
	payload, err := marshalFrame(frameRename, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameRename, payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	result, err := client.Rename(ctx, request)
	if err != nil || result.Title != request.Title || result.MetadataRevision != 1 {
		t.Fatalf("rename reconciliation = %+v, %v", result, err)
	}
	retry, err := client.Rename(ctx, request)
	if err != nil || retry.MetadataRevision != result.MetadataRevision {
		t.Fatalf("same request retry = %+v, %v, first = %+v", retry, err, result)
	}
	infos, err := client.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].MetadataRevision != 1 {
		t.Fatalf("metadata after retries = %+v, %v", infos, err)
	}
}

func TestDurableKillRetriesValidateKilledIncarnation(t *testing.T) {
	for _, name := range []string{"embedded", "remote"} {
		t.Run(name, func(t *testing.T) {
			supervisor := testSupervisor(t)
			server := testServer(t, supervisor)
			var provider base.DurableProvider
			if name == "embedded" {
				provider = NewProvider(supervisor)
			} else {
				provider = NewRemoteProvider(testClient(t, server))
			}
			killer, ok := provider.(base.DurableIdentityKiller)
			if !ok {
				t.Fatalf("%T does not expose checked kill", provider)
			}
			ctx := context.Background()
			id := ids.New()
			options := CreateOptions{ID: id, Command: []string{"/bin/sh", "-c", "sleep 30"}, Env: []string{"TERM=xterm-256color", "LC_ALL=C"}}
			t.Cleanup(func() { _ = supervisor.Kill(context.Background(), id) })

			original, err := supervisor.Create(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			identityA := base.DurableIdentity{CreatedAt: original.Info().CreatedAt, Incarnation: original.Identity().Incarnation}
			if err := killer.KillWithIdentity(ctx, id, identityA); err != nil {
				t.Fatalf("kill A = %v", err)
			}
			wrong := base.DurableIdentity{CreatedAt: identityA.CreatedAt, Incarnation: ids.New()}
			if err := killer.KillWithIdentity(ctx, id, wrong); !errors.Is(err, durable.ErrIdentity) {
				t.Fatalf("retry kill A as B = %v, want identity changed", err)
			}
			if err := killer.KillWithIdentity(ctx, id, identityA); err != nil {
				t.Fatalf("idempotent retry kill A = %v", err)
			}
			if name == "embedded" {
				err = supervisor.Kill(ctx, id)
			} else {
				err = testClient(t, server).Kill(ctx, id, nil)
			}
			if err != nil {
				t.Fatalf("legacy nil-identity retry = %v", err)
			}

			replacement, err := supervisor.Create(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			identityB := base.DurableIdentity{CreatedAt: replacement.Info().CreatedAt, Incarnation: replacement.Identity().Incarnation}
			if err := killer.KillWithIdentity(ctx, id, identityB); err != nil {
				t.Fatalf("kill B = %v", err)
			}
			if err := killer.KillWithIdentity(ctx, id, identityB); err != nil {
				t.Fatalf("idempotent retry kill B = %v", err)
			}
			if err := killer.KillWithIdentity(ctx, id, identityA); !errors.Is(err, durable.ErrIdentity) {
				t.Fatalf("retry kill B as A = %v, want identity changed", err)
			}
		})
	}
}

func TestDurableIdentityProvidersRejectStaleIncarnation(t *testing.T) {
	for _, name := range []string{"embedded", "remote"} {
		t.Run(name, func(t *testing.T) {
			supervisor := testSupervisor(t)
			server := testServer(t, supervisor)
			var provider base.DurableProvider
			if name == "embedded" {
				provider = NewProvider(supervisor)
			} else {
				provider = NewRemoteProvider(testClient(t, server))
			}
			ctx := context.Background()
			id := ids.New()
			options := CreateOptions{ID: id, Command: []string{"/bin/sh", "-c", "sleep 30"}, Env: []string{"TERM=xterm-256color", "LC_ALL=C"}}
			original, err := supervisor.Create(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			stale := base.DurableIdentity{CreatedAt: original.Info().CreatedAt, Incarnation: original.Identity().Incarnation}
			if err := supervisor.Kill(ctx, id); err != nil {
				t.Fatal(err)
			}
			replacement, err := supervisor.Create(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			current := base.DurableIdentity{CreatedAt: replacement.Info().CreatedAt, Incarnation: replacement.Identity().Incarnation}
			t.Cleanup(func() { _ = supervisor.Kill(context.Background(), id) })

			attacher, ok := provider.(base.DurableIdentityAttacher)
			if !ok {
				t.Fatalf("%T does not expose checked attach", provider)
			}
			killer, ok := provider.(base.DurableIdentityKiller)
			if !ok {
				t.Fatalf("%T does not expose checked kill", provider)
			}
			if attachment, err := attacher.AttachWithIdentity(ctx, id, stale); attachment != nil || !errors.Is(err, durable.ErrIdentity) {
				t.Fatalf("stale AttachWithIdentity = %v, %v", attachment, err)
			}
			if err := killer.KillWithIdentity(ctx, id, stale); !errors.Is(err, durable.ErrIdentity) {
				t.Fatalf("stale KillWithIdentity = %v, want identity changed", err)
			}
			if attachment, err := attacher.AttachWithIdentity(ctx, id, base.DurableIdentity{}); attachment != nil || !errors.Is(err, durable.ErrInvalidInput) {
				t.Fatalf("attach without incarnation = %v, %v, want invalid input", attachment, err)
			}

			attachment, err := attacher.AttachFromWithIdentity(ctx, id, current, 0)
			if err != nil {
				t.Fatalf("current AttachFromWithIdentity = %v", err)
			}
			_ = attachment.Close()
			if err := killer.KillWithIdentity(ctx, id, current); err != nil {
				t.Fatalf("current KillWithIdentity = %v", err)
			}
			if err := killer.KillWithIdentity(ctx, id, current); err != nil {
				t.Fatalf("repeated KillWithIdentity = %v", err)
			}
		})
	}
}

func dialServer(t *testing.T, socketPath string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func awaitFrame(t *testing.T, conn net.Conn, kind frameType) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		got, payload, err := readFrame(conn)
		if err != nil {
			t.Fatalf("await frame %d: %v", kind, err)
		}
		if got == kind {
			return payload
		}
		// Output and attention-state frames are pushed asynchronously while attached.
		if got != frameOutput && got != frameState {
			t.Fatalf("await frame %d: got unexpected frame %d", kind, got)
		}
	}
}
