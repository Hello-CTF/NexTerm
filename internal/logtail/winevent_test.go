package logtail

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

func TestWinEventCommandScriptMatchesGolden(t *testing.T) {
	command, err := sourceCommand(Source{Kind: SourceWinEvent, Value: "System"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(command, "powershell -NoProfile -NonInteractive -EncodedCommand ") {
		t.Fatalf("command = %q", command)
	}
	script, err := psquote.DecodeCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "winevent.golden.ps1")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if script != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("script differs from %s; rerun with -update if intended:\n%s", path, script)
	}
}

func TestWinEventCommandBindsChannelAsLiteral(t *testing.T) {
	command, err := sourceCommand(Source{Kind: SourceWinEvent, Value: "Microsoft-Windows-PowerShell/Operational"})
	if err != nil {
		t.Fatal(err)
	}
	script, err := psquote.DecodeCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(script, "$log='Microsoft-Windows-PowerShell/Operational'\n") {
		t.Fatalf("script does not start with the channel literal: %q", script[:60])
	}
}

func TestWinEventCommandRejectsUnsafeChannels(t *testing.T) {
	for _, value := range []string{"System'; calc; '", "Sys*", "$env:TEMP", "a\"b", "-System", "", strings.Repeat("x", 129), "Sys`tem"} {
		if _, err := sourceCommand(Source{Kind: SourceWinEvent, Value: value}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("channel %q error = %v, want ErrInvalid", value, err)
		}
	}
}

func TestServiceStreamsWindowsEventLinesAndKeepsAccessDeniedVisible(t *testing.T) {
	transport := newFakeStreamTransport()
	service := New(Config{Resolver: ResolverFunc(func(_ context.Context, id string) (Target, error) {
		return Target{SessionID: id, SessionName: "win", Platform: PlatformWindows, Transport: transport, Generation: 7}, nil
	})})
	sink := newRecordingSink()
	source := Source{SourceID: "ev", SessionID: "win1", Kind: SourceWinEvent, Value: "Security"}
	if _, err := service.Start(t.Context(), "owner", Request{RunID: "ev-run", Sources: []Source{source}}, sink); err != nil {
		t.Fatal(err)
	}
	command := <-transport.commands
	if script, err := psquote.DecodeCommand(command); err != nil || !strings.HasPrefix(script, "$log='Security'\n") {
		t.Fatalf("command script = %q, %v", script, err)
	}
	channel := <-transport.opened
	channel.send("2026-11-10 10:00:00 [Error] Service Control Manager (id 7034, rec 12): The service terminated\r\n", false)
	channel.send("access denied: the account cannot read the event log Security\n", true)
	_ = channel.Close()
	events := sink.waitFor(t, hasDone)
	var line, denied bool
	for _, event := range events {
		line = line || event.Kind == EventLine && !event.Stderr && strings.HasSuffix(event.Text, "The service terminated")
		denied = denied || event.Kind == EventLine && event.Stderr && strings.HasPrefix(event.Text, "access denied")
	}
	if !line || !denied {
		t.Fatalf("line = %v, denied = %v; events: %+v", line, denied, events)
	}
}

func TestServiceRefusesSourcesThatDoNotMatchTheKnownPlatform(t *testing.T) {
	for _, test := range []struct {
		platform string
		kind     SourceKind
		value    string
	}{
		{PlatformWindows, SourceFile, "C:/x.log"},
		{PlatformWindows, SourceDocker, "web"},
		{PlatformPOSIX, SourceWinEvent, "System"},
	} {
		transport := newFakeStreamTransport()
		service := New(Config{Resolver: ResolverFunc(func(_ context.Context, id string) (Target, error) {
			return Target{SessionID: id, Platform: test.platform, Transport: transport, Generation: 7}, nil
		})})
		sink := newRecordingSink()
		source := Source{SourceID: "s", SessionID: "h", Kind: test.kind, Value: test.value}
		if _, err := service.Start(t.Context(), "owner", Request{RunID: "mismatch", Sources: []Source{source}}, sink); err != nil {
			t.Fatal(err)
		}
		events := sink.waitFor(t, hasDone)
		var failed bool
		for _, event := range events {
			failed = failed || event.Kind == EventStatus && event.Status == StatusFailed && event.Error != ""
		}
		if !failed || len(transport.commands) != 0 {
			t.Fatalf("%s/%s: failed = %v, commands = %d", test.platform, test.kind, failed, len(transport.commands))
		}
	}
}
