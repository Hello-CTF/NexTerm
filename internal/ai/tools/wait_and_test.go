package tools

import (
	"context"
	"fmt"
	"testing"
)

// pattern and lines combine as AND: a wait asking for both ends only when
// both hold, never on either one alone.

func TestWaitForPatternAndLinesCombineAsAND(t *testing.T) {
	tab := fakeTab("t1", "s1", "$ ", 0)
	tab.ring = newFakeRing(0, "")
	fake := newTabFake(tab)
	registry := p1Registry(fake)

	// Pattern present but fewer new lines than required: NOT satisfied.
	tab.ring.append("xready\r\n")
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w1", "wait_for", `{"pattern":"ready","lines":3,"since_seq":1,"timeout_ms":60}`), nil)
	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("pattern-only satisfied the AND wait: %+v", result)
	}

	// Enough new lines but pattern absent: NOT satisfied.
	tab.ring.append("one\r\ntwo\r\nthree\r\n")
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w2", "wait_for", `{"pattern":"missing","lines":2,"since_seq":1,"timeout_ms":60}`), nil)
	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("lines-only satisfied the AND wait: %+v", result)
	}

	// Both hold: satisfied.
	tab.ring.append("ready\r\nmore\r\n")
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w3", "wait_for", `{"pattern":"ready","lines":2,"since_seq":1,"timeout_ms":2000}`), nil)
	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("AND wait did not satisfy: %+v", result)
	}
	if !result.Wait.Tabs[0].Matched || result.Wait.Tabs[0].NewLines < 2 {
		t.Fatalf("AND wait metadata = %+v", result.Wait.Tabs[0])
	}
}

func TestWaitForLinesOnlyAndPatternOnlyUnchanged(t *testing.T) {
	tab := fakeTab("t1", "s1", "$ alpha\r\nbeta\r\n$ ", 0)
	tab.ring = newFakeRing(0, "")
	fake := newTabFake(tab)
	registry := p1Registry(fake)

	// Pattern-only keeps the legacy visible-tail semantics.
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w1", "wait_for", `{"pattern":"beta","timeout_ms":2000}`), nil)
	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("pattern-only wait = %+v", result)
	}

	// Lines-only keeps the anchored new-output semantics.
	tab.ring.append("gamma\r\ndelta\r\n")
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w2", "wait_for", fmt.Sprintf(`{"lines":2,"since_seq":%d,"timeout_ms":2000}`, 1)), nil)
	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("lines-only wait = %+v", result)
	}
}
