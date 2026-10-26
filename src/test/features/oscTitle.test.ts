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
    title: "Terminal 1",
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
    seed([terminal("t1", { title: "my terminal", renamedByUser: true, customTitleAt: 1 })]);
    expect(applyRemoteTabTitle("t1", "remote title")).toBe(false);
    expect(titleOf()).toBe("my terminal");
  });

  it("keeps a user rename authoritative even after remote title updates", () => {
    applyRemoteTabTitle("t1", "first remote");
    useUi.getState().updateTab("t1", { title: "locked", renamedByUser: true, customTitleAt: 1 });
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
    expect(titleOf()).toBe("Terminal 1");
    expect(applyRemoteTabTitle("t1", "Terminal 1")).toBe(false);
    expect(titleOf()).toBe("Terminal 1");
  });

  it("unlocks a legacy rename lock without a decision time on load", () => {
    // Older builds recorded backend OSC titles as renames; such a lock may hold an OSC title.
    seed([terminal("t1", { title: "user@host: ~", renamedByUser: true })]);
    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(serializeLayout(useUi.getState()))));
    const tab = parsed?.workspaces[0].panes[0].tabs[0];
    expect(tab?.title).toBe("user@host: ~");
    expect(tab?.renamedByUser).toBeUndefined();
    // In memory the same legacy lock does not block OSC titles either.
    expect(applyRemoteTabTitle("t1", "vim")).toBe(true);
  });

  it("ignores unknown tabs", () => {
    expect(applyRemoteTabTitle("missing", "title")).toBe(false);
  });

  it("sanitizeRemoteTabTitle collapses whitespace and strips control chars", () => {
    expect(sanitizeRemoteTabTitle("  a\t b  c ")).toBe("a b c");
    expect(sanitizeRemoteTabTitle("\x1b]0;abc")).toBe("]0;abc");
  });

  it("keeps emoji sequences whole in remote titles", () => {
    const family = "👨‍👩‍👧‍👦";
    for (const title of [
      `${family} family`,
      "❤️ build",
      "🇯🇵🇺🇸 flags",
      "👍🏽 ok",
      "1️⃣ keycap",
      "🏴\u{e0067}\u{e0062}\u{e0073}\u{e0063}\u{e0074}\u{e007f} scotland",
    ]) {
      expect(applyRemoteTabTitle("t1", title)).toBe(true);
      expect(titleOf()).toBe(title);
    }
    // At the 80 code point cap a straddling cluster is dropped whole, never split.
    const prefix = "中".repeat(76);
    applyRemoteTabTitle("t1", `${prefix}${family}tail`);
    expect(titleOf()).toBe(prefix);
    applyRemoteTabTitle("t1", "🙂".repeat(100));
    expect(titleOf()).toBe("🙂".repeat(80));
    expect(titleOf()).not.toMatch(/[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]|�/);
  });

  it("drops C1 controls and bidi overrides but keeps joiners", () => {
    expect(sanitizeRemoteTabTitle("a\u0085b\u009bc")).toBe("a b c");
    expect(sanitizeRemoteTabTitle("‮evil‬ ⁦x⁩")).toBe("evil x");
    expect(sanitizeRemoteTabTitle("👩‍💻 dev")).toBe("👩‍💻 dev");
  });

  it("an emoji rename and an AI dock title survive a layout round trip", () => {
    const title = "🚀 部署 👨‍👩‍👧‍👦 ❤️";
    seed([terminal("t1", { title, renamedByUser: true, customTitleAt: 1700000000000 })]);
    const longAiTitle = `${"中".repeat(78)}🇯🇵tail`;
    useUi.setState({
      aiBoards: { ws: { tabs: [{ id: "ai1", title: longAiTitle }], activeTabId: "ai1" } },
    } as never);
    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(serializeLayout(useUi.getState()))));
    expect(parsed?.workspaces[0].panes[0].tabs[0]).toMatchObject({ title, renamedByUser: true });
    expect(parsed?.aiBoards?.ws?.tabs[0].title).toBe(`${"中".repeat(78)}🇯🇵`);
    const clipped = `${"中".repeat(79)}🇯🇵`;
    useUi.setState({ aiBoards: { ws: { tabs: [{ id: "ai1", title: clipped }], activeTabId: "ai1" } } } as never);
    const reparsed = sanitizeLayout(JSON.parse(JSON.stringify(serializeLayout(useUi.getState()))));
    expect(reparsed?.aiBoards?.ws?.tabs[0].title).toBe("中".repeat(79));
  });

  it("renamedByUser survives a layout serialize/sanitize round trip", () => {
    seed([terminal("t1", { title: "my terminal", renamedByUser: true, customTitleAt: 1700000000000 })]);
    const dto = serializeLayout(useUi.getState());
    expect(dto).not.toBeNull();
    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed?.workspaces[0].panes[0].tabs[0]).toMatchObject({
      renamedByUser: true,
      customTitleAt: 1700000000000,
    });

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

  it("persists explicit opt-in", () => {
    useUi.getState().setTerminalOsc52(true);
    expect(useUi.getState().terminalOsc52).toBe(true);
    expect(localStorage.getItem("nexterm.terminal.v1")).toBe('{"osc52":true}');
    useUi.getState().setTerminalOsc52(false);
    expect(localStorage.getItem("nexterm.terminal.v1")).toBe('{"osc52":false}');
  });
});
