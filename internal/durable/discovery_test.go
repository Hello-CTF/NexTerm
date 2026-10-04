package durable

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

type multiSessionTmux struct {
	backend  *Backend
	sessions [][]record
}

func (m *multiSessionTmux) run(_ context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 || args[0] != "list-panes" {
		return nil, nil
	}
	all := false
	target := ""
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "-a":
			all = true
		case "-t":
			index++
			target = args[index]
		}
	}
	var output strings.Builder
	write := func(panes []record) {
		for _, current := range panes {
			output.WriteString(discoveryLine(m.backend, current))
		}
	}
	switch {
	case all:
		for _, panes := range m.sessions {
			write(panes)
		}
	case target != "":
		for _, panes := range m.sessions {
			if len(panes) > 0 && panes[0].name == target {
				write(panes)
			}
		}
	default:
		if len(m.sessions) > 0 {
			write(m.sessions[0])
		}
	}
	return []byte(output.String()), nil
}

func multiSessionPair(backend *Backend) (first, second record) {
	first = unitRecord(backend, ids.New())
	second = unitRecord(backend, ids.New())
	second.info.SessionID = "$2"
	second.info.PaneID = "%2"
	second.windowID = "@2"
	second.paneOption = "%2"
	second.windowOption = "@2"
	return first, second
}

func TestListEnumeratesEveryTmuxSession(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	first, second := multiSessionPair(backend)
	foreign := unitRecord(backend, ids.New())
	foreign.info.SessionID = "$9"
	foreign.info.PaneID = "%9"
	foreign.windowID = "@9"
	foreign.owner = ""
	foreign.namespace = ""
	foreign.paneOption = ""
	foreign.windowOption = ""
	server := &multiSessionTmux{backend: backend, sessions: [][]record{{first}, {second}, {foreign}}}
	runner.handler = server.run

	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("List returned %d infos, want both owned sessions: %+v", len(infos), infos)
	}
	byID := make(map[string]Info, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}
	for _, want := range []Info{first.info, second.info} {
		got, ok := byID[want.ID]
		if !ok {
			t.Fatalf("owned session %s missing from %+v", want.ID, infos)
		}
		if !sameIdentity(want, got) {
			t.Fatalf("session %s identity = %+v, want %+v", want.ID, got, want)
		}
	}
	calls := runner.recordedCalls()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0][:3], []string{"list-panes", "-a", "-F"}) {
		t.Fatalf("discovery calls = %+v", calls)
	}
}

func TestAttachmentBeyondCurrentTmuxSessionDrains(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	first, second := multiSessionPair(backend)
	second.recordingLive = false
	server := &multiSessionTmux{backend: backend, sessions: [][]record{{first}, {second}}}
	runner.handler = server.run
	if err := os.Mkdir(backend.sessionDir(second.info.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recordingPath(second.info.ID), []byte("second-output:"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderClosingPath(second.info.ID), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderDonePath(second.info.ID), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := backend.Attach(context.Background(), second.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Detach()
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "second-output:" {
		t.Fatalf("attachment output = %q, want the full second-session recording", output)
	}
}

func TestKillBeyondCurrentTmuxSessionKillsOwnTmuxSession(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	first, second := multiSessionPair(backend)
	server := &multiSessionTmux{backend: backend, sessions: [][]record{{first}, {second}}}
	runner.handler = server.run
	if err := os.Mkdir(backend.sessionDir(second.info.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recordingPath(second.info.ID), []byte("raw"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderDonePath(second.info.ID), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := backend.Kill(context.Background(), second.info.ID); err != nil {
		t.Fatal(err)
	}
	var kills [][]string
	for _, call := range runner.recordedCalls() {
		if call[0] == "kill-session" {
			kills = append(kills, call)
		}
	}
	if len(kills) != 1 || !reflect.DeepEqual(kills[0], []string{"kill-session", "-t", second.info.SessionID}) {
		t.Fatalf("kill-session calls = %+v, want exactly one targeting %s", kills, second.info.SessionID)
	}
	if _, err := os.Lstat(backend.sessionDir(second.info.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second session artifacts remain: %v", err)
	}
}
