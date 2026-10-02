package durable

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestAliveClosingSessionDrainsWithoutFailure(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	current.recordingLive = false
	discoveries := 0
	runner.handler = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 || args[0] != "list-panes" {
			return nil, nil
		}
		discoveries++
		if discoveries == 3 {
			file, err := os.OpenFile(backend.recordingPath(current.info.ID), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return nil, err
			}
			if _, err := file.Write([]byte("final")); err != nil {
				_ = file.Close()
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
			if err := os.WriteFile(backend.recorderDonePath(current.info.ID), []byte("0\n"), 0o600); err != nil {
				return nil, err
			}
		}
		return []byte(discoveryLine(backend, current)), nil
	}
	if err := os.Mkdir(backend.sessionDir(current.info.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recordingPath(current.info.ID), []byte("old:"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderClosingPath(current.info.ID), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := backend.Attach(context.Background(), current.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Detach()
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "old:final" {
		t.Fatalf("closing output = %q, discoveries = %d", output, discoveries)
	}
	if discoveries < 3 {
		t.Fatalf("completion preceded delayed recorder metadata: discoveries = %d", discoveries)
	}
}

func TestReadRejectsUnexpectedStoppedRecorder(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	current.recordingLive = false
	installRecords(backend, runner, current)
	session := newUnitSession(t, backend, current, nil)
	if _, err := session.Read(make([]byte, 1)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Read error = %v, want ErrUnavailable", err)
	}
}

func TestKillRetryAfterSessionGoneCleansArtifacts(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	killed := false
	runner.handler = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, nil
		}
		switch args[0] {
		case "kill-session":
			killed = true
			return nil, nil
		case "list-panes":
			if killed {
				return nil, nil
			}
			return []byte(discoveryLine(backend, current)), nil
		default:
			return nil, nil
		}
	}
	if err := os.Mkdir(backend.sessionDir(current.info.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recordingPath(current.info.ID), []byte("raw"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderDonePath(current.info.ID), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backend.Kill(context.Background(), current.info.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backend.sessionDir(current.info.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderDonePath(current.info.ID), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderDoneTempPath(current.info.ID), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := backend.Kill(context.Background(), current.info.ID); err != nil {
			t.Fatalf("Kill retry %d: %v", attempt+1, err)
		}
	}
	for _, path := range []string{backend.sessionDir(current.info.ID), backend.tombstonePath(current.info.ID)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact path remains after retries: %s: %v", path, err)
		}
	}
	killCalls := 0
	for _, call := range runner.recordedCalls() {
		if call[0] == "kill-session" {
			killCalls++
		}
	}
	if killCalls != 1 {
		t.Fatalf("kill-session calls = %d, want 1", killCalls)
	}
}

func TestKillRecoversTombstoneAfterRename(t *testing.T) {
	metadataCases := []struct {
		name    string
		data    []byte
		present bool
	}{
		{name: "present", data: []byte("0\n"), present: true},
		{name: "absent"},
		{name: "malformed", data: []byte("not-an-exit-status\n"), present: true},
	}
	for _, restart := range []bool{false, true} {
		recovery := "cleanup-retry"
		if restart {
			recovery = "daemon-restart"
		}
		for _, metadata := range metadataCases {
			t.Run(recovery+"/"+metadata.name, func(t *testing.T) {
				backend, runner := newUnitBackend(t)
				touchUnitSocket(t, backend)
				id := ids.New()
				if err := os.Mkdir(backend.sessionDir(id), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(backend.recordingPath(id), []byte("raw"), 0o600); err != nil {
					t.Fatal(err)
				}
				if metadata.present {
					if err := os.WriteFile(backend.recorderDonePath(id), metadata.data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(backend.recorderDoneTempPath(id), []byte("pending\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(backend.sessionDir(id), backend.tombstonePath(id)); err != nil {
					t.Fatal(err)
				}

				active := backend
				activeRunner := runner
				if restart {
					var err error
					active, err = New(Config{
						Binary:         backend.binary,
						SocketPath:     backend.socketPath,
						StateDir:       backend.stateDir,
						Namespace:      backend.namespace,
						CommandTimeout: backend.commandTimeout,
					})
					if err != nil {
						t.Fatal(err)
					}
					activeRunner = &fakeRunner{}
					active.runner = activeRunner.run
				}
				installRecords(active, activeRunner)
				for attempt := 0; attempt < 2; attempt++ {
					ctx, cancel := context.WithTimeout(context.Background(), active.commandTimeout/10)
					err := active.Kill(ctx, id)
					cancel()
					if err != nil {
						t.Fatalf("Kill attempt %d: %v", attempt+1, err)
					}
					for _, path := range []string{active.sessionDir(id), active.tombstonePath(id)} {
						if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("artifact remains after Kill attempt %d at %s: %v", attempt+1, path, err)
						}
					}
				}
				for _, call := range activeRunner.recordedCalls() {
					if len(call) > 0 && call[0] == "kill-session" {
						t.Fatalf("tombstone recovery issued kill-session: %v", call)
					}
				}
			})
		}
	}
}
