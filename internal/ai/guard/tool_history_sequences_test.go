package guard

import "testing"

func TestR4RawHistorySequencesMatchTaggedDanger(t *testing.T) {
	t.Parallel()
	for _, keys := range []string{"\x1b[A<enter>", "\x1b[B<enter>", "<up><enter>", "<down><enter>"} {
		t.Run(keys, func(t *testing.T) {
			ruling := ClassifySendKeysWithBuffer(keys, "ls", false, nil)
			if ruling.Risk != Danger {
				t.Fatalf("history ruling = %v, want Danger", ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
				t.Fatalf("silent history decision = %+v, want ActionAsk", decision)
			}
		})
	}
}
