import { describe, expect, it, vi } from "vitest";
import { registerTerminalLinkSource, terminalLinkSource, type TerminalLinkSource } from "../../features/terminal/terminalLinkRegistry";

function source(url: string): TerminalLinkSource {
  return { extract: () => [{ url, context: "", row: 0 }], open: vi.fn(), openPanel: vi.fn() };
}

describe("terminal link registry", () => {
  it("returns the source registered for a tab and nothing for others", () => {
    const a = source("https://a.test");
    const off = registerTerminalLinkSource("tab-a", a);
    expect(terminalLinkSource("tab-a")).toBe(a);
    expect(terminalLinkSource("tab-b")).toBeUndefined();
    off();
    expect(terminalLinkSource("tab-a")).toBeUndefined();
  });

  it("lets a later registration replace the earlier one", () => {
    const first = source("https://1.test");
    const second = source("https://2.test");
    const offFirst = registerTerminalLinkSource("tab-r", first);
    const offSecond = registerTerminalLinkSource("tab-r", second);
    expect(terminalLinkSource("tab-r")).toBe(second);
    offFirst();
    expect(terminalLinkSource("tab-r")).toBe(second);
    offSecond();
    expect(terminalLinkSource("tab-r")).toBeUndefined();
  });

  it("removes only its own registration", () => {
    const first = source("https://1.test");
    const second = source("https://2.test");
    const offFirst = registerTerminalLinkSource("tab-o", first);
    const offSecond = registerTerminalLinkSource("tab-o", second);
    offSecond();
    // The earlier registration was replaced, so revoking it must not resurrect or delete anything.
    offFirst();
    expect(terminalLinkSource("tab-o")).toBeUndefined();
  });
});
