import { describe, expect, it } from "vitest";
import { composeUserMessage, currentAtQuery, splitUserRefs } from "../features/ai/userRefs";
import { searchableTextOf } from "../features/ai/conversationSearch";
import { aiTabBusyIds, clearAiTabRun, getAiTabBusyVersion, setAiTabBusy, subscribeAiTabBusy } from "../features/ai/runOwnership";

describe("user message references", () => {
  it("round-trips the reference prefix", () => {
    const text = composeUserMessage("为什么报错", [{ label: "web\n01", detail: "prod · 工作区" }]);
    expect(splitUserRefs(text)).toEqual({ refs: ["web 01（prod · 工作区）"], body: "为什么报错" });
  });

  it("keeps text without a well-formed prefix as body", () => {
    expect(splitUserRefs("[引用] 是什么")).toEqual({ refs: [], body: "[引用] 是什么" });
    expect(splitUserRefs("[引用]\nplain")).toEqual({ refs: [], body: "[引用]\nplain" });
    expect(composeUserMessage("hi", [])).toBe("hi");
  });

  it("searches the visible text, not the prefix header", () => {
    const text = composeUserMessage("查看日志", [{ label: "build", detail: "ci" }]);
    const searchable = searchableTextOf({ id: "u", role: "user", text, attempt: null } as never);
    expect(searchable).toContain("build（ci）");
    expect(searchable).toContain("查看日志");
    expect(searchable).not.toContain("[引用]");
  });

  it("reads the @ query only while a reference is being typed", () => {
    expect(currentAtQuery("@")).toBe("");
    expect(currentAtQuery("看看 @web")).toBe("web");
    expect(currentAtQuery("看看 @web 的输出")).toBeNull();
    expect(currentAtQuery("mail@example")).toBeNull();
    expect(currentAtQuery("no mention")).toBeNull();
  });
});

describe("AI tab busy state", () => {
  it("notifies listeners when a run is cleared", () => {
    let notified = 0;
    const off = subscribeAiTabBusy(() => {
      notified += 1;
    });
    setAiTabBusy("tab-clear", true);
    const before = getAiTabBusyVersion();
    clearAiTabRun("tab-clear");
    off();
    expect(aiTabBusyIds.has("tab-clear")).toBe(false);
    expect(getAiTabBusyVersion()).toBeGreaterThan(before);
    expect(notified).toBe(2);
  });
});
