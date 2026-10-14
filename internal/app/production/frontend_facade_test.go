package production

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"testing"
)

// Command pinned values in the frontend IPC facade (files under src/ipc that initiate calls directly) must all fall within
// the desktop production dispatcher's registered surface and, except the desktop-only local-folder pair, the server
// dispatcher's as well; the reverse difference may only contain ai_takeover_resume, the single pure-backend entry (the
// frontend only consumes ai_takeover_enter/run/exit).
var frontendFacadeFiles = []string{
	filepath.Join("..", "..", "..", "src", "ipc", "commands.ts"),
	filepath.Join("..", "..", "..", "src", "ipc", "cron.ts"),
	filepath.Join("..", "..", "..", "src", "ipc", "grantApi.ts"),
	filepath.Join("..", "..", "..", "src", "ipc", "memory.ts"),
	filepath.Join("..", "..", "..", "src", "ipc", "remoteApi.ts"),
}

var (
	frontendCallSite = regexp.MustCompile(`\bcall\b`)
	frontendCallName = regexp.MustCompile(`^[a-z][a-z0-9_]+$`)
)

func frontendFacadeCommands(t *testing.T) map[string]bool {
	t.Helper()
	commands := map[string]bool{}
	for _, name := range frontendFacadeFiles {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, site := range frontendCallSite.FindAllStringIndex(text, -1) {
			index := site[1]
			skipSpace := func() {
				for index < len(text) && (text[index] == ' ' || text[index] == '\t' || text[index] == '\n' || text[index] == '\r') {
					index++
				}
			}
			skipSpace()
			if index < len(text) && text[index] == '<' {
				depth := 1
				index++
				for index < len(text) && depth > 0 {
					if text[index] == '<' {
						depth++
					} else if text[index] == '>' {
						depth--
					}
					index++
				}
				if depth != 0 {
					continue
				}
				skipSpace()
			}
			if index >= len(text) || text[index] != '(' {
				continue
			}
			index++
			skipSpace()
			if index >= len(text) || text[index] != '"' {
				continue
			}
			end := index + 1
			for end < len(text) && text[end] != '"' {
				end++
			}
			if end >= len(text) {
				continue
			}
			if command := text[index+1 : end]; frontendCallName.MatchString(command) {
				commands[command] = true
			}
		}
	}
	if len(commands) == 0 {
		t.Fatal("frontend facade extraction returned no commands")
	}
	return commands
}

// desktopOnlyFacadeCommands are called by the frontend but registered only in the desktop
// assembly (local_folder_list/local_file_read walk the machine the process runs on, so the
// server assembly must never expose them); every other facade command must exist in both.
var desktopOnlyFacadeCommands = []string{"local_file_read", "local_folder_list"}

func TestFrontendFacadeCommandsAreRegisteredInProduction(t *testing.T) {
	server, err := NewProduction(t.Context(), ProductionConfig{DataDir: t.TempDir(), Desktop: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	desktop, err := NewProduction(t.Context(), ProductionConfig{DataDir: t.TempDir(), Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = desktop.Shutdown(context.Background()) })

	registeredServer := map[string]bool{}
	for _, command := range server.Dispatcher.Commands() {
		registeredServer[command] = true
	}
	registeredDesktop := map[string]bool{}
	for _, command := range desktop.Dispatcher.Commands() {
		registeredDesktop[command] = true
	}
	facade := frontendFacadeCommands(t)

	var missing []string
	for command := range facade {
		if !registeredDesktop[command] {
			missing = append(missing, command)
			continue
		}
		if !registeredServer[command] && !slices.Contains(desktopOnlyFacadeCommands, command) {
			missing = append(missing, command)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("frontend facade commands without a production registration: %v", missing)
	}

	var backendOnly []string
	for command := range registeredServer {
		if !facade[command] {
			backendOnly = append(backendOnly, command)
		}
	}
	sort.Strings(backendOnly)
	if !slices.Equal(backendOnly, []string{"ai_takeover_resume"}) {
		t.Fatalf("production commands without a frontend wrapper = %v, want [ai_takeover_resume]", backendOnly)
	}

	var desktopBackendOnly []string
	for command := range registeredDesktop {
		if !facade[command] {
			desktopBackendOnly = append(desktopBackendOnly, command)
		}
	}
	sort.Strings(desktopBackendOnly)
	if !slices.Equal(desktopBackendOnly, []string{"ai_takeover_resume"}) {
		t.Fatalf("desktop production commands without a frontend wrapper = %v, want [ai_takeover_resume]", desktopBackendOnly)
	}
}
