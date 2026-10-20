package guard

import "testing"

func TestPackageManagerReadAndMutationClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		risk    Risk
		kind    Kind
	}{
		{command: "apt list --installed", risk: Safe, kind: KindNone},
		{command: "apt search ripgrep", risk: Safe, kind: KindNone},
		{command: "apt show bash", risk: Safe, kind: KindNone},
		{command: "apt --version", risk: Safe, kind: KindNone},
		{command: "apt install ripgrep", risk: NeedsConfirm, kind: KindPackage},
		{command: "apt-get upgrade", risk: NeedsConfirm, kind: KindPackage},
		{command: "dnf list installed", risk: Safe, kind: KindNone},
		{command: "dnf info bash", risk: Safe, kind: KindNone},
		{command: "dnf search ripgrep", risk: Safe, kind: KindNone},
		{command: "dnf install ripgrep", risk: NeedsConfirm, kind: KindPackage},
		{command: "brew list --formula", risk: Safe, kind: KindNone},
		{command: "brew search ripgrep", risk: Safe, kind: KindNone},
		{command: "brew info ripgrep", risk: Safe, kind: KindNone},
		{command: "brew --version", risk: Safe, kind: KindNone},
		{command: "brew install ripgrep", risk: NeedsConfirm, kind: KindPackage},
		{command: "brew uninstall ripgrep", risk: NeedsConfirm, kind: KindPackage},
		{command: "winget list --name Git", risk: Safe, kind: KindNone},
		{command: "winget search ripgrep", risk: Safe, kind: KindNone},
		{command: "winget show Git.Git", risk: Safe, kind: KindNone},
		{command: "winget --version", risk: Safe, kind: KindNone},
		{command: "winget install Git.Git", risk: NeedsConfirm, kind: KindPackage},
		{command: "apt", risk: NeedsConfirm, kind: KindPackage},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			ruling := ClassifyCommand(test.command, nil)
			if ruling.Risk != test.risk || ruling.Kind != test.kind {
				t.Fatalf("ClassifyCommand(%q) = %+v, want risk=%s kind=%q", test.command, ruling, test.risk, test.kind)
			}
		})
	}
}

func TestInitAndTelinitRunlevelClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		risk    Risk
		kind    Kind
	}{
		{command: "init 0", risk: Danger, kind: KindDanger},
		{command: "init 6", risk: Danger, kind: KindDanger},
		{command: "telinit 0", risk: Danger, kind: KindDanger},
		{command: "telinit 6", risk: Danger, kind: KindDanger},
		{command: "init 3", risk: NeedsConfirm, kind: KindService},
		{command: "telinit 1", risk: NeedsConfirm, kind: KindService},
		{command: "init", risk: NeedsConfirm, kind: KindService},
		{command: "telinit", risk: NeedsConfirm, kind: KindService},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			ruling := ClassifyCommand(test.command, nil)
			if ruling.Risk != test.risk || ruling.Kind != test.kind {
				t.Fatalf("ClassifyCommand(%q) = %+v, want risk=%s kind=%q", test.command, ruling, test.risk, test.kind)
			}
		})
	}
}
