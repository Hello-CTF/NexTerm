package guard

import "testing"

func TestR1AllowSessionRemembersEveryCompoundKind(t *testing.T) {
	t.Parallel()
	ruling := ClassifyCommand("touch /tmp/x; kill 123", nil)
	kinds := ruling.ApprovalKinds()
	if len(kinds) != 2 {
		t.Fatalf("approval kinds = %v, ruling = %+v", kinds, ruling)
	}
	memory := NewMemory()
	for _, kind := range kinds {
		memory.Add(kind)
	}
	if decision := Decide(Config{Mode: ReadWrite}, ruling, memory); decision.Action != ActionAllow {
		t.Fatalf("approved compound decision = %+v", decision)
	}
	if decision := Decide(Config{Mode: ReadWrite}, ClassifyCommand("systemctl restart demo", nil), memory); decision.Action != ActionAsk {
		t.Fatalf("unapproved kind decision = %+v", decision)
	}
}
