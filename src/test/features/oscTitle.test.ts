/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  layoutApi: { get: vi.fn(), put: vi.fn() },
  terminalApi: { listLive: vi.fn(), closeTab: vi.fn() },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));

import { sanitizeLayout, serializeLayout } from "../../app/layout";
import {
  applyRemoteTabTitle,
  sanitizeRemoteTabTitle,
  useUi,
  type AppTab,
} from "../../app/store";

function terminal(id = "t1", extra: Partial<AppTab> = {}): AppTab {
  return {
    id,
    kind: "terminal",
    title: "终端 1",
    sessionId: "s",
    tabId: `kernel-${id}`,
    closable: true,
    ...extra,
  };
}

function seed(tabs: AppTab[]) {
  useUi.setState({
    workspaces: [
      {
        id: "ws",
        kind: "session",
        title: "ws",
        panes: [{ id: "pane", tabs, activeTabId: tabs[0]?.id ?? null }],
        activePaneId: "pane",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws",
    sessions: [],
    toasts: [],
  });
}

function titleOf(id = "t1"): string | undefined {
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      const t = p.tabs.find((x) => x.id === id);
      if (t) return t.title;
    }
  }
  return undefined;
}

describe("remote tab titles (OSC 0/2)", () => {
  beforeEach(() => {
    localStorage.clear();
    seed([terminal()]);
  });

  it("updates the tab title when the user has not renamed it", () => {
    expect(applyRemoteTabTitle("t1", "user@host: ~/proj")).toBe(true);
    expect(titleOf()).toBe("user@host: ~/proj");
  });

  it("never overrides a user-renamed tab", () => {
    seed([terminal("t1", { title: "我的终端", renamedByUser: true })]);
    expect(applyRemoteTabTitle("t1", "remote title")).toBe(false);
    expect(titleOf()).toBe("我的终端");
  });

  it("keeps a user rename authoritative even after remote title updates", () => {
    applyRemoteTabTitle("t1", "first remote");
    useUi.getState().updateTab("t1", { title: "locked", renamedByUser: true });
    expect(applyRemoteTabTitle("t1", "second remote")).toBe(false);
    expect(titleOf()).toBe("locked");
  });

  it("sanitizes control characters and caps the length", () => {
    applyRemoteTabTitle("t1", "\x07evil\x00 title\nnext\tline");
    expect(titleOf()).toBe("evil title next line");
    applyRemoteTabTitle("t1", "x".repeat(200));
    expect(titleOf()).toBe("x".repeat(80));
  });

  it("ignores empty and unchanged titles", () => {
    expect(applyRemoteTabTitle("t1", "   \x00 ")).toBe(false);
    expect(titleOf()).toBe("终端 1");
    expect(applyRemoteTabTitle("t1", "终端 1")).toBe(false);
    expect(titleOf()).toBe("终端 1");
  });

  it("ignores unknown tabs", () => {
    expect(applyRemoteTabTitle("missing", "title")).toBe(false);
  });

  it("sanitizeRemoteTabTitle collapses whitespace and strips control chars", () => {
    expect(sanitizeRemoteTabTitle("  a\t b  c ")).toBe("a b c");
    expect(sanitizeRemoteTabTitle("\x1b]0;abc")).toBe("]0;abc");
  });

  it("renamedByUser survives a layout serialize/sanitize round trip", () => {
    seed([terminal("t1", { title: "我的终端", renamedByUser: true })]);
    const dto = serializeLayout(useUi.getState());
    expect(dto).not.toBeNull();
    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed?.workspaces[0].panes[0].tabs[0].renamedByUser).toBe(true);

    seed([terminal("t2")]);
    const plain = serializeLayout(useUi.getState());
    const parsedPlain = sanitizeLayout(JSON.parse(JSON.stringify(plain)));
    expect(parsedPlain?.workspaces[0].panes[0].tabs[0].renamedByUser).toBeUndefined();
  });
});

describe("terminalOsc52 setting", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("is off by default", () => {
    useUi.getState().setTerminalOsc52(false);
    expect(useUi.getState().terminalOsc52).toBe(false);
    expect(localStorage.getItem("nexterm.terminal.v1")).toBe('{"osc52":false}');
  });

  it("persists explicit opt-in", () => {
    useUi.getState().setTerminalOsc52(true);
    expect(useUi.getState().terminalOsc52).toBe(true);
    expect(localStorage.getItem("nexterm.terminal.v1")).toBe('{"osc52":true}');
    useUi.getState().setTerminalOsc52(false);
    expect(localStorage.getItem("nexterm.terminal.v1")).toBe('{"osc52":false}');
  });
});
