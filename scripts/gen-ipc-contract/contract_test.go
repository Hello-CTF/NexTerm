package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGeneratedContractMetadata(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)

	tests := []struct {
		name    string
		nested  bool
		channel bool
		stream  string
	}{
		{name: "host_share_terminal_open", nested: true, channel: true, stream: "json"},
		{name: "host_share_terminal_input", nested: true, stream: "none"},
		{name: "host_share_terminal_close", nested: true, stream: "none"},
		{name: "exec_batch_execute", stream: "none"},
		{name: "exec_batch_cancel", stream: "none"},
		{name: "forward_rule_list", stream: "none"},
		{name: "fs_cancel", stream: "none"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Nested != test.nested || command.Channel != test.channel || command.Stream != test.stream {
				t.Fatalf("metadata = nested:%t channel:%t stream:%s, want nested:%t channel:%t stream:%s",
					command.Nested, command.Channel, command.Stream, test.nested, test.channel, test.stream)
			}
			if typeIdentifier(command.Input) == "" || typeIdentifier(command.Output) == "" {
				t.Fatal("input/output Go type identifiers must be present")
			}
		})
	}

	if got := typeIdentifier(commands["exec_batch_execute"].Input); got != "github.com/Hello-CTF/NexTerm/internal/execbatch.Request" {
		t.Fatalf("exec_batch_execute input = %q", got)
	}
	if got := typeIdentifier(commands["host_share_terminal_open"].Output); got != "github.com/Hello-CTF/NexTerm/internal/sync.RemoteOK" {
		t.Fatalf("host_share_terminal_open output = %q", got)
	}
	if got := typeIdentifier(commands["fs_cancel"].Input); got != "github.com/Hello-CTF/NexTerm/internal/app/production.fsCancelRequest" {
		t.Fatalf("fs_cancel input = %q", got)
	}
	for _, name := range []string{"app_platform", "db_connect"} {
		if legacy, ok := commands[name]; !ok || legacy.Input != nil || legacy.Scope != "" {
			t.Fatalf("legacy command %q must have a name without DTO metadata: %+v", name, legacy)
		}
	}
	for _, test := range []struct {
		name    string
		nested  bool
		channel bool
		stream  string
	}{
		{name: "app_platform", stream: "none"},
		{name: "db_connect", nested: true, stream: "none"},
		{name: "db_disconnect", stream: "none"},
		{name: "redis_command", stream: "none"},
		{name: "session_connect", nested: true, stream: "none"},
		{name: "terminal_attach", channel: true, stream: "binary"},
	} {
		command, ok := commands[test.name]
		if !ok {
			t.Fatalf("legacy command %q is missing", test.name)
		}
		if command.Nested != test.nested || command.Channel != test.channel || command.Stream != test.stream {
			t.Fatalf("legacy metadata = nested:%t channel:%t stream:%s, want nested:%t channel:%t stream:%s",
				command.Nested, command.Channel, command.Stream, test.nested, test.channel, test.stream)
		}
	}

	registry := newTypeRegistry()
	listOutput, err := registry.ref(commands["forward_rule_list"].Output)
	if err != nil {
		t.Fatal(err)
	}
	if listOutput.Manifest != "contractTypes.ForwardRule[]" {
		t.Fatalf("forward_rule_list manifest output = %q", listOutput.Manifest)
	}
	deleteOutput, err := registry.ref(commands["forward_rule_delete"].Output)
	if err != nil {
		t.Fatal(err)
	}
	if deleteOutput.TS != "unknown" || deleteOutput.Manifest != "unknown" {
		t.Fatalf("any output must remain unknown, got %+v", deleteOutput)
	}

	manifest := string(generated.files["manifest.ts"])
	if !strings.Contains(manifest, "streamPayload: \"unknown\"") {
		t.Fatal("JSON stream payload must remain unknown")
	}
	for _, deferred := range []string{"memory-owner", "ai-owner"} {
		if !strings.Contains(manifest, strconvQuote(deferred)) {
			t.Fatalf("deferred scope %q is missing", deferred)
		}
	}

	events := make(map[string]string, len(generated.events))
	for _, event := range generated.events {
		events[event.Key] = event.Topic
	}
	wantEvents := map[string]string{
		"aiEvent": "ai://event", "aiNotification": "ai://notification", "aiWatchToolData": "ai://watch-tool-data",
		"appError": "app://error", "deepLink": "app://deep-link",
		"connectProgress": "session://connect-progress", "deviceStatus": "device://status",
		"dockerStats": "docker://stats", "filesDropped": "files://dropped",
		"fsProgress": "fs://progress", "helperProgress": "session://helper-progress",
		"layoutChanged": "layout://changed", "sessionStatus": "session://status",
		"inventoryChanged":  "inventory://changed",
		"syncRoundComplete": "sync://round-complete", "syncStatus": "sync://status",
		"terminalControl": "terminal://control", "terminalExit": "terminal://exit",
		"terminalThrottled": "terminal://throttled", "updateProgress": "update://progress",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("event topics = %v, want %v", events, wantEvents)
	}

	renderedAgain, err := renderContracts(generated.commands, generated.events)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(renderedAgain, generated.files) {
		t.Fatal("rendering the same metadata is not deterministic")
	}
}

func TestConsumedJSONFieldRenameFailsDriftCheck(t *testing.T) {
	baseline := generateForTest(t, nil)
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "execbatch", "types.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changedSource := bytes.Replace(source, []byte(`json:"batchId"`), []byte(`json:"batchID"`), 1)
	if bytes.Equal(source, changedSource) {
		t.Fatal("exec batch request batchId tag was not found")
	}
	changed := generateForTest(t, map[string][]byte{path: changedSource})
	if bytes.Equal(baseline.files["types.ts"], changed.files["types.ts"]) {
		t.Fatal("renaming a consumed JSON field did not change generated types")
	}
	if !bytes.Contains(changed.files["types.ts"], []byte("batchID: string;")) {
		t.Fatal("renamed JSON field is absent from generated types")
	}

	outputDir := t.TempDir()
	if err := writeGenerated(outputDir, baseline.files); err != nil {
		t.Fatal(err)
	}
	if err := checkGenerated(outputDir, baseline.files); err != nil {
		t.Fatalf("fresh baseline failed check: %v", err)
	}
	err = checkGenerated(outputDir, changed.files)
	if err == nil || !strings.Contains(err.Error(), "types.ts") {
		t.Fatalf("stale generated types were not rejected: %v", err)
	}
}

func TestLegacyCommandRatchet(t *testing.T) {
	commands := make([]commandContract, legacyCommandLimit)
	if err := checkLegacyCommandRatchet(commands); err != nil {
		t.Fatalf("ratchet rejected the current limit: %v", err)
	}
	commands = append(commands, commandContract{Name: "new_legacy"})
	if err := checkLegacyCommandRatchet(commands); err == nil || !strings.Contains(err.Error(), "exceeds ratchet") {
		t.Fatalf("ratchet accepted legacy growth: %v", err)
	}
}

// The production settings commands are a typed scope: they carry DTO metadata
// and stay out of the legacy surface, so the ratchet holds at 265 without
// hiding any other registration.
func TestProductionSettingsCommandsAreTypedScope(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	for _, name := range []string{"production_settings_get", "production_settings_set"} {
		command, ok := commands[name]
		if !ok {
			t.Fatalf("production settings command %q is missing", name)
		}
		if command.Scope != "production" {
			t.Fatalf("command %q scope = %q, want production", name, command.Scope)
		}
		if command.Input == nil || command.Output == nil {
			t.Fatalf("command %q must carry DTO metadata: %+v", name, command)
		}
	}
	if got := typeIdentifier(commands["production_settings_get"].Output); got != "github.com/Hello-CTF/NexTerm/internal/app/production.productionSettingsDTO" {
		t.Fatalf("production_settings_get output = %q", got)
	}
	if got := typeIdentifier(commands["production_settings_set"].Input); got != "github.com/Hello-CTF/NexTerm/internal/app/production.productionSettingsUpdateRequest" {
		t.Fatalf("production_settings_set input = %q", got)
	}
	legacy := 0
	for _, command := range generated.commands {
		if command.Input == nil {
			legacy++
		}
	}
	if legacy > legacyCommandLimit {
		t.Fatalf("legacy count %d exceeds ratchet %d after typing production settings", legacy, legacyCommandLimit)
	}
}

// The published contract is derived from the production wiring: a command
// enters the manifest only when its registration package is reachable from a
// shipped binary. Registration-ready packages that no binary imports yet are
// deferred and stay out of the published output and the ratchet.
func TestUnwiredRegistrationsStayOutOfThePublishedContract(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	// The AI skills service is wired into the production builder now, so its
	// commands publish under the ai-skills typed scope with DTO metadata.
	for _, name := range []string{"ai_skills_list", "ai_skills_dispatch", "ai_skills_material", "ai_skills_save_override", "ai_skills_delete_override"} {
		command, ok := commands[name]
		if !ok {
			t.Fatalf("wired command %q is missing", name)
		}
		if command.Scope != "ai-skills" {
			t.Fatalf("command %q scope = %q, want ai-skills", name, command.Scope)
		}
		if command.Input == nil || command.Output == nil {
			t.Fatalf("command %q must carry DTO metadata: %+v", name, command)
		}
	}
	// Real production commands stay published: deferred registration must
	// not exclude them.
	for _, name := range []string{"app_platform", "session_connect", "inventory_target_exec", "terminal_stream_ack"} {
		if _, ok := commands[name]; !ok {
			t.Fatalf("production command %q was wrongly excluded", name)
		}
	}
	// The ratchet sees only the published legacy commands.
	legacy := 0
	for _, command := range generated.commands {
		if command.Input == nil {
			legacy++
		}
	}
	if legacy > legacyCommandLimit {
		t.Fatalf("published legacy count %d exceeds ratchet %d", legacy, legacyCommandLimit)
	}
}

func TestCheckGeneratedIgnoresOSMetadataAndRejectsStrayFiles(t *testing.T) {
	outputDir := t.TempDir()
	files := map[string][]byte{"manifest.ts": []byte("generated\n")}
	if err := writeGenerated(outputDir, files); err != nil {
		t.Fatal(err)
	}
	if err := checkGenerated(outputDir, files); err != nil {
		t.Fatalf("fresh output failed check: %v", err)
	}
	for _, name := range []string{".DS_Store", "Thumbs.db", "desktop.ini"} {
		if err := os.WriteFile(filepath.Join(outputDir, name), []byte("metadata\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := checkGenerated(outputDir, files); err != nil {
			t.Fatalf("OS metadata %q was rejected: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(outputDir, "stray.ts"), []byte("stray\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkGenerated(outputDir, files); err == nil || !strings.Contains(err.Error(), "stray.ts (stray)") {
		t.Fatalf("stray generated file was not rejected: %v", err)
	}
}

func generateForTest(t *testing.T, overlay map[string][]byte) *generation {
	t.Helper()
	generated, err := generateContract(repositoryRoot(t), overlay)
	if err != nil {
		t.Fatal(err)
	}
	return generated
}

func TestGeneratedFilesHaveSingleTrailingNewline(t *testing.T) {
	generated := generateForTest(t, nil)
	for name, content := range generated.files {
		if len(content) == 0 {
			t.Errorf("%s is empty", name)
			continue
		}
		if !bytes.HasSuffix(content, []byte("\n")) {
			t.Errorf("%s has no trailing newline", name)
		}
		if bytes.HasSuffix(content, []byte("\n\n")) {
			t.Errorf("%s has a blank line at EOF", name)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func commandsByName(commands []commandContract) map[string]commandContract {
	result := make(map[string]commandContract, len(commands))
	for _, command := range commands {
		result[command.Name] = command
	}
	return result
}

func strconvQuote(value string) string {
	return `"` + value + `"`
}
