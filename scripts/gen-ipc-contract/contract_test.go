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

func TestRemoteDesktopVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	tests := []struct {
		name   string
		scope  string
		nested bool
	}{
		{name: "remote_account_admin_create_user", scope: "remote", nested: true},
		{name: "remote_account_admin_set_user_state", scope: "remote", nested: true},
		{name: "remote_account_admin_users", scope: "remote"},
		{name: "remote_account_change_password", scope: "remote", nested: true},
		{name: "remote_account_device_revoke", scope: "remote", nested: true},
		{name: "remote_account_devices", scope: "remote"},
		{name: "remote_account_me", scope: "remote"},
		{name: "remote_fleet_base_urls_get", scope: "remote"},
		{name: "remote_fleet_base_urls_put", scope: "remote", nested: true},
		{name: "remote_fleet_device_autostart", scope: "remote", nested: true},
		{name: "remote_fleet_device_metrics", scope: "remote", nested: true},
		{name: "remote_fleet_device_revoke", scope: "remote", nested: true},
		{name: "remote_fleet_devices", scope: "remote"},
		{name: "remote_fleet_enroll_code", scope: "remote", nested: true},
		{name: "remote_healthz", scope: "remote"},
		{name: "remote_share_host_create", scope: "remote", nested: true},
		{name: "remote_share_host_list", scope: "remote"},
		{name: "remote_share_host_revoke", scope: "remote", nested: true},
		{name: "remote_share_link_list", scope: "remote"},
		{name: "remote_share_link_revoke", scope: "remote", nested: true},
		{name: "remote_share_preview_list", scope: "remote"},
		{name: "remote_share_preview_revoke", scope: "remote", nested: true},
		{name: "preview_share_active", scope: "preview-share", nested: true},
		{name: "preview_share_create", scope: "preview-share", nested: true},
		{name: "sync_compare", scope: "sync"},
		{name: "sync_link_mfa", scope: "sync", nested: true},
		{name: "sync_link_reset_password", scope: "sync", nested: true},
		{name: "sync_repair_undecryptable", scope: "sync", nested: true},
	}
	selected := make(map[string]bool, len(tests))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != test.scope || command.Nested != test.nested {
				t.Fatalf("metadata = scope:%q nested:%t, want scope:%q nested:%t", command.Scope, command.Nested, test.scope, test.nested)
			}
			if command.Channel || command.Stream != "none" {
				t.Fatalf("unexpected channel/stream metadata: %+v", command)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			selected[test.name] = true
		})
	}
	if len(selected) != 28 {
		t.Fatalf("selected remote desktop command count = %d, want 28", len(selected))
	}
	for name, command := range commands {
		if (command.Scope == "remote" || command.Scope == "preview-share" || command.Scope == "sync") && !selected[name] {
			t.Errorf("unexpected command %q joined the remote desktop vertical scopes", name)
		}
	}
	for _, name := range []string{"sync_link_set", "app_info"} {
		command, ok := commands[name]
		if !ok {
			t.Fatalf("excluded legacy command %q is missing", name)
		}
		if command.Scope != "" || command.Input != nil || command.Output != nil {
			t.Fatalf("excluded command %q acquired DTO metadata: %+v", name, command)
		}
	}
}

func TestSessionTerminalVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	tests := []struct {
		name    string
		scope   string
		nested  bool
		channel bool
		stream  string
		output  string
	}{
		{name: "session_connect_device", scope: "session", nested: true, stream: "none", output: "SessionInfoDTO"},
		{name: "session_connect_local", scope: "session", stream: "none", output: "SessionInfoDTO"},
		{name: "session_disconnect", scope: "session", stream: "none", output: "unknown"},
		{name: "session_list", scope: "session", stream: "none", output: "SessionInfoDTO[]"},
		{name: "session_open_line_tab", scope: "session", channel: true, stream: "binary", output: "AttachedTabDTO"},
		{name: "session_quick_connect_user", scope: "session", stream: "none", output: "string"},
		{name: "session_reconnect", scope: "session", stream: "none", output: "boolean"},
		{name: "terminal_attach", scope: "terminal", channel: true, stream: "binary", output: "AttachedTabDTO"},
		{name: "terminal_broadcast_prepare", scope: "terminal", nested: true, stream: "none", output: "unknown"},
		{name: "terminal_rename_tab", scope: "terminal", stream: "none", output: "AttachedTabDTO"},
		{name: "terminal_write", scope: "terminal", nested: true, stream: "none", output: "unknown"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != test.scope || command.Nested != test.nested || command.Channel != test.channel || command.Stream != test.stream {
				t.Fatalf("metadata = scope:%q nested:%t channel:%t stream:%s, want scope:%q nested:%t channel:%t stream:%s",
					command.Scope, command.Nested, command.Channel, command.Stream, test.scope, test.nested, test.channel, test.stream)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			output, err := registry.ref(command.Output)
			if err != nil {
				t.Fatal(err)
			}
			if output.TS != test.output {
				t.Fatalf("output = %q, want %q", output.TS, test.output)
			}
			selected[test.name] = true
		})
	}
	if len(selected) != 11 {
		t.Fatalf("selected session/terminal command count = %d, want 11", len(selected))
	}
	for name, command := range commands {
		if (command.Scope == "session" || command.Scope == "terminal") && !selected[name] {
			t.Errorf("unexpected command %q joined the session/terminal vertical scopes", name)
		}
	}
	for _, name := range []string{
		"session_connect",
		"session_connect_quick",
		"session_line_exec",
		"session_probe",
		"session_probe_host_key",
		"terminal_attach_tab",
		"terminal_claim",
		"terminal_close_tab",
		"terminal_create_tab",
		"terminal_detach",
		"terminal_dump",
		"terminal_export_log",
		"terminal_list",
		"terminal_record_start",
		"terminal_record_status",
		"terminal_record_stop",
		"terminal_release",
		"terminal_resize",
		"terminal_resize_flush",
		"terminal_screen_text",
		"terminal_set_visible",
		"terminal_snapshot",
		"terminal_switch_encoding",
		"terminal_tail",
	} {
		command, ok := commands[name]
		if !ok {
			t.Fatalf("deferred legacy command %q is missing", name)
		}
		if command.Scope != "" || command.Input != nil || command.Output != nil {
			t.Fatalf("deferred command %q acquired DTO metadata: %+v", name, command)
		}
	}
	if rendered := registry.render(); !strings.Contains(rendered, "nextRetryAt?: string | null;") {
		t.Fatalf("session DTO does not preserve the JSON time contract:\n%s", rendered)
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
