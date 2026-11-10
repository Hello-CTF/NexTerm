package supervisor

import "testing"

func TestWrapShellIntegrationSkipsBashOnWindows(t *testing.T) {
	previous := hostGOOS
	hostGOOS = "windows"
	t.Cleanup(func() { hostGOOS = previous })
	for _, command := range [][]string{
		{`C:\Program Files\Git\bin\bash.exe`, "-i"},
		{`C:\Program Files\Git\bin\bash.exe`},
		{"bash", "-l"},
	} {
		if launch, ok := wrapShellIntegration(command, nil); ok {
			launch.cleanup()
			t.Fatalf("wrapShellIntegration(%q) wrapped bash on Windows", command)
		}
	}
}
