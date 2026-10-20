package docker

import (
	"strings"
	"testing"
)

func TestCommandBackendDockerContextPinsEveryAction(t *testing.T) {
	runner := &recordingRunner{result: CommandResult{Stdout: "{}\n"}}
	backend := NewCommandBackend(runner, nil, ShellPOSIX, WithDockerContext("prod"))
	if _, err := backend.ListContainerDTOs(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := backend.Action(t.Context(), ActionOptions{Container: "web", Action: ActionStop}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.ExecOnce(t.Context(), ExecOptions{Container: "web", Cmd: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.OpenLogs(t.Context(), LogsOptions{Container: "web", Tail: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.ListImageDTOs(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker '--context' 'prod' 'ps' '-a' '--format' '{{json .}}'",
		"docker '--context' 'prod' 'stop' 'web'",
		"docker '--context' 'prod' 'exec' 'web' 'true'",
		"docker '--context' 'prod' 'logs' '--tail' '10' 'web'",
		"docker '--context' 'prod' 'images' '--format' '{{json .}}'",
	}
	if strings.Join(runner.commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %q, want %q", runner.commands, want)
	}
}

func TestCommandBackendDockerContextAppliesToStreamsAndCompose(t *testing.T) {
	opener := &recordingOpener{stream: &fakeCommandStream{}}
	runner := &recordingRunner{}
	backend := NewCommandBackend(runner, opener, ShellPOSIX, WithDockerContext("prod"))
	if _, err := backend.OpenExec(t.Context(), ExecOptions{Container: "web", Cmd: []string{"sh"}, TTY: true}); err != nil {
		t.Fatal(err)
	}
	if want := "docker '--context' 'prod' 'exec' '-it' 'web' 'sh'"; opener.commands[0] != want {
		t.Fatalf("exec stream command = %q, want %q", opener.commands[0], want)
	}
	if _, err := backend.RunCompose(t.Context(), ComposeCLIPlugin, ComposeStack{
		Project: "shop", WorkingDir: "/srv/shop", ConfigFiles: []string{"/srv/shop/compose.yml"},
	}, ComposeActionDown); err != nil {
		t.Fatal(err)
	}
	if want := "docker '--context' 'prod' 'compose' '--project-directory' '/srv/shop' '--file' '/srv/shop/compose.yml' '--project-name' 'shop' 'down'"; runner.commands[0] != want {
		t.Fatalf("compose command = %q, want %q", runner.commands[0], want)
	}
	legacyRunner := &recordingRunner{}
	legacy := NewCommandBackend(legacyRunner, nil, ShellPOSIX, WithDockerContext("prod"))
	if _, err := legacy.RunCompose(t.Context(), ComposeCLILegacy, ComposeStack{
		Project: "shop", WorkingDir: "/srv/shop", ConfigFiles: []string{"/srv/shop/compose.yml"},
	}, ComposeActionDown); err != nil {
		t.Fatal(err)
	}
	if want := "docker-compose '--project-directory' '/srv/shop' '--file' '/srv/shop/compose.yml' '--project-name' 'shop' 'down'"; legacyRunner.commands[0] != want {
		t.Fatalf("legacy compose command = %q, want %q", legacyRunner.commands[0], want)
	}
}

func TestCommandBackendWithoutContextKeepsCommandsUnchanged(t *testing.T) {
	for _, options := range [][]CommandBackendOption{nil, {WithDockerContext("")}} {
		runner := &recordingRunner{result: CommandResult{Stdout: "{}\n"}}
		backend := NewCommandBackend(runner, nil, ShellPOSIX, options...)
		if _, err := backend.ListContainerDTOs(t.Context(), false); err != nil {
			t.Fatal(err)
		}
		if want := "docker 'ps' '--format' '{{json .}}'"; runner.commands[0] != want {
			t.Fatalf("commands = %q, want %q", runner.commands, want)
		}
	}
}

func TestCommandBackendDockerContextPowerShellQuoting(t *testing.T) {
	runner := &recordingRunner{result: CommandResult{Stdout: "{}\n"}}
	backend := NewCommandBackend(runner, nil, ShellPowerShell, WithDockerContext("prod"))
	if _, err := backend.ListContainerDTOs(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if want := "docker '--context' 'prod' 'ps' '--format' '{{json .}}'"; runner.commands[0] != want {
		t.Fatalf("commands = %q, want %q", runner.commands, want)
	}
}
