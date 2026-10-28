package session

import (
	"slices"
	"testing"
)

func TestDurableTerminalEnvStatesTermAndColor(t *testing.T) {
	if got := durableTerminalEnv(""); !slices.Equal(got, []string{"TERM=xterm-256color", "COLORTERM=truecolor"}) {
		t.Fatalf("default env = %v", got)
	}
	if got := durableTerminalEnv("xterm-test"); !slices.Equal(got, []string{"TERM=xterm-test", "COLORTERM=truecolor"}) {
		t.Fatalf("explicit term env = %v", got)
	}
}
