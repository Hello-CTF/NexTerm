import { describe, expect, it } from "vitest";
import { runScopeForRefs, type RefChip } from "../../features/ai/panel/aiSidebarShared";

const tab = (refId: string, sessionId: string): RefChip => ({ id: `tab:${refId}`, kind: "tab", label: refId, detail: "", refId, sessionId });

describe("run scope", () => {
  it("targets the one @-referenced terminal when no terminal is active", () => {
    expect(runScopeForRefs({ sessionId: undefined, tabId: undefined }, [tab("tab-2", "session-2")])).toEqual({ sessionId: "session-2", tabId: "tab-2" });
  });

  it("keeps the active terminal, a database scope and an ambiguous choice", () => {
    const active = { sessionId: "session-1", tabId: "tab-1" };
    expect(runScopeForRefs(active, [tab("tab-2", "session-2")])).toBe(active);
    const db = { connId: "conn-1" };
    expect(runScopeForRefs(db, [tab("tab-2", "session-2")])).toBe(db);
    const none = { sessionId: undefined, tabId: undefined };
    expect(runScopeForRefs(none, [tab("tab-2", "session-2"), tab("tab-3", "session-3")])).toBe(none);
    expect(runScopeForRefs(none, [])).toBe(none);
  });
});
