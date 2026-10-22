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
	for _, test := range []struct {
		name    string
		nested  bool
		channel bool
		stream  string
	}{
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

func TestPlatformUpdateBiometricVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	updateInstallInput := "{\n  version: string | null;\n}"
	tests := []struct {
		name   string
		scope  string
		input  string
		output string
	}{
		{name: "app_platform", scope: "platform", input: "Record<string, never>", output: "string"},
		{name: "desktop_tray_get", scope: "platform", input: "Record<string, never>", output: "DesktopTrayDTO"},
		{name: "desktop_tray_set", scope: "platform", input: "DesktopTraySetRequest", output: "DesktopTrayDTO"},
		{name: "system_insets", scope: "platform", input: "Record<string, never>", output: "ScreenInsets"},
		{name: "app_update_check", scope: "update", input: "Record<string, never>", output: "UpdateStatus"},
		{name: "app_update_install", scope: "update", input: updateInstallInput, output: "UpdateResult"},
		{name: "app_restart", scope: "update", input: "Record<string, never>", output: "UpdateResult"},
		{name: "biometric_status", scope: "biometric", input: "Record<string, never>", output: "BiometricStatus"},
		{name: "biometric_unlock", scope: "biometric", input: "Record<string, never>", output: "BiometricUnlockResult"},
		{name: "biometric_enable", scope: "biometric", input: "BiometricEnableRequest", output: "unknown"},
		{name: "biometric_disable", scope: "biometric", input: "Record<string, never>", output: "unknown"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != test.scope || command.Nested || command.Channel || command.Stream != "none" {
				t.Fatalf("unexpected platform/update/biometric metadata: %+v", command)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			input, err := registry.ref(command.Input)
			if err != nil {
				t.Fatal(err)
			}
			if input.TS != test.input {
				t.Fatalf("input = %q, want %q", input.TS, test.input)
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
		t.Fatalf("selected platform/update/biometric command count = %d, want 11", len(selected))
	}
	for name, command := range commands {
		if (command.Scope == "platform" || command.Scope == "update" || command.Scope == "biometric") && !selected[name] {
			t.Errorf("unexpected command %q joined the platform/update/biometric scopes", name)
		}
		if strings.HasPrefix(name, "app_") && name != "app_info" && !selected[name] {
			t.Errorf("app command %q is missing from the explicit consumer inventory", name)
		}
	}
	appInfo, ok := commands["app_info"]
	if !ok {
		t.Fatal("legacy command app_info is missing")
	}
	if appInfo.Scope != "" || appInfo.Input != nil || appInfo.Output != nil {
		t.Fatalf("app_info has no commands.ts consumer and must remain legacy: %+v", appInfo)
	}

	declarations := make(map[string]*tsDeclaration)
	for _, declaration := range registry.declarations {
		declarations[declaration.Name] = declaration
	}
	declarationTests := []struct {
		name   string
		want   []string
		absent []string
	}{
		{name: "DesktopTrayDTO", want: []string{"enabled: boolean;"}},
		{name: "DesktopTraySetRequest", want: []string{"enabled: boolean;"}},
		{name: "ScreenInsets", want: []string{"top: number;", "right: number;", "bottom: number;", "left: number;"}},
		{name: "UpdateStatus", want: []string{"available: boolean;", "currentVersion: string;", "version: string;", "canInstall: boolean;", "unavailableReason: string;"}},
		{name: "UpdateResult", want: []string{"installed: boolean;", "restarted: boolean;", "version: string;", "unavailableReason: string;"}},
		{name: "BiometricStatus", want: []string{"available: boolean;", "canEvaluate: boolean;", "hasMaterial: boolean;"}, absent: []string{"\n  password:", "\n  secret:"}},
		{name: "BiometricUnlockResult", want: []string{"ok: boolean;", "password?: string;", "message?: string;"}},
		{name: "BiometricEnableRequest", want: []string{"password: string;"}, absent: []string{"password?:"}},
	}
	for _, test := range declarationTests {
		declaration := declarations[test.name]
		if declaration == nil {
			t.Errorf("%s declaration is missing", test.name)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s declaration is missing %q:\n%s", test.name, want, declaration.Body)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(declaration.Body, absent) {
				t.Errorf("%s declaration unexpectedly contains %q:\n%s", test.name, absent, declaration.Body)
			}
		}
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
		if (command.Scope == "remote" || command.Scope == "preview-share") && !selected[name] {
			t.Errorf("unexpected command %q joined the remote desktop vertical scopes", name)
		}
	}
	command, ok := commands["app_info"]
	if !ok {
		t.Fatal("excluded legacy command app_info is missing")
	}
	if command.Scope != "" || command.Input != nil || command.Output != nil {
		t.Fatalf("excluded command app_info acquired DTO metadata: %+v", command)
	}
}

func TestSyncVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	tests := []struct {
		name   string
		nested bool
	}{
		{name: "sync_apply_objects", nested: true},
		{name: "sync_bundle_read", nested: true},
		{name: "sync_bundle_write", nested: true},
		{name: "sync_collect_ai_grants"},
		{name: "sync_collect_ai_permission"},
		{name: "sync_collect_ai_profiles", nested: true},
		{name: "sync_collect_assets", nested: true},
		{name: "sync_collect_credentials", nested: true},
		{name: "sync_collect_known_hosts", nested: true},
		{name: "sync_collect_preferences"},
		{name: "sync_collect_tombstones", nested: true},
		{name: "sync_compare"},
		{name: "sync_digest"},
		{name: "sync_export", nested: true},
		{name: "sync_import", nested: true},
		{name: "sync_kind_opt_in_get"},
		{name: "sync_kind_opt_in_set", nested: true},
		{name: "sync_link_get"},
		{name: "sync_link_mfa", nested: true},
		{name: "sync_link_reset_password", nested: true},
		{name: "sync_link_set", nested: true},
		{name: "sync_preferences_get"},
		{name: "sync_preferences_set", nested: true},
		{name: "sync_repair_undecryptable", nested: true},
		{name: "sync_status"},
		{name: "sync_wake"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != "sync" || command.Nested != test.nested {
				t.Fatalf("metadata = scope:%q nested:%t, want scope:sync nested:%t", command.Scope, command.Nested, test.nested)
			}
			if command.Channel || command.Stream != "none" {
				t.Fatalf("unexpected channel/stream metadata: %+v", command)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			if _, err := registry.ref(command.Input); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.ref(command.Output); err != nil {
				t.Fatal(err)
			}
			selected[test.name] = true
		})
	}
	for name, command := range commands {
		if strings.HasPrefix(name, "sync_") && !selected[name] {
			t.Errorf("sync command %q is missing from the vertical inventory", name)
		}
		if command.Scope == "sync" && !selected[name] {
			t.Errorf("unexpected command %q joined the sync vertical", name)
		}
	}
	transcript := commands["transcript_sync_opt_in"]
	if transcript.Scope != "transcript" || transcript.Input == nil || transcript.Output == nil {
		t.Fatalf("transcript opt-in must join the transcript vertical instead of sync: %+v", transcript)
	}

	rendered := registry.render()
	for _, want := range []string{
		"export type ApplyObject = {\n  id: string;\n  kind: string;\n  payload: unknown;\n};",
		"secret?: string | null;",
		"apiKey?: string | null;",
		"mfaTicket?: string;",
		"recoveryKey?: string;",
		"remoteBlobHash?: string;",
		"localPayloadHash?: string;",
		"credTombstones?: SyncCredentialTombstone[];",
		"snippets?: SyncBundleSnippet[];",
		"warnings?: string[];",
		"value: unknown;",
		"defaults: Record<string, unknown>;",
		"overrides: Record<string, unknown>;",
		"effective: Record<string, unknown>;",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("generated sync declarations are missing %q", want)
		}
	}
}

func TestTranscriptVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	tests := []struct {
		name   string
		input  string
		output string
	}{
		{name: "transcript_hosts", input: "Record<string, never>", output: "TranscriptHostDTO[]"},
		{name: "transcript_list", input: "TranscriptListRequest", output: "TranscriptSummaryDTO[]"},
		{name: "transcript_read", input: "TranscriptReadRequest", output: "TranscriptReadResult"},
		{name: "transcript_search", input: "TranscriptSearchRequest", output: "TranscriptMatchDTO[]"},
		{name: "transcript_delete", input: "TranscriptIDRequest", output: "unknown"},
		{name: "transcript_sync_opt_in", input: "TranscriptSyncOptInRequest", output: "Record<string, never>"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != "transcript" || command.Nested || command.Channel || command.Stream != "none" {
				t.Fatalf("unexpected transcript metadata: %+v", command)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			input, err := registry.ref(command.Input)
			if err != nil {
				t.Fatal(err)
			}
			if input.TS != test.input {
				t.Fatalf("input = %q, want %q", input.TS, test.input)
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
	if len(selected) != 6 {
		t.Fatalf("selected transcript command count = %d, want 6", len(selected))
	}
	for name, command := range commands {
		if strings.HasPrefix(name, "transcript_") && !selected[name] {
			t.Errorf("transcript command %q is missing from the vertical inventory", name)
		}
		if command.Scope == "transcript" && !selected[name] {
			t.Errorf("unexpected command %q joined the transcript vertical", name)
		}
	}

	declarations := make(map[string]*tsDeclaration)
	for _, declaration := range registry.declarations {
		declarations[declaration.Name] = declaration
	}
	declarationTests := []struct {
		name   string
		want   []string
		absent []string
	}{
		{name: "TranscriptSummaryDTO", want: []string{"storedBytes: number;", "syncOptIn: boolean;", "contentOmitted: boolean;", "endedAt: number | null;"}},
		{name: "TranscriptChunkDTO", want: []string{"seq: number;", "tabId: string;", "ts: number;", "kind: number;", "dataBase64: string;"}},
		{name: "TranscriptReadRequest", want: []string{"id: string;", "afterSeq: number;", "maxBytes?: number;"}, absent: []string{"afterSeq?:"}},
		{name: "TranscriptReadResult", want: []string{"chunks: TranscriptChunkDTO[];", "nextSeq: number;", "done: boolean;", "totalBytes: number;"}},
		{name: "TranscriptSyncOptInRequest", want: []string{"id: string;", "optIn: boolean;"}},
	}
	for _, test := range declarationTests {
		declaration := declarations[test.name]
		if declaration == nil {
			t.Errorf("%s declaration is missing", test.name)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s declaration is missing %q:\n%s", test.name, want, declaration.Body)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(declaration.Body, absent) {
				t.Errorf("%s declaration unexpectedly contains %q:\n%s", test.name, absent, declaration.Body)
			}
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

func TestHelperOperationContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	registry := newTypeRegistry()
	for _, name := range []string{"terminal_helper_operation", "terminal_helper_offline"} {
		command, ok := commands[name]
		if !ok {
			t.Fatalf("helper command %q is missing", name)
		}
		if command.Scope != "helper" || command.Nested || command.Channel || command.Stream != "none" {
			t.Fatalf("helper command %q metadata = %+v", name, command)
		}
		if command.Input == nil || command.Output == nil {
			t.Fatalf("helper command %q must have real DTOs", name)
		}
		output, err := registry.ref(command.Output)
		if err != nil {
			t.Fatal(err)
		}
		if output.TS != "HelperOperationDTO" {
			t.Fatalf("helper command %q output = %q", name, output.TS)
		}
	}
	if rendered := registry.render(); !strings.Contains(rendered, "protocol?: number | null;") {
		t.Fatalf("helper protocol evidence must remain optional and nullable:\n%s", rendered)
	}
}

func TestCronAndLogTailVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	tests := []struct {
		name    string
		scope   string
		channel bool
		stream  string
		output  string
	}{
		{name: "cron_register", scope: "cron", stream: "none", output: "CronJob"},
		{name: "cron_list", scope: "cron", stream: "none", output: "CronJob[]"},
		{name: "cron_get", scope: "cron", stream: "none", output: "CronJob"},
		{name: "cron_set_enabled", scope: "cron", stream: "none", output: "CronJob"},
		{name: "cron_unregister", scope: "cron", stream: "none", output: "unknown"},
		{name: "log_tail_start", scope: "log-tail", channel: true, stream: "json", output: "LogTailStartResult"},
		{name: "log_tail_cancel", scope: "log-tail", stream: "none", output: "LogTailCancelResult"},
		{name: "log_tail_save", scope: "log-tail", stream: "none", output: "LogTailSaveResult"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != test.scope || command.Nested || command.Channel != test.channel || command.Stream != test.stream {
				t.Fatalf("metadata = scope:%q nested:%t channel:%t stream:%s, want scope:%q nested:false channel:%t stream:%s",
					command.Scope, command.Nested, command.Channel, command.Stream, test.scope, test.channel, test.stream)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			if _, err := registry.ref(command.Input); err != nil {
				t.Fatal(err)
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
	for name, command := range commands {
		if (strings.HasPrefix(name, "cron_") || strings.HasPrefix(name, "log_tail_")) && !selected[name] {
			t.Errorf("command %q is missing from the cron/logTail vertical inventory", name)
		}
		if (command.Scope == "cron" || command.Scope == "log-tail") && !selected[name] {
			t.Errorf("unexpected command %q joined the cron/logTail vertical scopes", name)
		}
	}

	cronGet, ok := commands["cron_get"]
	if !ok || cronGet.Output == nil {
		t.Fatal("cron_get output metadata is missing")
	}
	job := registry.declarations[typeIdentifier(cronGet.Output)]
	if job == nil {
		t.Fatal("cron job declaration is missing")
	}
	for _, want := range []string{
		"schedule: string;",
		"nextRunAt: string;",
		"retryAt?: string;",
		"lastCoalesced?: boolean;",
		"lease: CronLease;",
		"run: CronRunState;",
		"ownerId: string;",
		"ownerKind: string;",
	} {
		if !strings.Contains(job.Body, want) {
			t.Errorf("cron job declaration is missing %q", want)
		}
	}
	manifest := string(generated.files["manifest.ts"])
	start := strings.Index(manifest, `"log_tail_start": {`)
	if start < 0 {
		t.Fatal("log_tail_start manifest entry is missing")
	}
	end := strings.Index(manifest[start:], "\n    },")
	if end < 0 {
		t.Fatal("log_tail_start manifest entry is unterminated")
	}
	if entry := manifest[start : start+end]; !strings.Contains(entry, `streamPayload: "unknown"`) {
		t.Fatalf("log_tail_start JSON stream payload must remain unknown:\n%s", entry)
	}
}

func TestAssetGroupKnownHostVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	tests := []struct {
		name   string
		scope  string
		nested bool
		output string
	}{
		{name: "asset_list", scope: "asset", output: "AssetDTO[]"},
		{name: "asset_get", scope: "asset", output: "AssetDTO"},
		{name: "asset_create", scope: "asset", nested: true, output: "AssetDTO"},
		{name: "asset_update", scope: "asset", nested: true, output: "AssetDTO"},
		{name: "asset_read_key_file", scope: "asset", output: "string"},
		{name: "asset_delete", scope: "asset", output: "unknown"},
		{name: "asset_search", scope: "asset", output: "AssetDTO[]"},
		{name: "asset_search_meta", scope: "asset", output: "AssetSearchDTO"},
		{name: "asset_probe_batch", scope: "asset", nested: true, output: "AssetProbeBatchDTO"},
		{name: "group_list", scope: "group", output: "AssetGroupDTO[]"},
		{name: "group_create", scope: "group", output: "AssetGroupDTO"},
		{name: "group_update", scope: "group", output: "AssetGroupDTO"},
		{name: "group_delete", scope: "group", output: "unknown"},
		{name: "known_host_list", scope: "known-host", output: "unknown"},
		{name: "known_host_accept", scope: "known-host", output: "unknown"},
		{name: "known_host_remove", scope: "known-host", output: "unknown"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
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
			if _, err := registry.ref(command.Input); err != nil {
				t.Fatal(err)
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
	if len(selected) != 16 {
		t.Fatalf("selected asset/group/known-host command count = %d, want 16", len(selected))
	}
	for name, command := range commands {
		if (command.Scope == "asset" || command.Scope == "group" || command.Scope == "known-host") && !selected[name] {
			t.Errorf("unexpected command %q joined the asset/group/known-host vertical scopes", name)
		}
	}
	for _, name := range []string{"session_probe_host_key"} {
		command := commands[name]
		if command.Scope != "" || command.Input != nil || command.Output != nil {
			t.Errorf("deferred command %q acquired DTO metadata: %+v", name, command)
		}
	}

	declarationTests := []struct {
		command string
		output  bool
		want    []string
	}{
		{command: "asset_get", output: true, want: []string{"options: unknown;"}},
		{command: "asset_create", want: []string{"kind: string;", "name: string;", "groupId?: string | null;", "options?: unknown;", "tags?: string;", "sort?: number;"}},
		{command: "asset_update", want: []string{"id: string;", "groupId?: string | null;", "name?: string | null;", "options?: unknown;", "tags?: string | null;"}},
		{command: "asset_get", want: []string{"id?: string;", "q?: string;", "path?: string;"}},
		{command: "asset_search_meta", output: true, want: []string{"assets: AssetDTO[];", "total: number;", "truncated: boolean;"}},
		{command: "asset_probe_batch", want: []string{"assetIds: string[];", "timeoutMs?: number;", "maxConcurrent?: number;"}},
		{command: "group_update", want: []string{"id: string;", "name?: string | null;", "parentId?: string | null;", "sort?: number | null;"}},
		{command: "known_host_accept", want: []string{"host: string;", "port: number;", "keyType: string;", "fingerprint: string;", "key?: string;"}},
	}
	for _, test := range declarationTests {
		command := commands[test.command]
		contractType := command.Input
		kind := "input"
		if test.output {
			contractType = command.Output
			kind = "output"
		}
		declaration := registry.declarations[typeIdentifier(contractType)]
		if declaration == nil {
			t.Errorf("%s %s declaration is missing", test.command, kind)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s %s declaration is missing %q:\n%s", test.command, kind, want, declaration.Body)
			}
		}
	}
	if rendered := registry.render(); strings.Contains(rendered, "OptionalIPCValue") {
		t.Errorf("optional IPC implementation type leaked into the wire contract:\n%s", rendered)
	}
}

func TestVaultCredentialVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	tests := []struct {
		name   string
		scope  string
		nested bool
		input  string
		output string
	}{
		{name: "vault_status", scope: "vault", input: "Record<string, never>", output: "VaultStatus"},
		{name: "vault_init_master", scope: "vault", input: "VaultPasswordRequest", output: "unknown"},
		{name: "vault_init_dpapi", scope: "vault", input: "VaultPasswordRequest", output: "unknown"},
		{name: "vault_unlock", scope: "vault", input: "VaultPasswordRequest", output: "unknown"},
		{name: "vault_lock", scope: "vault", input: "Record<string, never>", output: "unknown"},
		{name: "vault_set_autolock", scope: "vault", input: "VaultAutoLockRequest", output: "unknown"},
		{name: "vault_change_password", scope: "vault", input: "VaultPasswordRequest", output: "unknown"},
		{name: "vault_set_credential", scope: "vault", nested: true, input: "CredentialSetRequest", output: "Record<string, string>"},
		{name: "vault_list_credentials", scope: "vault", input: "Record<string, never>", output: "CredentialDTO[]"},
		{name: "vault_delete_credential", scope: "vault", input: "IDRequest", output: "unknown"},
		{name: "vault_reveal_credential", scope: "vault", input: "IDRequest", output: "RevealedCredentialDTO"},
		{name: "vault_generate_key", scope: "vault", nested: true, input: "VaultGenerateKeyRequest", output: "GeneratedKeyDTO"},
		{name: "credential_update", scope: "credential", nested: true, input: "CredentialUpdateRequest", output: "unknown"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
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
			input, err := registry.ref(command.Input)
			if err != nil {
				t.Fatal(err)
			}
			if input.TS != test.input {
				t.Fatalf("input = %q, want %q", input.TS, test.input)
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
	if len(selected) != 13 {
		t.Fatalf("selected vault/credential command count = %d, want 13", len(selected))
	}
	for name, command := range commands {
		if strings.HasPrefix(name, "vault_") && !selected[name] {
			t.Errorf("vault command %q is missing from the vertical inventory", name)
		}
		if (command.Scope == "vault" || command.Scope == "credential") && !selected[name] {
			t.Errorf("unexpected command %q joined the vault/credential vertical scopes", name)
		}
	}

	declarations := make(map[string]*tsDeclaration)
	for _, declaration := range registry.declarations {
		declarations[declaration.Name] = declaration
	}
	declarationTests := []struct {
		name   string
		want   []string
		absent []string
	}{
		{name: "VaultStatus", want: []string{"initialized: boolean;", "mode: string;", "unlocked: boolean;", "autoLockMinutes: number;", "passwordless: boolean;", "systemProtection: boolean;"}, absent: []string{"\n  password:", "\n  secret:"}},
		{name: "VaultPasswordRequest", want: []string{"password?: string;", "oldPassword?: string;", "newPassword?: string;"}},
		{name: "VaultAutoLockRequest", want: []string{"minutes: number | null;"}},
		{name: "CredentialSetRequest", want: []string{"id?: string;", "name: string;", "kind: string;", "secret: string;", "source?: string | null;", "passphrase?: string | null;"}},
		{name: "CredentialUpdateRequest", want: []string{"id: string;", "name?: string | null;", "secret?: string | null;", "source?: string | null;", "passphrase?: string | null;"}},
		{name: "CredentialDTO", want: []string{"usedBy: AssetRef[];", "source: string | null;", "refPath: string | null;", "hasPassphrase: boolean;"}, absent: []string{"\n  secret:", "\n  value:", "\n  passphrase:"}},
		{name: "RevealedCredentialDTO", want: []string{"kind: string;", "value: string;", "source: string | null;", "refPath: string | null;", "passphrase: string | null;"}},
		{name: "VaultGenerateKeyRequest", want: []string{"name: string;", "algorithm?: string;", "passphrase?: string;"}},
		{name: "GeneratedKeyDTO", want: []string{"id: string;", "name: string;", "algorithm: string;", "fingerprint: string;", "publicKey: string;"}, absent: []string{"\n  privateKey:", "\n  secret:", "\n  passphrase:", "\n  value:"}},
	}
	for _, test := range declarationTests {
		declaration := declarations[test.name]
		if declaration == nil {
			t.Errorf("%s declaration is missing", test.name)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s declaration is missing %q:\n%s", test.name, want, declaration.Body)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(declaration.Body, absent) {
				t.Errorf("%s declaration unexpectedly exposes %q:\n%s", test.name, absent, declaration.Body)
			}
		}
	}
}

func TestFSLocalMountVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	structType := func(fields ...string) string {
		return "{\n  " + strings.Join(fields, "\n  ") + "\n}"
	}
	tests := []struct {
		name   string
		scope  string
		nested bool
		input  string
		output string
	}{
		{name: "fs_cancel", scope: "fs", input: "FSCancelRequest", output: "boolean"},
		{name: "fs_checksum", scope: "fs", input: structType("sessionId: string;", "path: string;", "algo?: string;"), output: "string"},
		{name: "fs_chmod", scope: "fs", input: structType("sessionId: string;", "path: string;", "mode: number;"), output: "unknown"},
		{name: "fs_delete", scope: "fs", input: structType("sessionId: string;", "path: string;", "isDir: boolean;"), output: "unknown"},
		{name: "fs_download", scope: "fs", input: structType("sessionId: string;", "remotePath: string;", "localPath: string;", "resume?: boolean;", "taskId?: string;", "attemptId?: string;"), output: "number"},
		{name: "fs_extract", scope: "fs", input: structType("sessionId: string;", "path: string;"), output: "string"},
		{name: "fs_list", scope: "fs", input: structType("sessionId: string;", "path: string;"), output: "FSEntryDTO[]"},
		{name: "fs_mkdir", scope: "fs", input: structType("sessionId: string;", "path: string;"), output: "unknown"},
		{name: "fs_pack_download", scope: "fs", input: structType("sessionId: string;", "remotePath: string;", "localPath: string;", "taskId?: string;", "attemptId?: string;"), output: "number"},
		{name: "fs_read", scope: "fs", input: structType("sessionId: string;", "path: string;", "maxBytes?: number;"), output: "FSReadDTO"},
		{name: "fs_read_range", scope: "fs", input: structType("sessionId: string;", "path: string;", "offset: number;", "maxBytes?: number;"), output: "FSRangeReadDTO"},
		{name: "fs_rename", scope: "fs", input: structType("sessionId: string;", "from: string;", "to: string;"), output: "unknown"},
		{name: "fs_roots", scope: "fs", input: structType("sessionId: string;"), output: "FSRootsDTO"},
		{name: "fs_upload", scope: "fs", input: structType("sessionId: string;", "localPath: string;", "remotePath: string;", "resume?: boolean;", "taskId?: string;", "attemptId?: string;"), output: "number"},
		{name: "fs_upload_abort", scope: "fs", input: structType("sessionId: string;", "uploadId: string;", "taskId: string;", "attemptId: string;"), output: "unknown"},
		{name: "fs_upload_begin", scope: "fs", input: structType("sessionId: string;", "remotePath: string;", "totalSize: number;", "chunkSize?: number;", "taskId: string;", "attemptId: string;"), output: "FSChunkUploadBeginDTO"},
		{name: "fs_upload_chunk", scope: "fs", input: structType("sessionId: string;", "uploadId: string;", "taskId: string;", "attemptId: string;", "index: number;", "size: number;", "sha256: string;", "contentBase64: string;"), output: "FSChunkUploadProgressDTO"},
		{name: "fs_upload_commit", scope: "fs", input: structType("sessionId: string;", "uploadId: string;", "taskId: string;", "attemptId: string;"), output: "number"},
		{name: "fs_write", scope: "fs", nested: true, input: structType("sessionId: string;", "path: string;", "contentBase64: string;", "backup: boolean;", "taskId?: string;", "attemptId?: string;"), output: "unknown"},
		{name: "local_file_read", scope: "local-file", input: "LocalFileReadRequest", output: "FSReadDTO"},
		{name: "local_file_read_range", scope: "local-file", input: "LocalFileReadRangeRequest", output: "FSRangeReadDTO"},
		{name: "local_folder_list", scope: "local-file", input: "LocalFolderListRequest", output: "LocalFolderListDTO"},
		{name: "mount_capability", scope: "mount", input: "Record<string, never>", output: "string | null"},
		{name: "mount_create", scope: "mount", nested: true, input: "MountCreateArgs", output: "MountEntryDTO"},
		{name: "mount_list", scope: "mount", input: "MountListRequest", output: "MountEntryDTO[]"},
		{name: "mount_remove", scope: "mount", input: "MountRemoveArgs", output: "unknown"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
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
			input, err := registry.ref(command.Input)
			if err != nil {
				t.Fatal(err)
			}
			if input.TS != test.input {
				t.Fatalf("input = %q, want %q", input.TS, test.input)
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
	if len(selected) != 26 {
		t.Fatalf("selected fs/local-file/mount command count = %d, want 26", len(selected))
	}
	for name, command := range commands {
		inventory := strings.HasPrefix(name, "fs_") || strings.HasPrefix(name, "local_file_") || strings.HasPrefix(name, "local_folder_") || strings.HasPrefix(name, "mount_")
		if inventory && !selected[name] {
			t.Errorf("command %q is missing from the fs/local-file/mount vertical inventory", name)
		}
		if (command.Scope == "fs" || command.Scope == "local-file" || command.Scope == "mount") && !selected[name] {
			t.Errorf("unexpected command %q joined the fs/local-file/mount vertical scopes", name)
		}
	}

	declarations := make(map[string]*tsDeclaration)
	for _, declaration := range registry.declarations {
		declarations[declaration.Name] = declaration
	}
	declarationTests := []struct {
		name   string
		want   []string
		absent []string
	}{
		{name: "FSRootsDTO", want: []string{"pathStyle: string;", "roots: FSRootDTO[];", "homePath: string | null;", "supported: boolean;"}},
		{name: "FSEntryDTO", want: []string{"path: string;", "owner: string | null;", "group: string | null;", "symlinkTarget: string | null;"}},
		{name: "FSReadDTO", want: []string{"path: string;", "size: number;", "contentBase64: string;"}, absent: []string{"number[]"}},
		{name: "FSRangeReadDTO", want: []string{"offset: number;", "size: number;", "contentBase64: string;", "truncated: boolean;"}, absent: []string{"number[]"}},
		{name: "LocalFolderFileDTO", want: []string{"relativePath: string;", "localPath: string;", "size: number;", "mtimeMs: number;"}},
		{name: "LocalFolderListDTO", want: []string{"files: LocalFolderFileDTO[];", "emptyDirs: string[];"}},
		{name: "LocalFileReadRequest", want: []string{"path: string;", "maxBytes?: number;"}},
		{name: "LocalFileReadRangeRequest", want: []string{"path: string;", "offset: number;", "maxBytes?: number;"}},
		{name: "MountCreateArgs", want: []string{"sessionId: string;", "remotePath: string;", "localPoint: string;", "username?: string;", "password?: string;"}},
		{name: "MountEntryDTO", want: []string{"localPoint: string;", "remote: string;", "sessionId: string | null;", "createdAt: number | null;", "managed: boolean;"}, absent: []string{"\n  username:", "\n  password:", "\n  secret:"}},
	}
	for _, test := range declarationTests {
		declaration := declarations[test.name]
		if declaration == nil {
			t.Errorf("%s declaration is missing", test.name)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s declaration is missing %q:\n%s", test.name, want, declaration.Body)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(declaration.Body, absent) {
				t.Errorf("%s declaration unexpectedly exposes %q:\n%s", test.name, absent, declaration.Body)
			}
		}
	}
}

func TestDockerVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	structType := func(fields ...string) string {
		return "{\n  " + strings.Join(fields, "\n  ") + "\n}"
	}
	tests := []struct {
		name    string
		nested  bool
		channel bool
		stream  string
		input   string
		output  string
	}{
		{name: "docker_action", nested: true, stream: "none", input: structType("sessionId: string;", "containerId: string;", "action: string;", "newName?: string;"), output: "unknown"},
		{name: "docker_compose_action", stream: "none", input: "DockerComposeActionIPCRequest", output: "string"},
		{name: "docker_compose_logs_attach", channel: true, stream: "binary", input: "DockerComposeLogsIPCRequest", output: "string"},
		{name: "docker_compose_redeploy_attach", channel: true, stream: "binary", input: structType("sessionId: string;", "project: string;", "path: string;", "ticket: string;"), output: "string"},
		{name: "docker_compose_redeploy_prepare", stream: "none", input: structType("sessionId: string;", "project: string;", "path: string;", "baseContentBase64: string;", "contentBase64: string;", "productionConfirmation?: DockerProductionConfirmation | null;"), output: "DockerComposeRedeployPrepareResult"},
		{name: "docker_compose_stacks", stream: "none", input: "SessionIDRequest", output: "ComposeOverview"},
		{name: "docker_container_list_dir", stream: "none", input: structType("sessionId: string;", "containerId: string;", "path: string;"), output: "string[]"},
		{name: "docker_disk_usage", stream: "none", input: "SessionIDRequest", output: "DiskUsage"},
		{name: "docker_exec_attach", nested: true, channel: true, stream: "binary", input: structType("sessionId: string;", "containerId: string;", "cmd?: string;", "cols: number;", "rows: number;", "clientId: string;"), output: "string"},
		{name: "docker_image_pull", stream: "none", input: structType("sessionId: string;", "image: string;"), output: "string"},
		{name: "docker_image_remove", stream: "none", input: structType("sessionId: string;", "image: string;", "force?: boolean;"), output: "unknown"},
		{name: "docker_images", stream: "none", input: "SessionIDRequest", output: "ImageSummary[]"},
		{name: "docker_inspect", stream: "none", input: structType("sessionId: string;", "containerId: string;"), output: "unknown"},
		{name: "docker_logs_attach", channel: true, stream: "binary", input: "DockerLogsRequest", output: "string"},
		{name: "docker_overview", stream: "none", input: "SessionIDRequest", output: "Overview"},
		{name: "docker_prune", stream: "none", input: "DockerPruneIPCRequest", output: "PruneResult"},
		{name: "docker_prune_preview", stream: "none", input: "DockerPrunePreviewIPCRequest", output: "PrunePreview"},
		{name: "docker_ps", stream: "none", input: "SessionIDRequest", output: "ContainerSummary[]"},
		{name: "docker_reclaimable", stream: "none", input: "SessionIDRequest", output: "ReclaimableProbe"},
		{name: "docker_stats", stream: "none", input: "SessionIDRequest", output: "string"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != "docker" || command.Nested != test.nested || command.Channel != test.channel || command.Stream != test.stream {
				t.Fatalf("metadata = scope:%q nested:%t channel:%t stream:%s, want scope:docker nested:%t channel:%t stream:%s",
					command.Scope, command.Nested, command.Channel, command.Stream, test.nested, test.channel, test.stream)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			input, err := registry.ref(command.Input)
			if err != nil {
				t.Fatal(err)
			}
			if input.TS != test.input {
				t.Fatalf("input = %q, want %q", input.TS, test.input)
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
	if len(selected) != 20 {
		t.Fatalf("selected docker command count = %d, want 20", len(selected))
	}
	for name, command := range commands {
		if strings.HasPrefix(name, "docker_") && !selected[name] {
			t.Errorf("docker command %q is missing from the vertical inventory", name)
		}
		if command.Scope == "docker" && !selected[name] {
			t.Errorf("unexpected command %q joined the docker vertical", name)
		}
	}

	declarations := make(map[string]*tsDeclaration)
	for _, declaration := range registry.declarations {
		declarations[declaration.Name] = declaration
	}
	declarationTests := []struct {
		name   string
		want   []string
		absent []string
	}{
		{name: "DockerProductionConfirmation", want: []string{"confirmed: boolean;", "targetSetHash: string;", "commandHash: string;", "environmentNames?: string[];"}, absent: []string{"\n  sessionId:", "\n  project:", "\n  action:", "\n  token:"}},
		{name: "DockerComposeActionIPCRequest", want: []string{"sessionId: string;", "project: string;", "action: string;", "productionConfirmation?: DockerProductionConfirmation | null;"}},
		{name: "DockerComposeLogsIPCRequest", want: []string{"sessionId: string;", "project: string;", "tail?: number | null;"}},
		{name: "DockerComposeRedeployPrepareResult", want: []string{"ticket: string;", "contentHash: string;"}},
		{name: "DockerPruneIPCRequest", want: []string{"sessionId: string;", "token: string;", "digest: string;", "productionConfirmation?: DockerProductionConfirmation | null;"}},
		{name: "PrunePreview", want: []string{"token: string;", "digest: string;", "expiresAt: number;", "items: PruneItem[];", "totalBytes: number;"}},
		{name: "PruneResult", want: []string{"items: PruneItemResult[];", "freedBytes: number;"}},
		{name: "ContainerSummary", want: []string{"id: string;", "composeProject: string | null;", "composeService?: string | null;"}},
		{name: "Overview", want: []string{"containers: ContainerSummary[];", "hostStats: HostStats;"}},
		{name: "ComposeOverview", want: []string{"cli?: string;", "available: boolean;", "stacks: ComposeStack[];"}},
		{name: "DiskUsage", want: []string{"categories: DiskCategory[];", "images: DiskImage[];", "containers: DiskContainer[];", "volumes: VolumeUsage[];", "networks: NetworkUsage[];", "buildCache: BuildCacheUsage[];"}},
		{name: "ReclaimableProbe", want: []string{"totalBytes: number;", "categories: DiskCategory[];"}},
	}
	for _, test := range declarationTests {
		declaration := declarations[test.name]
		if declaration == nil {
			t.Errorf("%s declaration is missing", test.name)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s declaration is missing %q:\n%s", test.name, want, declaration.Body)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(declaration.Body, absent) {
				t.Errorf("%s declaration unexpectedly exposes %q:\n%s", test.name, absent, declaration.Body)
			}
		}
	}
	if got := typeIdentifier(commands["docker_inspect"].Output); got != "encoding/json.RawMessage" {
		t.Fatalf("docker_inspect output = %q, want encoding/json.RawMessage", got)
	}
	manifest := string(generated.files["manifest.ts"])
	for _, name := range []string{"docker_logs_attach", "docker_exec_attach", "docker_compose_logs_attach", "docker_compose_redeploy_attach"} {
		start := strings.Index(manifest, strconvQuote(name)+": {")
		if start < 0 {
			t.Fatalf("%s manifest entry is missing", name)
		}
		end := strings.Index(manifest[start:], "\n    },")
		if end < 0 {
			t.Fatalf("%s manifest entry is unterminated", name)
		}
		if entry := manifest[start : start+end]; !strings.Contains(entry, `streamPayload: "unknown"`) {
			t.Fatalf("%s stream payload must remain unknown:\n%s", name, entry)
		}
	}
}

func TestDBRedisVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	queryInput := "{\n  connId: string;\n  sql: string;\n  timeoutMs?: number | null;\n}"
	tests := []struct {
		name   string
		scope  string
		nested bool
		input  string
		output string
	}{
		{name: "db_connect", scope: "db", nested: true, input: "ConnectRequest", output: "ConnectResult"},
		{name: "db_disconnect", scope: "db", input: "ConnectionRequest", output: "unknown"},
		{name: "db_schemas", scope: "db", input: "ConnectionRequest", output: "string[]"},
		{name: "db_tables", scope: "db", input: "TablesRequest", output: "string[]"},
		{name: "db_columns", scope: "db", input: "ColumnsRequest", output: "TableDescribe"},
		{name: "db_query", scope: "db", input: queryInput, output: "QueryResult"},
		{name: "redis_scan", scope: "redis", input: "RedisScanRequest", output: "[number, string[]]"},
		{name: "redis_inspect", scope: "redis", input: "RedisKeyRequest", output: "RedisKeyView"},
		{name: "redis_command", scope: "redis", input: "RedisCommandRequest", output: "string"},
		{name: "redis_set_ttl", scope: "redis", input: "RedisTTLRequest", output: "unknown"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
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
			if got := typeIdentifier(command.Input); got == "In" || got == "Out" {
				t.Fatalf("generic wrapper type %q leaked into input", got)
			}
			if got := typeIdentifier(command.Output); got == "In" || got == "Out" {
				t.Fatalf("generic wrapper type %q leaked into output", got)
			}
			input, err := registry.ref(command.Input)
			if err != nil {
				t.Fatal(err)
			}
			if input.TS != test.input {
				t.Fatalf("input = %q, want %q", input.TS, test.input)
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
	if len(selected) != 10 {
		t.Fatalf("selected DB/Redis command count = %d, want 10", len(selected))
	}
	for name, command := range commands {
		inventory := strings.HasPrefix(name, "db_") || strings.HasPrefix(name, "redis_")
		if inventory && !selected[name] {
			t.Errorf("command %q is missing from the DB/Redis vertical inventory", name)
		}
		if (command.Scope == "db" || command.Scope == "redis") && !selected[name] {
			t.Errorf("unexpected command %q joined the DB/Redis vertical scopes", name)
		}
	}

	declarations := make(map[string]*tsDeclaration)
	for _, declaration := range registry.declarations {
		declarations[declaration.Name] = declaration
	}
	declarationTests := []struct {
		name   string
		want   []string
		absent []string
	}{
		{name: "QueryResult", want: []string{"columns: string[];", "rows: unknown[][];", "rowsAffected: number;", "durationMs: number;", "truncated: boolean;", "error: string | null;"}},
		{name: "TableColumn", want: []string{"default: string | null;"}, absent: []string{"default?:"}},
		{name: "RedisKeyView", want: []string{"keyType: string;", "ttl: number;", "value: unknown;"}},
		{name: "TablesRequest", want: []string{"schema?: string;"}, absent: []string{"schema?: string | null;"}},
		{name: "ColumnsRequest", want: []string{"schema?: string;", "table: string;"}, absent: []string{"schema?: string | null;"}},
		{name: "RedisScanRequest", want: []string{"cursor?: number | null;", "pattern?: string | null;", "count?: number | null;"}},
		{name: "RedisCommandRequest", want: []string{"connId: string;", "args: string[];"}},
		{name: "RedisTTLRequest", want: []string{"key: string;", "seconds: number;"}},
	}
	for _, test := range declarationTests {
		declaration := declarations[test.name]
		if declaration == nil {
			t.Errorf("%s declaration is missing", test.name)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s declaration is missing %q:\n%s", test.name, want, declaration.Body)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(declaration.Body, absent) {
				t.Errorf("%s declaration unexpectedly exposes %q:\n%s", test.name, absent, declaration.Body)
			}
		}
	}
	if declaration := registry.declarations[typeIdentifier(commands["redis_scan"].Output)]; declaration != nil {
		t.Fatalf("RedisScanResult must use its wire tuple, not a struct declaration: %+v", declaration)
	}
}

func TestDockerUntypedOutputsRemainUnknown(t *testing.T) {
	root := repositoryRoot(t)
	targets := map[string][]string{
		"docker_ipc.go": {"docker_overview", "docker_ps", "docker_images"},
		"docker_maintenance_ipc.go": {
			"docker_compose_stacks", "docker_compose_action", "docker_compose_redeploy_prepare",
			"docker_disk_usage", "docker_prune_preview", "docker_prune", "docker_reclaimable",
		},
	}
	overlay := make(map[string][]byte)
	for file, commands := range targets {
		path := filepath.Join(root, "internal", "app", "production", file)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, command := range commands {
			start := bytes.Index(source, []byte(`ipc.Register(dispatcher, "`+command+`",`))
			if start < 0 {
				t.Fatalf("registration for %s is missing", command)
			}
			resultStart := bytes.Index(source[start:], []byte(") ("))
			if resultStart < 0 {
				t.Fatalf("handler results for %s are missing", command)
			}
			resultStart += start + len(") (")
			resultEnd := bytes.Index(source[resultStart:], []byte(", error)"))
			if resultEnd < 0 {
				t.Fatalf("handler error result for %s is missing", command)
			}
			resultEnd += resultStart
			source = append(append(append([]byte(nil), source[:resultStart]...), []byte("any")...), source[resultEnd:]...)
		}
		overlay[path] = source
	}
	generated := generateForTest(t, overlay)
	commands := commandsByName(generated.commands)
	registry := newTypeRegistry()
	for _, names := range targets {
		for _, name := range names {
			output, err := registry.ref(commands[name].Output)
			if err != nil {
				t.Fatal(err)
			}
			if output.TS != "unknown" || output.Manifest != "unknown" {
				t.Errorf("%s any output = %+v, want unknown", name, output)
			}
		}
	}
}

func TestForwardRuntimeVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	tests := []struct {
		name   string
		input  string
		output string
	}{
		{name: "forward_env", input: "Record<string, never>", output: "Environment"},
		{name: "forward_create", input: "CreateLocalArgs", output: "Spec"},
		{name: "forward_create_socks", input: "CreateSocksArgs", output: "Spec"},
		{name: "forward_create_remote", input: "CreateRemoteArgs", output: "Spec"},
		{name: "forward_list", input: "Record<string, never>", output: "Spec[]"},
		{name: "forward_retry", input: "RetryRequest", output: "Spec"},
		{name: "forward_remove", input: "RemoveRequest", output: "unknown"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != "forward" || command.Nested || command.Channel || command.Stream != "none" {
				t.Fatalf("unexpected forward runtime metadata: %+v", command)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			input, err := registry.ref(command.Input)
			if err != nil {
				t.Fatal(err)
			}
			if input.TS != test.input {
				t.Fatalf("input = %q, want %q", input.TS, test.input)
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
	if len(selected) != 7 {
		t.Fatalf("selected forward runtime command count = %d, want 7", len(selected))
	}
	for name, command := range commands {
		if strings.HasPrefix(name, "forward_") && !strings.HasPrefix(name, "forward_rule_") && !selected[name] {
			t.Errorf("forward runtime command %q is missing from the explicit inventory", name)
		}
		if command.Scope == "forward" && !selected[name] {
			t.Errorf("unexpected command %q joined the forward runtime scope", name)
		}
	}
	if command := commands["forward_rule_list"]; command.Scope != "forward-rule" {
		t.Fatalf("saved rules must remain a separate scope: %+v", command)
	}

	declarations := make(map[string]*tsDeclaration)
	for _, declaration := range registry.declarations {
		declarations[declaration.Name] = declaration
	}
	declarationTests := []struct {
		name   string
		want   []string
		absent []string
	}{
		{name: "Environment", want: []string{"available: boolean;", "platform: string;", "listenHost: string;", "reason?: string;"}},
		{name: "Spec", want: []string{"id: string;", "sessionId: string;", "hostId?: string;", "status: string;", "error?: string;", "listenHost: string;", "listenPort: number;", "targetHost: string | null;", "targetPort: number | null;", "kind: string;", "createdAt: number;"}, absent: []string{"listenHost?:", "targetHost?:", "targetPort?:", "autoStart", "assetId", "stopped"}},
		{name: "CreateLocalArgs", want: []string{"sessionId: string;", "listenPort: number;", "targetHost: string;", "targetPort: number;"}, absent: []string{"acknowledgeRisk", "autoStart"}},
		{name: "CreateSocksArgs", want: []string{"sessionId: string;", "listenPort: number;", "acknowledgeRisk?: boolean;"}, absent: []string{"targetHost", "targetPort", "autoStart"}},
		{name: "CreateRemoteArgs", want: []string{"sessionId: string;", "bindHost: string;", "bindPort: number;", "targetHost: string;", "targetPort: number;", "acknowledgeRisk?: boolean;"}, absent: []string{"bindHost?:", "bindPort?:", "targetHost?:", "targetPort?:", "autoStart"}},
		{name: "RetryRequest", want: []string{"id: string;", "sessionId?: string;"}, absent: []string{"sessionId: string;"}},
		{name: "RemoveRequest", want: []string{"id: string;"}},
	}
	for _, test := range declarationTests {
		declaration := declarations[test.name]
		if declaration == nil {
			t.Errorf("%s declaration is missing", test.name)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s declaration is missing %q:\n%s", test.name, want, declaration.Body)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(declaration.Body, absent) {
				t.Errorf("%s declaration unexpectedly contains %q:\n%s", test.name, absent, declaration.Body)
			}
		}
	}
	if rendered := registry.render(); strings.Contains(rendered, "ExposureRisk") {
		t.Fatalf("confirmation error detail leaked into a normal forward output DTO:\n%s", rendered)
	}
}

func TestOperationsVerticalContracts(t *testing.T) {
	generated := generateForTest(t, nil)
	commands := commandsByName(generated.commands)
	structType := func(fields ...string) string {
		return "{\n  " + strings.Join(fields, "\n  ") + "\n}"
	}
	tests := []struct {
		name   string
		scope  string
		nested bool
		input  string
		output string
	}{
		{name: "snippet_list", scope: "snippet", input: "Record<string, never>", output: "SnippetDTO[]"},
		{name: "snippet_create", scope: "snippet", input: "SnippetInputRequest", output: "Record<string, string>"},
		{name: "snippet_update", scope: "snippet", input: "SnippetPatchRequest", output: "unknown"},
		{name: "snippet_delete", scope: "snippet", input: structType("id: string;"), output: "unknown"},
		{name: "audit_query", scope: "audit", nested: true, input: "AuditQueryRequest", output: "AuditDTO[]"},
		{name: "audit_count", scope: "audit", nested: true, input: structType("sessionId?: string | null;", "assetId?: string | null;", "source?: string | null;", "kind?: string | null;"), output: "AuditCountDTO"},
		{name: "command_query", scope: "command-log", nested: true, input: "CommandLogQueryRequest", output: "CommandLogDTO[]"},
		{name: "command_count", scope: "command-log", nested: true, input: structType("sessionId?: string | null;", "assetId?: string | null;", "userId?: string | null;"), output: "AuditCountDTO"},
		{name: "layout_get", scope: "layout", input: "Record<string, never>", output: "LayoutDTO"},
		{name: "layout_put", scope: "layout", nested: true, input: "LayoutPutRequest", output: "LayoutSaveDTO"},
		{name: "ssh_import_preview", scope: "ssh-import", nested: true, input: "SshImportPreviewRequest", output: "SshImportPreviewDTO"},
		{name: "ssh_import_apply", scope: "ssh-import", nested: true, input: "SshImportApplyRequest", output: "SshImportApplyDTO"},
	}
	selected := make(map[string]bool, len(tests))
	registry := newTypeRegistry()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, ok := commands[test.name]
			if !ok {
				t.Fatalf("selected command %q is missing", test.name)
			}
			if command.Scope != test.scope || command.Nested != test.nested || command.Channel || command.Stream != "none" {
				t.Fatalf("unexpected operations metadata: %+v", command)
			}
			if command.Input == nil || command.Output == nil {
				t.Fatalf("selected command must carry input/output DTO metadata: %+v", command)
			}
			input, err := registry.ref(command.Input)
			if err != nil {
				t.Fatal(err)
			}
			if input.TS != test.input {
				t.Fatalf("input = %q, want %q", input.TS, test.input)
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
	if len(selected) != 12 {
		t.Fatalf("selected operations command count = %d, want 12", len(selected))
	}
	for name, command := range commands {
		switch command.Scope {
		case "snippet", "audit", "command-log", "layout", "ssh-import":
			if !selected[name] {
				t.Errorf("unexpected command %q joined the operations scopes", name)
			}
		}
	}

	declarations := make(map[string]*tsDeclaration)
	for _, declaration := range registry.declarations {
		declarations[declaration.Name] = declaration
	}
	declarationTests := []struct {
		name string
		want []string
	}{
		{name: "SnippetInputRequest", want: []string{"name: string;", "body: string;", "groupId?: string | null;", "sort?: number;"}},
		{name: "SnippetPatchRequest", want: []string{"id: string;", "name: string;", "body: string;", "groupId?: string | null;", "sort?: number | null;"}},
		{name: "AuditQueryRequest", want: []string{"sessionId?: string | null;", "assetId?: string | null;", "source?: string | null;", "kind?: string | null;", "limit?: number;", "offset?: number;"}},
		{name: "AuditDTO", want: []string{"payload: unknown;", "exitCode: number | null;", "durationMs: number | null;"}},
		{name: "AuditCountDTO", want: []string{"total: number;"}},
		{name: "CommandLogQueryRequest", want: []string{"sessionId?: string | null;", "assetId?: string | null;", "userId?: string | null;", "limit?: number;", "offset?: number;"}},
		{name: "CommandLogDTO", want: []string{"userId: string | null;", "command: string;", "exitCode: number | null;"}},
		{name: "LayoutDTO", want: []string{"revision: number;", "updatedAt: number;", "data: unknown;"}},
		{name: "LayoutPutRequest", want: []string{"data: string;", "revision: number;"}},
		{name: "LayoutSaveDTO", want: []string{"saved: boolean;", "revision: number;", "conflict: boolean;"}},
		{name: "SshImportPreviewRequest", want: []string{"source: string;", "path?: string;", "confirmed?: boolean;"}},
		{name: "SshImportApplyRequest", want: []string{"source: string;", "path?: string;", "confirmed?: boolean;", "groupId?: string | null;", "tags?: string | null;", "hosts: SshImportItemAction[];", "keys: SshImportItemAction[];"}},
		{name: "SshImportPreviewDTO", want: []string{"hosts: SshImportHostDTO[];", "keys: SshImportKeyDTO[];", "diagnostics: SshImportDiagnosticDTO[];", "truncated: boolean;"}},
		{name: "SshImportApplyDTO", want: []string{"assetsCreated: number;", "assetsUpdated: number;", "credentialsCreated: number;", "credentialsUpdated: number;", "skipped: number;", "warnings: string[];"}},
	}
	for _, test := range declarationTests {
		declaration := declarations[test.name]
		if declaration == nil {
			t.Errorf("%s declaration is missing", test.name)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(declaration.Body, want) {
				t.Errorf("%s declaration is missing %q:\n%s", test.name, want, declaration.Body)
			}
		}
	}
}

func TestRemainingLegacyCommandsAreExplicitlyDeferred(t *testing.T) {
	generated := generateForTest(t, nil)
	want := []string{
		"ai_answer",
		"ai_cancel",
		"ai_chat",
		"ai_circuit_status",
		"ai_confirm",
		"ai_conversation_create",
		"ai_conversation_delete",
		"ai_conversation_list",
		"ai_conversation_retitle",
		"ai_edit_resend",
		"ai_get_permission",
		"ai_get_provider",
		"ai_hitl_events",
		"ai_hitl_snapshot",
		"ai_messages",
		"ai_model_activate",
		"ai_model_delete",
		"ai_model_preset",
		"ai_model_profiles",
		"ai_model_refresh",
		"ai_model_save",
		"ai_models",
		"ai_presets",
		"ai_run_events",
		"ai_run_get",
		"ai_run_list",
		"ai_set_permission",
		"ai_set_provider",
		"ai_steer",
		"ai_takeover_cancel",
		"ai_takeover_confirm",
		"ai_takeover_enter",
		"ai_takeover_exit",
		"ai_takeover_pause",
		"ai_takeover_resume",
		"ai_takeover_run",
		"ai_test_provider",
		"ai_usage_summary",
		"app_info",
		"memory_create",
		"memory_delete",
		"memory_edit",
		"memory_get",
		"memory_index",
		"memory_settings_get",
		"memory_settings_set",
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
	}
	var got []string
	aiMemory := 0
	sessionTerminal := 0
	appInfo := 0
	for _, command := range generated.commands {
		if command.Input != nil {
			continue
		}
		got = append(got, command.Name)
		if command.Scope != "" || command.Output != nil {
			t.Errorf("legacy command %q acquired typed metadata: %+v", command.Name, command)
		}
		switch {
		case strings.HasPrefix(command.Name, "ai_"), strings.HasPrefix(command.Name, "memory_"):
			aiMemory++
		case strings.HasPrefix(command.Name, "session_"), strings.HasPrefix(command.Name, "terminal_"):
			sessionTerminal++
		case command.Name == "app_info":
			appInfo++
		default:
			t.Errorf("legacy command %q is outside every explicitly deferred category", command.Name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining legacy commands = %v, want %v", got, want)
	}
	if len(got) != 70 || aiMemory != 45 || sessionTerminal != 24 || appInfo != 1 {
		t.Fatalf("legacy categories = total:%d AI/memory:%d session/terminal:%d app_info:%d, want 70/45/24/1", len(got), aiMemory, sessionTerminal, appInfo)
	}
	if scopes := deferredScopes(); !reflect.DeepEqual(scopes, []string{"memory-owner", "ai-owner"}) {
		t.Fatalf("deferred scopes = %v, want only memory-owner and ai-owner", scopes)
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
