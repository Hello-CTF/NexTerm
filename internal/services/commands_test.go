package services

import (
	"errors"
	"strings"
	"testing"
)

func TestSystemdCommandsQuoteUnitAndRestrictActions(t *testing.T) {
	t.Parallel()
	unit := "nginx'; touch /tmp/pwn; echo '.service"
	command, err := SystemdActionCommand(ActionRestart, unit, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `systemctl restart -- 'nginx'"'"'; touch /tmp/pwn; echo '"'"'.service'`
	if command != want {
		t.Fatalf("restart command = %q, want %q", command, want)
	}
	if !strings.Contains(command, " -- '") {
		t.Fatalf("unit is not separated from options: %q", command)
	}
	if _, err := SystemdActionCommand("restart; touch /tmp/pwn", "nginx.service", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid action error = %v", err)
	}
	for _, invalid := range []string{"", " nginx.service", "nginx.service\n", "nginx.service\x00"} {
		if _, err := SystemdActionCommand(ActionStatus, invalid, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("unit %q error = %v", invalid, err)
		}
	}
}

func TestSudoAndKillCommandsAreNonInteractiveAndNumeric(t *testing.T) {
	t.Parallel()
	command, err := SystemdActionCommand(ActionStatus, "nginx.service", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`if [ "$(id -u)" -eq 0 ]`, "sudo -n -- systemctl status", "(exit 127)"} {
		if !strings.Contains(command, part) {
			t.Errorf("sudo command %q does not contain %q", command, part)
		}
	}
	if strings.Contains(command, "sudo -S") || strings.Contains(command, "sudo -A") {
		t.Fatalf("sudo command unexpectedly requests a password: %q", command)
	}

	kill, err := KillCommand(4321, false)
	if err != nil || kill != "kill -TERM -- 4321" {
		t.Fatalf("kill command = %q, %v", kill, err)
	}
	for _, pid := range []int{0, -1, -999} {
		if _, err := KillCommand(pid, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("kill pid %d error = %v", pid, err)
		}
	}
}

func TestFinderCommandChecksSSBeforeNetstatAndAlwaysReportsPS(t *testing.T) {
	t.Parallel()
	command := FinderCommand(true)
	for _, part := range []string{
		"command -v ss", "ss -ltnp", "elif command -v netstat", "netstat -ltnp",
		"ps -eo pid,user,%cpu,%mem,etime,args", "missing ss and netstat", "missing ps", "__NEXTERM_PS_END__",
	} {
		if !strings.Contains(command, part) {
			t.Errorf("finder command does not contain %q", part)
		}
	}
	if strings.Index(command, "command -v ss") > strings.Index(command, "command -v netstat") {
		t.Fatal("netstat is checked before ss")
	}
	if strings.ContainsAny(command, "\r\n\x00") {
		t.Fatalf("finder command is not one line: %q", command)
	}
}
