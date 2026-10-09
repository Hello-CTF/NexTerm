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

// 前端 IPC facade(src/ipc 下直接发起 call 的四个文件)的命令钉值必须全部落在
// production dispatcher 注册面上; 反向差集只允许 ai_takeover_resume 这一个纯后端
// 入口(前端只接 ai_takeover_enter/run/exit)。
var frontendFacadeFiles = []string{
	filepath.Join("..", "..", "..", "src", "ipc", "commands.ts"),
	filepath.Join("..", "..", "..", "src", "ipc", "cron.ts"),
	filepath.Join("..", "..", "..", "src", "ipc", "grantApi.ts"),
	filepath.Join("..", "..", "..", "src", "ipc", "memory.ts"),
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

func TestFrontendFacadeCommandsAreRegisteredInProduction(t *testing.T) {
	production, err := NewProduction(t.Context(), ProductionConfig{DataDir: t.TempDir(), Desktop: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	registered := map[string]bool{}
	for _, command := range production.Dispatcher.Commands() {
		registered[command] = true
	}
	facade := frontendFacadeCommands(t)

	var missing []string
	for command := range facade {
		if !registered[command] {
			missing = append(missing, command)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("frontend facade commands without a production registration: %v", missing)
	}

	var backendOnly []string
	for command := range registered {
		if !facade[command] {
			backendOnly = append(backendOnly, command)
		}
	}
	sort.Strings(backendOnly)
	if !slices.Equal(backendOnly, []string{"ai_takeover_resume"}) {
		t.Fatalf("production commands without a frontend wrapper = %v, want [ai_takeover_resume]", backendOnly)
	}
}
