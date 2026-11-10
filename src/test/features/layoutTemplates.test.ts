/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  assetGet: vi.fn(),
  snippetList: vi.fn(),
  sessionConnect: vi.fn(),
  sessionList: vi.fn(),
  prepareDurable: vi.fn(),
  terminalWrite: vi.fn(),
  vaultStatus: vi.fn(),
  ask: vi.fn(),
  promptText: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, get: mocks.assetGet, snippetList: mocks.snippetList, credentialRequired: vi.fn().mockResolvedValue(false) },
    sessionApi: {
      ...actual.sessionApi,
      connect: mocks.sessionConnect,
      list: mocks.sessionList,
      prepareDurable: mocks.prepareDurable,
      probeHostKey: vi.fn().mockResolvedValue({ known: true }),
    },
    terminalApi: { ...actual.terminalApi, write: mocks.terminalWrite },
    vaultApi: { status: mocks.vaultStatus, unlock: vi.fn() },
  };
});

import { useUi, type Workspace } from "../../app/store";
import { registerAccountPreferenceStore, type AccountPreferenceView } from "../../app/preferences";
import {
  captureLayoutTemplate,
  getLayoutTemplates,
  layoutTemplateError,
  layoutTemplateListError,
  layoutTemplatePanes,
  parseLayoutTemplates,
  saveLayoutTemplates,
  withPaneSnippet,
  type LayoutTemplate,
  type LayoutTemplateNode,
} from "../../app/layoutTemplates";
import { LAYOUT_TERMINAL_READY_TIMEOUT_MS, openLayoutTemplate, saveWorkspaceAsLayoutTemplate } from "../../app/layoutTemplateOpen";
import { workspaceLayoutTree } from "../../app/paneLayout";
import { paneRects, type SplitNode } from "../../app/splitTree";
import type { Asset, SessionInfo } from "../../ipc/commands";
import { flushMacrotask } from "./reactTestUtils";

const ASSET: Asset = {
  id: "asset-1", groupId: null, kind: "ssh", name: "web-01", host: "10.0.0.1", port: 22, username: "root",
  authKind: "agent", keyPath: null, credId: null, options: {}, tags: "", note: "", sort: 0,
  createdAt: 0, updatedAt: 0, deletedAt: null, builtin: false,
};

const SESSION: SessionInfo = {
  id: "s1", assetId: "asset-1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0,
};

const SNIPPET = {
  id: "snip-1", groupId: null, name: "tail logs", body: "tail -f /var/log/app.log", sort: 0,
  createdAt: 0, updatedAt: 0, description: "", steps: [],
};

const THREE_PANES: LayoutTemplateNode = {
  type: "split",
  dir: "row",
  ratio: 0.6,
  a: { type: "leaf", snippetId: "snip-1" },
  b: { type: "split", dir: "col", ratio: 0.3, a: { type: "leaf" }, b: { type: "leaf", snippetId: "snip-gone" } },
};

function template(overrides: Partial<LayoutTemplate> = {}): LayoutTemplate {
  return { id: "lt-1", name: "部署", assetId: "asset-1", layout: THREE_PANES, ...overrides };
}

function balanced(depth: number): LayoutTemplateNode {
  if (depth === 0) return { type: "leaf" };
  return { type: "split", dir: "col", ratio: 0.5, a: balanced(depth - 1), b: balanced(depth - 1) };
}

function shape(node: SplitNode): unknown {
  return node.type === "leaf" ? "leaf" : { dir: node.dir, ratio: node.ratio, a: shape(node.a), b: shape(node.b) };
}

function hostWorkspace(): Workspace | undefined {
  return useUi.getState().workspaces.find((workspace) => workspace.assetId === "asset-1");
}

function terminalTabs(workspace: Workspace | undefined) {
  return (workspace?.panes ?? []).map((pane) => pane.tabs.filter((tab) => tab.kind === "terminal"));
}

// attachTerminals plays the part of the mounted terminal views: each terminal tab gets its backend terminal id.
function attachTerminals(): void {
  for (const pane of hostWorkspace()?.panes ?? []) {
    for (const tab of pane.tabs) {
      if (tab.kind === "terminal" && !tab.tabId) useUi.getState().updateTab(tab.id, { tabId: `term-${pane.id}` });
    }
  }
}

function toastTexts(): string[] {
  return mocks.toast.mock.calls.map((call) => String(call[1]));
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  registerAccountPreferenceStore(null);
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    workspaceRuntime: {},
    connectionProblems: {},
    connectingAssetIds: [],
    pushToast: mocks.toast,
  });
  mocks.assetGet.mockResolvedValue(ASSET);
  mocks.snippetList.mockResolvedValue([SNIPPET]);
  mocks.sessionConnect.mockResolvedValue(SESSION);
  mocks.sessionList.mockResolvedValue([]);
  mocks.prepareDurable.mockResolvedValue({ state: "unavailable", reason: "test" });
  mocks.terminalWrite.mockResolvedValue(undefined);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  registerAccountPreferenceStore(null);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [] });
});

describe("layout template model", () => {
  it("captures the split shape, ratios and host of a workspace but no live sessions", () => {
    const tree: SplitNode = {
      type: "split", id: "n1", dir: "row", ratio: 0.62345678,
      a: { type: "leaf", id: "n2", paneId: "pa" },
      b: { type: "split", id: "n3", dir: "col", ratio: 0.4, a: { type: "leaf", id: "n4", paneId: "pb" }, b: { type: "leaf", id: "n5", paneId: "pc" } },
    };
    const workspace: Workspace = {
      id: "ws", kind: "session", title: "web-01", sessionId: "s1", assetId: "asset-1", closable: true,
      panes: ["pa", "pb", "pc"].map((id) => ({
        id, activeTabId: `t-${id}`,
        tabs: [{ id: `t-${id}`, kind: "terminal" as const, title: "终端", sessionId: "s1", tabId: `live-${id}`, closable: true }],
      })),
      activePaneId: "pa",
      layoutTree: tree,
    };
    const captured = captureLayoutTemplate(workspace, "  三窗格  ", "lt-x");
    expect(captured).toEqual({
      template: {
        id: "lt-x",
        name: "三窗格",
        assetId: "asset-1",
        layout: {
          type: "split", dir: "row", ratio: 0.6235,
          a: { type: "leaf" },
          b: { type: "split", dir: "col", ratio: 0.4, a: { type: "leaf" }, b: { type: "leaf" } },
        },
      },
    });
    expect(JSON.stringify(captured)).not.toMatch(/s1|live-|"pa"/);
  });

  it("refuses workspaces without a saved host", () => {
    const base = { id: "ws", title: "x", closable: true, panes: [{ id: "p", tabs: [], activeTabId: null }], activePaneId: "p" };
    expect(captureLayoutTemplate({ ...base, kind: "tools" }, "x")).toHaveProperty("error");
    expect(captureLayoutTemplate({ ...base, kind: "session", sessionId: "quick" }, "x")).toHaveProperty("error");
  });

  it("enforces the pane, name and list limits the backend enforces", () => {
    expect(layoutTemplateError(template({ layout: balanced(4) }))).toBeNull();
    expect(layoutTemplateError(template({ layout: { type: "split", dir: "row", ratio: 0.5, a: balanced(4), b: { type: "leaf" } } }))).toMatch("16");
    expect(layoutTemplateError(template({ name: "名".repeat(65) }))).not.toBeNull();
    expect(layoutTemplateError(template({ name: " " }))).not.toBeNull();
    expect(layoutTemplateError(template({ layout: { type: "split", dir: "row", ratio: 0.95, a: { type: "leaf" }, b: { type: "leaf" } } }))).not.toBeNull();
    const many = Array.from({ length: 31 }, (_, index) => template({ id: `lt-${index}` }));
    expect(layoutTemplateListError(many)).not.toBeNull();
    expect(layoutTemplateListError([template(), template()])).not.toBeNull();
  });

  it("keeps only the valid stored templates", () => {
    const stored = [template(), { ...template({ id: "lt-2" }), layout: { type: "tab" } }, "junk", template({ id: "lt-3", name: "" })];
    expect(parseLayoutTemplates(stored)?.map((entry) => entry.id)).toEqual(["lt-1"]);
    expect(parseLayoutTemplates({})).toBeNull();
  });

  it("keeps a template that carries fields from a newer build and drops those fields", () => {
    const newer = {
      ...template({ layout: { type: "split", dir: "row", ratio: 0.5, a: { type: "leaf", snippetId: "snip-1" }, b: { type: "leaf" } } }),
      color: "red",
    };
    (newer.layout as unknown as Record<string, unknown>).label = "x";
    ((newer.layout as { a: unknown }).a as Record<string, unknown>).cwd = "/srv";
    const parsed = parseLayoutTemplates([newer]);
    expect(parsed).toHaveLength(1);
    expect(JSON.stringify(parsed)).not.toMatch(/color|label|cwd/);
    expect(parsed![0].layout).toEqual({ type: "split", dir: "row", ratio: 0.5, a: { type: "leaf", snippetId: "snip-1" }, b: { type: "leaf" } });
    // Known fields on the wrong node kind stay invalid.
    expect(layoutTemplateError(template({ layout: { type: "leaf", dir: "row" } as unknown as LayoutTemplateNode }))).not.toBeNull();
  });

  it("sets and clears the snippet of one pane in reading order", () => {
    const next = withPaneSnippet(THREE_PANES, 1, "snip-2");
    expect(layoutTemplatePanes(next).map((pane) => pane.snippetId)).toEqual(["snip-1", "snip-2", "snip-gone"]);
    expect(layoutTemplatePanes(withPaneSnippet(next, 0, undefined))[0]).toEqual({ type: "leaf" });
  });

  it("saves templates to the account preference when signed in", async () => {
    const data: Record<string, unknown> = {};
    const store = {
      async getView(): Promise<AccountPreferenceView> {
        return { defaults: {}, overrides: { ...data } };
      },
      putOverrides: vi.fn(async (set: Record<string, unknown>) => {
        Object.assign(data, set);
      }),
      async deleteOverrides() {},
    };
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    await flushMacrotask();
    await saveLayoutTemplates([template()]);
    expect(store.putOverrides).toHaveBeenCalledWith({ "workspace.layoutTemplates": [template()] });
    expect(getLayoutTemplates()).toEqual([template()]);
    await expect(saveLayoutTemplates([template({ layout: balanced(5) })])).rejects.toThrow();
  });
});

describe("saving a workspace as a template", () => {
  it("asks for a name and stores the layout", async () => {
    useUi.setState({
      workspaces: [{
        id: "ws", kind: "session", title: "web-01", sessionId: "s1", assetId: "asset-1", closable: true,
        panes: [{ id: "pa", tabs: [], activeTabId: null }, { id: "pb", tabs: [], activeTabId: null }], activePaneId: "pa",
        layoutTree: { type: "split", id: "n", dir: "row", ratio: 0.5, a: { type: "leaf", id: "a", paneId: "pa" }, b: { type: "leaf", id: "b", paneId: "pb" } },
      }],
    });
    mocks.promptText.mockResolvedValue("双栏");
    const saved = await saveWorkspaceAsLayoutTemplate("ws");
    expect(saved?.name).toBe("双栏");
    expect(getLayoutTemplates()).toEqual([saved]);
    expect(layoutTemplatePanes(saved!.layout)).toHaveLength(2);
  });
});

describe("opening a template", () => {
  it("opens the host's workspace with the template's split tree and one terminal per pane", async () => {
    const opening = openLayoutTemplate(template({ layout: withPaneSnippet(withPaneSnippet(THREE_PANES, 0, undefined), 2, undefined) }));
    const result = await opening;
    expect(result.ok).toBe(true);
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    const workspace = hostWorkspace()!;
    expect(useUi.getState().activeWorkspaceId).toBe(workspace.id);
    expect(shape(workspaceLayoutTree(workspace)!)).toEqual({
      dir: "row", ratio: 0.6, a: "leaf", b: { dir: "col", ratio: 0.3, a: "leaf", b: "leaf" },
    });
    expect(paneRects(workspaceLayoutTree(workspace)!)).toHaveLength(3);
    expect(terminalTabs(workspace).map((tabs) => tabs.length)).toEqual([1, 1, 1]);
    expect(terminalTabs(workspace).flat().every((tab) => tab.sessionId === "s1")).toBe(true);
    // Without startup snippets nothing is confirmed and nothing is written.
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.terminalWrite).not.toHaveBeenCalled();
  });

  it("runs a pane's snippet in that pane only after confirmation and once its terminal is ready", async () => {
    const opening = openLayoutTemplate(template(), { readyTimeoutMs: 5_000 });
    await vi.waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    expect(mocks.terminalWrite).not.toHaveBeenCalled();
    attachTerminals();
    const result = await opening;
    expect(result.ok).toBe(true);
    const firstPane = hostWorkspace()!.panes[0];
    expect(mocks.terminalWrite).toHaveBeenCalledTimes(1);
    expect(mocks.terminalWrite.mock.calls[0][0]).toBe(`term-${firstPane.id}`);
    expect(new TextDecoder().decode(mocks.terminalWrite.mock.calls[0][1])).toBe("tail -f /var/log/app.log\r");
    // The third pane's snippet was deleted: its terminal opened without it, and the user is told.
    expect(terminalTabs(hostWorkspace())[2]).toHaveLength(1);
    expect(result.ok && result.notices.join(" ")).toContain("窗格 3");
    expect(toastTexts().some((text) => text.includes("窗格 3") && text.includes("已不存在"))).toBe(true);
  });

  it("does not run snippets the user declines", async () => {
    mocks.ask.mockResolvedValue(false);
    const result = await openLayoutTemplate(template());
    expect(result.ok).toBe(true);
    expect(mocks.terminalWrite).not.toHaveBeenCalled();
    expect(terminalTabs(hostWorkspace()).map((tabs) => tabs.length)).toEqual([1, 1, 1]);
  });

  it("warns about a production host before running snippets", async () => {
    mocks.assetGet.mockResolvedValue({ ...ASSET, tags: "prod" });
    mocks.ask.mockResolvedValue(false);
    await openLayoutTemplate(template());
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.ask.mock.calls[0];
    expect(message).toContain("生产环境");
    expect(options).toMatchObject({ kind: "warning" });
  });

  it("reports a deleted host and opens nothing", async () => {
    mocks.assetGet.mockRejectedValue(Object.assign(new Error("not found"), { code: "not_found" }));
    const result = await openLayoutTemplate(template());
    expect(result).toMatchObject({ ok: false, reason: "missing-asset" });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toEqual([]);
    expect(toastTexts().some((text) => text.includes("已不存在"))).toBe(true);
  });

  it("leaves a host workspace that already has tabs alone", async () => {
    useUi.setState({
      sessions: [SESSION],
      workspaces: [{
        id: "ws-open", kind: "session", title: "web-01", sessionId: "s1", assetId: "asset-1", closable: true,
        panes: [{ id: "p", tabs: [{ id: "t", kind: "terminal", title: "终端 1", sessionId: "s1", closable: true }], activeTabId: "t" }],
        activePaneId: "p",
      }],
    });
    const result = await openLayoutTemplate(template());
    expect(result).toMatchObject({ ok: false, reason: "occupied" });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-open");
  });

  describe("startup snippets", () => {
    const ONE_PANE = (snippetId: string): LayoutTemplate => template({ layout: { type: "leaf", snippetId } });

    it("skips a snippet that takes parameters and says so", async () => {
      mocks.snippetList.mockResolvedValue([{ ...SNIPPET, body: "tail -n {{lines}} /var/log/app.log" }]);
      const result = await openLayoutTemplate(ONE_PANE("snip-1"));
      expect(result.ok && result.notices.join(" ")).toContain("带参数");
      expect(mocks.ask).not.toHaveBeenCalled();
      expect(mocks.terminalWrite).not.toHaveBeenCalled();
    });

    it("reports a missing snippet and opens an empty terminal", async () => {
      const result = await openLayoutTemplate(ONE_PANE("snip-gone"));
      expect(result.ok && result.notices.join(" ")).toContain("已不存在");
      expect(terminalTabs(hostWorkspace()).flat()).toHaveLength(1);
      expect(mocks.terminalWrite).not.toHaveBeenCalled();
    });

    it("runs every step of a multi-step snippet", async () => {
      mocks.snippetList.mockResolvedValue([{ ...SNIPPET, steps: [{ command: "cd /srv" }, { command: "ls" }] }]);
      const opening = openLayoutTemplate(ONE_PANE("snip-1"), { readyTimeoutMs: 5_000 });
      await vi.waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
      attachTerminals();
      expect((await opening).ok).toBe(true);
      const written = mocks.terminalWrite.mock.calls.map((call) => new TextDecoder().decode(call[1])).join("");
      expect(written).toContain("cd /srv");
      expect(written).toContain("ls");
    });

    it("gives up after the readiness timeout without running the snippet", async () => {
      vi.useFakeTimers();
      try {
        const opening = openLayoutTemplate(ONE_PANE("snip-1"));
        await vi.waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
        await vi.advanceTimersByTimeAsync(LAYOUT_TERMINAL_READY_TIMEOUT_MS + 1);
        const result = await opening;
        expect(result.ok && result.notices.join(" ")).toContain("未就绪");
        expect(mocks.terminalWrite).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });

    it("runs in the exact tab it opened even when tabs move or other terminals attach first", async () => {
      const opening = openLayoutTemplate(ONE_PANE("snip-1"), { readyTimeoutMs: 5_000 });
      await vi.waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
      const workspace = hostWorkspace()!;
      const opened = workspace.panes[0].tabs[0];
      // The user opens another terminal in the pane and moves it ahead of the template's tab.
      useUi.setState((state) => ({
        workspaces: state.workspaces.map((candidate) => candidate.id !== workspace.id ? candidate : {
          ...candidate,
          panes: candidate.panes.map((pane) => ({
            ...pane,
            tabs: [{ id: "other", kind: "terminal" as const, title: "其他", sessionId: "s1", closable: true, tabId: "term-other" }, ...pane.tabs],
          })),
        }),
      }));
      useUi.getState().updateTab(opened.id, { tabId: "term-opened" });
      expect((await opening).ok).toBe(true);
      expect(mocks.terminalWrite).toHaveBeenCalledTimes(1);
      expect(mocks.terminalWrite.mock.calls[0][0]).toBe("term-opened");
    });

    it("skips the run when its tab was closed during the wait", async () => {
      const opening = openLayoutTemplate(ONE_PANE("snip-1"), { readyTimeoutMs: 5_000 });
      await vi.waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
      const workspace = hostWorkspace()!;
      useUi.setState((state) => ({
        workspaces: state.workspaces.map((candidate) => candidate.id !== workspace.id ? candidate : {
          ...candidate,
          panes: candidate.panes.map((pane) => ({
            ...pane,
            tabs: [{ id: "other", kind: "terminal" as const, title: "其他", sessionId: "s1", closable: true, tabId: "term-other" }],
          })),
        }),
      }));
      const result = await opening;
      expect(result.ok && result.notices.join(" ")).toContain("未就绪");
      expect(mocks.terminalWrite).not.toHaveBeenCalled();
    });
  });

  it("refuses a second open of the same host while the first is still running", async () => {
    const first = openLayoutTemplate(template(), { readyTimeoutMs: 5_000 });
    const second = await openLayoutTemplate(template({ id: "lt-2" }));
    expect(second).toMatchObject({ ok: false, reason: "occupied" });
    mocks.ask.mockResolvedValue(false);
    await vi.waitFor(() => expect(mocks.ask).toHaveBeenCalled());
    expect((await first).ok).toBe(true);
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    // Once finished, the host is free for the occupied check to take over rather than the in-flight guard.
    mocks.ask.mockClear();
    const third = await openLayoutTemplate(template());
    expect(third).toMatchObject({ ok: false, reason: "occupied" });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
  });
});
