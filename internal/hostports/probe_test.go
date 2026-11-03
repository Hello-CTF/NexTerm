package hostports

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestValidateProbeRejectsInjection(t *testing.T) {
	hosts := []string{
		";id", "$(id)", "`id`", "a b", "a\nb", "-oProxyCommand=x", "--help", "-1", "a'b", `a"b`, "a|b", "a&b", `a\b`,
		"ｅｘａｍｐｌｅ.com", "例え.jp", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 130) + "com", "", "-", "a-.com", "-a.com",
		"a..b", "fe80::1%eth0", "[::1", "host$", "a;b", "a\tb", "a\x00b",
	}
	for _, host := range hosts {
		if err := ValidateProbe(ProbeRequest{Method: "tcp", Host: host, Port: 22}); err == nil {
			t.Errorf("host %q accepted", host)
		}
	}
	urls := []string{
		"", "file:///etc/passwd", "javascript:alert(1)", "ftp://example.com/", "http://user:pw@example.com/", "http://user@example.com/",
		"http://example.com/a b", "http://example.com/\n", "http://example.com/'x", `http://example.com/"x`, "http://example.com/`id`",
		"http://example.com/$HOME", `http://example.com/\x`, "http://-oProxyCommand=x/", "http://exa mple.com/", "http://;id/",
		"http://example.com/" + strings.Repeat("a", 2100), "http://ｅxample.com/", "http:///path", "http://example.com:99999/", "-o /tmp/x",
		"http://example.com/{x}", "http://example.com/|x", "http://example.com/\x7f",
	}
	for _, u := range urls {
		if err := ValidateProbe(ProbeRequest{Method: "http", URL: u}); err == nil {
			t.Errorf("url %q accepted", u)
		}
	}
	for _, bad := range []ProbeRequest{{Method: "tcp", Host: "example.com"}, {Method: "ssh", Host: "example.com", Port: 1}, {Host: "a", Port: 1}} {
		if err := ValidateProbe(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestValidateProbeAcceptsOrdinaryTargets(t *testing.T) {
	good := []ProbeRequest{
		{Method: "tcp", Host: "127.0.0.1", Port: 5432}, {Method: "tcp", Host: "[::1]", Port: 80}, {Method: "tcp", Host: "::1", Port: 80},
		{Method: "tcp", Host: "db-1.internal.example.com", Port: 65535}, {Method: "tcp", Host: "localhost", Port: 1},
		{Method: "http", URL: "http://localhost:3000/"}, {Method: "https", URL: "https://example.com/a/b?x=1&y=2#frag"}, {Method: "http", URL: "http://[::1]:8080/health"},
	}
	for _, req := range good {
		if req.Method == "https" {
			req.Method = "http"
		}
		if err := ValidateProbe(req); err != nil {
			t.Errorf("%+v rejected: %v", req, err)
		}
	}
}

func TestProbeCommandWindowsWrapperRefusesShellMetacharacters(t *testing.T) {
	if _, err := ProbeCommand(ShellUnknown, ProbeRequest{Method: "http", URL: "http://example.com/?a=1&b=2"}); err == nil {
		t.Fatal("ampersand URL accepted for a cmd host")
	}
	command, err := ProbeCommand(ShellUnknown, ProbeRequest{Method: "tcp", Host: "example.com", Port: 22})
	if err != nil || !strings.HasPrefix(command, "powershell -NoProfile -NonInteractive -Command \"") {
		t.Fatalf("%q %v", command, err)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(command, "powershell -NoProfile -NonInteractive -Command \""), "\"")
	if strings.ContainsAny(inner, "\"%^&|<>!") {
		t.Fatalf("wrapper body contains forbidden characters: %q", inner)
	}
	ps, err := ProbeCommand(ShellPowerShell, ProbeRequest{Method: "http", URL: "http://example.com/?a=1&b=2"})
	if err != nil || !strings.Contains(ps, "'http://example.com/?a=1&b=2'") {
		t.Fatalf("%q %v", ps, err)
	}
}

func TestQuoteHelpers(t *testing.T) {
	if got := quotePOSIX("a'b"); got != `'a'\''b'` {
		t.Fatal(got)
	}
	if got := quotePowerShell("a'b"); got != `'a''b'` {
		t.Fatal(got)
	}
}

// writeFake installs an executable that records each argument on its own line.
func writeFake(t *testing.T, dir, name string) string {
	t.Helper()
	log := filepath.Join(dir, name+".args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + log + "'; done\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

func runPOSIX(t *testing.T, command, path string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = []string{"PATH=" + path}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s\n%s", err, out, command)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestProbeCommandPassesValuesAsDiscreteArguments(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	ncLog := writeFake(t, dir, "nc")
	curlLog := writeFake(t, dir, "curl")

	command, err := ProbeCommand(ShellPOSIX, ProbeRequest{Method: "tcp", Host: "[::1]", Port: 5432})
	if err != nil {
		t.Fatal(err)
	}
	runPOSIX(t, command, dir)
	if got, want := readLines(t, ncLog), []string{"-z", "-v", "-w", "5", "::1", "5432"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nc argv = %q want %q", got, want)
	}

	url := "https://example.com/a?x=1&y=2;z=(3)#f"
	command, err = ProbeCommand(ShellPOSIX, ProbeRequest{Method: "http", URL: url})
	if err != nil {
		t.Fatal(err)
	}
	runPOSIX(t, command, dir)
	want := []string{"-sS", "-o", "/dev/null", "-w", "%{http_code} %{time_total}\\n", "--max-time", "10", "--proto", "=http,https", "--", url}
	if got := readLines(t, curlLog); !reflect.DeepEqual(got, want) {
		t.Fatalf("curl argv = %q want %q", got, want)
	}
}

func TestProbeCommandTCPFallsBackToBash(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	timeoutLog := writeFake(t, dir, "timeout")
	if err := os.WriteFile(filepath.Join(dir, "bash"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command, err := ProbeCommand(ShellPOSIX, ProbeRequest{Method: "tcp", Host: "example.com", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	runPOSIX(t, command, dir)
	want := []string{"5", "bash", "-c", `exec 3<>"/dev/tcp/$0/$1"`, "example.com", "22"}
	if got := readLines(t, timeoutLog); !reflect.DeepEqual(got, want) {
		t.Fatalf("timeout argv = %q want %q", got, want)
	}
}

func TestServiceProbeDryRunAndExecution(t *testing.T) {
	zero := 0
	runner := &fakeRunner{reply: func(string) (base.ExecResult, error) {
		return base.ExecResult{Stdout: strings.Repeat("x", 20000), ExitCode: &zero}, nil
	}}
	service := NewService(resolverFor(Target{Runner: runner, Shell: ShellPOSIX}))
	req := ProbeRequest{Method: "tcp", Host: "example.com", Port: 22}
	dry, err := service.Probe(t.Context(), ProbeArgs{SessionID: "s", ProbeRequest: req, DryRun: true})
	if err != nil || dry.Ran || dry.Command == "" || len(runner.commands()) != 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	res, err := service.Probe(t.Context(), ProbeArgs{SessionID: "s", ProbeRequest: req})
	if err != nil || !res.Ran || !res.OK || res.Command != dry.Command || len(res.Output) > 8192 || len(res.Output) < 8000 {
		t.Fatalf("run: ok=%v ran=%v len=%d err=%v", res.OK, res.Ran, len(res.Output), err)
	}
	if _, err := service.Probe(t.Context(), ProbeArgs{SessionID: "s", ProbeRequest: ProbeRequest{Method: "tcp", Host: ";id", Port: 1}}); err == nil {
		t.Fatal("invalid probe ran")
	}
	if n := len(runner.commands()); n != 1 {
		t.Fatalf("commands = %d", n)
	}
	one := 1
	failing := &fakeRunner{reply: func(string) (base.ExecResult, error) { return base.ExecResult{Stderr: "refused", ExitCode: &one}, nil }}
	res, err = NewService(resolverFor(Target{Runner: failing, Shell: ShellPOSIX})).Probe(context.Background(), ProbeArgs{SessionID: "s", ProbeRequest: req})
	if err != nil || res.OK || res.ExitCode == nil || *res.ExitCode != 1 || !strings.Contains(res.Output, "refused") {
		t.Fatalf("failure: %+v %v", res, err)
	}
}
