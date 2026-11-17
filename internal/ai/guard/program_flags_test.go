package guard

import "testing"

func TestReadOnlyCommandsThatRunAProgramAreDangerous(t *testing.T) {
	for _, command := range []string{
		"rg --pre ./run.sh pattern .",
		"rg --pre=./run.sh pattern",
		"rg -n --hostname-bin /tmp/x foo",
		"sort --compress-program=sh file",
		"sort --compress=sh file",
		"cat log | rg --pre=sh x",
	} {
		if got := ClassifyCommand(command, nil); got.Risk != Danger {
			t.Errorf("ClassifyCommand(%q) = %s (%v), want dangerous", command, got.Risk, got)
		}
	}
	for _, command := range []string{
		"rg -n --pre-glob '*.gz' foo .",
		"rg -i --glob '*.go' 'func main' .",
		"rg -- --pre file",
		"sort -n file",
		"sort -k2 --parallel=2 file",
	} {
		if got := ClassifyCommand(command, nil); got.Risk != Safe {
			t.Errorf("ClassifyCommand(%q) = %s (%v), want safe", command, got.Risk, got)
		}
	}
}
