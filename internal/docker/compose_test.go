package docker

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

type fakeDockerCLI struct {
	mu       sync.Mutex
	commands []string
	results  map[string]CommandResult
	handler  func(string) CommandResult
}

func (f *fakeDockerCLI) RunCommand(_ context.Context, command string) (CommandResult, error) {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	f.mu.Unlock()
	if f.handler != nil {
		return f.handler(command), nil
	}
	if result, ok := f.results[command]; ok {
		return result, nil
	}
	return CommandResult{}, nil
}

func (f *fakeDockerCLI) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, command := range f.commands {
		if strings.HasPrefix(command, prefix) {
			count++
		}
	}
	return count
}

func (f *fakeDockerCLI) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func commandFailure(code int, message string) CommandResult {
	return CommandResult{ExitCode: &code, Stderr: message}
}

func composeInspect(project, workingDir string, files ...string) string {
	raw, _ := json.Marshal(map[string]any{"Config": map[string]any{"Labels": map[string]string{
		composeProjectLabel: project, composeWorkingDirLabel: workingDir, composeConfigFilesLabel: strings.Join(files, ","),
	}}})
	return string(raw)
}

func composePS(id, name, project, service string) string {
	raw, _ := json.Marshal(map[string]string{
		"ID": id, "Names": name, "Image": "example:latest", "State": "running", "Status": "Up",
		"Labels": composeProjectLabel + "=" + project + "," + composeServiceLabel + "=" + service,
	})
	return string(raw) + "\n"
}

func TestComposeStacksLabelsGroupingAndProbeCache(t *testing.T) {
	cli := &fakeDockerCLI{results: map[string]CommandResult{
		"docker 'ps' '-a' '--format' '{{json .}}'": {Stdout: composePS("c1", "shop-web-1", "shop", "web") + composePS("c2", "shop-db-1", "shop", "db") + composePS("c3", "other-web-1", "other", "web")},
		"docker 'inspect' 'c1'":                    {Stdout: composeInspect("shop", "/srv/shop", "compose.yml", "compose.prod.yml")},
		"docker 'inspect' 'c3'":                    {Stdout: composeInspect("other", "/srv/other", "compose.yml")},
	}}
	backend := NewCommandBackend(cli, nil, ShellPOSIX)
	service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: backend})
	overview, err := service.ComposeStacks(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !overview.Available || overview.CLI != ComposeCLIPlugin || len(overview.Stacks) != 2 {
		t.Fatalf("unexpected overview: %+v", overview)
	}
	shop := overview.Stacks[1]
	if shop.Project != "shop" || strings.Join(shop.Services, ",") != "db,web" || len(shop.ConfigFiles) != 2 {
		t.Fatalf("unexpected shop stack: %+v", shop)
	}
	if shop.ConfigFiles[0] != "/srv/shop/compose.yml" || shop.ConfigFiles[1] != "/srv/shop/compose.prod.yml" || shop.EnvPath != "/srv/shop/.env" || !shop.EnvExists {
		t.Fatalf("compose paths were not resolved: %+v", shop)
	}
	if _, err := service.ComposeStacks(t.Context(), "s1"); err != nil {
		t.Fatal(err)
	}
	if got := cli.count("docker 'compose' 'version'"); got != 1 {
		t.Fatalf("compose plugin probes = %d, want 1; commands=%q", got, cli.all())
	}
	if got := cli.count("docker-compose 'version'"); got != 0 {
		t.Fatalf("legacy probe ran after plugin success: %d", got)
	}
}

func TestComposeLegacyAndReadOnlyDegradation(t *testing.T) {
	t.Run("legacy is cached", func(t *testing.T) {
		cli := &fakeDockerCLI{handler: func(command string) CommandResult {
			switch command {
			case "docker 'ps' '-a' '--format' '{{json .}}'":
				return CommandResult{Stdout: composePS("c1", "web", "shop", "web")}
			case "docker 'inspect' 'c1'":
				return CommandResult{Stdout: composeInspect("shop", "/srv/shop", "compose.yml")}
			case "docker 'compose' 'version'":
				return commandFailure(127, "unknown command")
			default:
				return CommandResult{}
			}
		}}
		service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(cli, nil, ShellPOSIX)})
		overview, err := service.ComposeStacks(t.Context(), "s1")
		if err != nil || overview.CLI != ComposeCLILegacy {
			t.Fatalf("overview = %+v, %v", overview, err)
		}
		output, err := service.ComposeAction(t.Context(), ComposeActionRequest{SessionID: "s1", Project: "shop", Action: ComposeActionUp})
		if err != nil || output != "" {
			t.Fatalf("up = %q, %v", output, err)
		}
		commands := cli.all()
		if got := commands[len(commands)-1]; got != "docker-compose '--project-directory' '/srv/shop' '--file' '/srv/shop/compose.yml' '--project-name' 'shop' 'up' '--detach'" {
			t.Fatalf("legacy up command = %q", got)
		}
		if cli.count("docker 'compose' 'version'") != 1 || cli.count("docker-compose 'version'") != 1 {
			t.Fatalf("probe was not cached: %q", commands)
		}
	})

	t.Run("missing files make only the stack read-only", func(t *testing.T) {
		cli := &fakeDockerCLI{handler: func(command string) CommandResult {
			switch command {
			case "docker 'ps' '-a' '--format' '{{json .}}'":
				return CommandResult{Stdout: composePS("c1", "web", "shop", "web")}
			case "docker 'inspect' 'c1'":
				return CommandResult{Stdout: composeInspect("shop", "/srv/shop", "missing.yml")}
			case "test -f '/srv/shop/missing.yml'":
				return commandFailure(1, "missing")
			default:
				return CommandResult{}
			}
		}}
		service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(cli, nil, ShellPOSIX)})
		overview, err := service.ComposeStacks(t.Context(), "s1")
		if err != nil || len(overview.Stacks) != 1 || !overview.Stacks[0].ReadOnly {
			t.Fatalf("overview = %+v, %v", overview, err)
		}
		if _, err := service.ComposeAction(t.Context(), ComposeActionRequest{SessionID: "s1", Project: "shop", Action: ComposeActionDown}); err == nil {
			t.Fatal("read-only stack action unexpectedly succeeded")
		}
		if cli.count("docker 'compose' '--project-directory'") != 0 {
			t.Fatalf("mutation ran for missing files: %q", cli.all())
		}
	})

	t.Run("unsupported compose does not drop containers", func(t *testing.T) {
		cli := &fakeDockerCLI{handler: func(command string) CommandResult {
			switch command {
			case "docker 'ps' '-a' '--format' '{{json .}}'":
				return CommandResult{Stdout: composePS("c1", "web", "shop", "web")}
			case "docker 'inspect' 'c1'":
				return CommandResult{Stdout: composeInspect("shop", "/srv/shop", "compose.yml")}
			case "docker 'compose' 'version'", "docker-compose 'version'":
				return commandFailure(127, "not found")
			default:
				return CommandResult{}
			}
		}}
		service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(cli, nil, ShellPOSIX)})
		overview, err := service.ComposeStacks(t.Context(), "s1")
		if err != nil || overview.Available || len(overview.Stacks) != 1 || !overview.Stacks[0].ReadOnly {
			t.Fatalf("overview = %+v, %v", overview, err)
		}
	})
}

func TestComposeCommandQuotesUntrustedLabels(t *testing.T) {
	project := "shop';printf injected;#"
	workingDir := "/srv/shop';printf injected;#"
	file := workingDir + "/compose prod.yml"
	cli := &fakeDockerCLI{handler: func(command string) CommandResult {
		switch command {
		case "docker 'ps' '-a' '--format' '{{json .}}'":
			return CommandResult{Stdout: composePS("c1", "web", project, "web")}
		case "docker 'inspect' 'c1'":
			return CommandResult{Stdout: composeInspect(project, workingDir, file)}
		default:
			return CommandResult{}
		}
	}}
	service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(cli, nil, ShellPOSIX)})
	if _, err := service.ComposeAction(t.Context(), ComposeActionRequest{SessionID: "s1", Project: project, Action: ComposeActionRestart}); err != nil {
		t.Fatal(err)
	}
	commands := cli.all()
	got := commands[len(commands)-1]
	want := "docker 'compose' '--project-directory' " + quoteShell(workingDir, ShellPOSIX) +
		" '--file' " + quoteShell(file, ShellPOSIX) + " '--project-name' " + quoteShell(project, ShellPOSIX) + " 'restart'"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if !strings.Contains(got, `'"'"'`) {
		t.Fatalf("single quote was not escaped: %q", got)
	}
}

func TestComposeStackRejectsMissingMetadata(t *testing.T) {
	stack := composeStackFromLabels("shop", map[string]string{composeProjectLabel: "shop"})
	if !stack.ReadOnly || stack.Reason == "" {
		t.Fatalf("missing labels did not degrade to read-only: %+v", stack)
	}
}
