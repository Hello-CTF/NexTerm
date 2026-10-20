package production

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type generatedIPCManifest struct {
	Commands map[string]generatedIPCCommand `json:"commands"`
}

type generatedIPCCommand struct {
	Nested  bool   `json:"nested"`
	Channel bool   `json:"channel"`
	Stream  string `json:"stream"`
}

func readGeneratedIPCManifest(t *testing.T) generatedIPCManifest {
	t.Helper()
	path := filepath.Join("..", "..", "..", "src", "ipc", "generated", "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest generatedIPCManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Commands) == 0 {
		t.Fatal("generated IPC manifest contains no commands")
	}
	return manifest
}

var desktopOnlyProductionCommands = []string{"local_file_read", "local_file_read_range", "local_folder_list", "log_tail_save"}

func commandSet(commands []string) map[string]bool {
	set := make(map[string]bool, len(commands))
	for _, command := range commands {
		set[command] = true
	}
	return set
}

func assertCommandSet(t *testing.T, label string, actual []string, expected map[string]bool) {
	t.Helper()
	actualSet := commandSet(actual)
	var missing []string
	for command := range expected {
		if !actualSet[command] {
			missing = append(missing, command)
		}
	}
	var unexpected []string
	for command := range actualSet {
		if !expected[command] {
			unexpected = append(unexpected, command)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	if len(missing) > 0 || len(unexpected) > 0 {
		t.Fatalf("%s command set mismatch: missing=%v unexpected=%v", label, missing, unexpected)
	}
}

func TestProductionCommandsMatchGeneratedManifest(t *testing.T) {
	server, err := NewProduction(t.Context(), ProductionConfig{DataDir: t.TempDir(), Desktop: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	desktop, err := NewProduction(t.Context(), ProductionConfig{DataDir: t.TempDir(), Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := desktop.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = desktop.Shutdown(context.Background()) })

	manifest := readGeneratedIPCManifest(t)
	desktopCommands := make(map[string]bool, len(manifest.Commands))
	serverCommands := make(map[string]bool, len(manifest.Commands))
	desktopOnly := commandSet(desktopOnlyProductionCommands)
	for command := range manifest.Commands {
		desktopCommands[command] = true
		if !desktopOnly[command] {
			serverCommands[command] = true
		}
	}
	assertCommandSet(t, "desktop production", desktop.Dispatcher.Commands(), desktopCommands)
	assertCommandSet(t, "server production", server.Dispatcher.Commands(), serverCommands)
}

func TestGeneratedManifestEnvelopeMetadata(t *testing.T) {
	manifest := readGeneratedIPCManifest(t)
	for _, test := range []struct {
		name    string
		nested  bool
		channel bool
		stream  string
	}{
		{name: "app_platform", stream: "none"},
		{name: "db_connect", nested: true, stream: "none"},
		{name: "db_disconnect", stream: "none"},
		{name: "session_connect", nested: true, stream: "none"},
		{name: "terminal_attach", channel: true, stream: "binary"},
		{name: "terminal_broadcast_prepare", nested: true, stream: "none"},
		{name: "host_share_terminal_open", nested: true, channel: true, stream: "json"},
	} {
		command, ok := manifest.Commands[test.name]
		if !ok {
			t.Fatalf("generated command %q is missing", test.name)
		}
		if command.Nested != test.nested || command.Channel != test.channel || command.Stream != test.stream {
			t.Fatalf("%s metadata = nested:%t channel:%t stream:%s, want nested:%t channel:%t stream:%s",
				test.name, command.Nested, command.Channel, command.Stream, test.nested, test.channel, test.stream)
		}
	}
}
