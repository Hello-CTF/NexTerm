package usershell

import "testing"

func TestParsePasswdLine(t *testing.T) {
	if got := ParsePasswdLine("dev:x:1000:1000:Dev:/home/dev:/bin/zsh"); got != "/bin/zsh" {
		t.Fatalf("shell = %q", got)
	}
	if got := ParsePasswdLine("broken:line"); got != "" {
		t.Fatalf("shell = %q, want empty", got)
	}
}

func TestParseDscl(t *testing.T) {
	if got := ParseDscl("UserShell: /bin/zsh\n"); got != "/bin/zsh" {
		t.Fatalf("shell = %q", got)
	}
	if got := ParseDscl("No such key"); got != "" {
		t.Fatalf("shell = %q, want empty", got)
	}
}

func TestUsableRejectsNonShells(t *testing.T) {
	for _, shell := range []string{"", "zsh", "/usr/sbin/nologin", "/nonexistent/shell"} {
		if Usable(shell) {
			t.Fatalf("Usable(%q) = true", shell)
		}
	}
}
