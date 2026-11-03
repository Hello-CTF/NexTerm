import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ write: vi.fn(), ask: vi.fn(), toast: vi.fn() }));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});
vi.mock("../../ipc/commands", () => ({ terminalApi: { write: mocks.write } }));

import { executeSnippet, insertSnippet, type Snippet } from "../../features/explorer/snippetInsert";
import { useUi, type AppTab } from "../../app/store";

function seedTerminal(tabId = "term-1", extra: Partial<AppTab> = {}) {
  const tab: AppTab = { id: "tab-1", kind: "terminal", title: "T", tabId, closable: true, ...extra };
  useUi.setState({
    workspaces: [{
      id: "ws-1", kind: "session", title: "W",
      panes: [{ id: "pane-1", tabs: [tab], activeTabId: tab.id }],
      activePaneId: "pane-1", closable: true,
    }],
    activeWorkspaceId: "ws-1",
  });
}

function multi(steps: Array<{ name?: string; command: string; confirm?: boolean }>): Snippet {
  return {
    id: "m1", name: "deploy", groupId: null, sort: 1,
    body: steps.map((s) => s.command).join("\n"),
    steps: steps.map((s, i) => ({ id: `s${i}`, name: s.name ?? "", command: s.command, confirm: s.confirm })),
  };
}

function written(): string[] {
  return mocks.write.mock.calls.map(([, data]) => new TextDecoder().decode(data as Uint8Array));
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.write.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, toasts: [], pushToast: mocks.toast });
  seedTerminal();
});

describe("multi-step snippet execution in the current terminal", () => {
  it("writes each step in order and presses enter after every one", async () => {
    expect(await executeSnippet(multi([{ command: "cd /srv" }, { command: "git pull" }, { command: "make" }]))).toBe(true);
    expect(written()).toEqual(["cd /srv\r", "git pull\r", "make\r"]);
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("asks before a confirm step and stops after a refusal", async () => {
    mocks.ask.mockResolvedValue(false);
    const result = await executeSnippet(multi([
      { command: "ls" },
      { name: "restart", command: "systemctl restart app", confirm: true },
      { command: "echo done" },
    ]));
    expect(result).toBe(false);
    expect(written()).toEqual(["ls\r"]);
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const message = String(mocks.ask.mock.calls[0][0]);
    expect(message).toContain("restart");
    expect(message).toContain("systemctl restart app");
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("1"));
  });

  it("runs a confirmed step and continues", async () => {
    await executeSnippet(multi([{ command: "a", confirm: true }, { command: "b" }]));
    expect(written()).toEqual(["a\r", "b\r"]);
  });

  it("stops when the terminal disappears between steps", async () => {
    mocks.ask.mockImplementation(async () => {
      useUi.setState({ workspaces: [] });
      return true;
    });
    const result = await executeSnippet(multi([{ command: "a" }, { command: "b", confirm: true }, { command: "c" }]));
    expect(result).toBe(false);
    expect(written()).toEqual(["a\r"]);
  });

  it("stops when the account changes between steps", async () => {
    mocks.ask.mockImplementation(async () => {
      useUi.setState({ accountEpoch: useUi.getState().accountEpoch + 1 });
      return true;
    });
    const result = await executeSnippet(multi([{ command: "a", confirm: true }, { command: "b" }]));
    expect(result).toBe(false);
    expect(written()).toEqual([]);
  });

  it("refuses to insert a multi-step snippet", async () => {
    expect(await insertSnippet(multi([{ command: "a" }, { command: "b" }]))).toBe(false);
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("keeps single-step behaviour unchanged", async () => {
    const single: Snippet = { id: "s", name: "one", body: "df -h", groupId: null, sort: 0, steps: [] };
    expect(await executeSnippet(single)).toBe(true);
    expect(await insertSnippet(single)).toBe(true);
    expect(written()).toEqual(["df -h\r", "df -h"]);
  });

  function seedTwo() {
    const mk = (id: string, tabId: string, extra: Partial<AppTab> = {}): AppTab =>
      ({ id, kind: "terminal", title: id, tabId, closable: true, ...extra });
    useUi.setState({
      workspaces: [{
        id: "ws-1", kind: "session", title: "W",
        panes: [{ id: "pane-1", tabs: [mk("tab-1", "term-1"), mk("tab-2", "term-2")], activeTabId: "tab-1" }],
        activePaneId: "pane-1", closable: true,
      }],
      activeWorkspaceId: "ws-1",
    });
  }
  const setTab = (id: string, patch: Partial<AppTab>) => {
    const ws = useUi.getState().workspaces;
    useUi.setState({
      workspaces: ws.map((w) => ({
        ...w,
        panes: w.panes.map((p) => ({ ...p, tabs: p.tabs.map((t) => (t.id === id ? { ...t, ...patch } : t)) })),
      })),
    });
  };

  it("keeps writing to the original terminal after the active tab changes during a confirmation", async () => {
    seedTwo();
    mocks.ask.mockImplementation(async () => {
      const ws = useUi.getState().workspaces;
      useUi.setState({
        workspaces: ws.map((w) => ({ ...w, panes: w.panes.map((p) => ({ ...p, activeTabId: "tab-2" })) })),
      });
      return true;
    });
    expect(await executeSnippet(multi([{ command: "a" }, { command: "b", confirm: true }, { command: "c" }]))).toBe(true);
    expect(mocks.write.mock.calls.map(([id]) => id)).toEqual(["term-1", "term-1", "term-1"]);
  });

  it("stops when the original terminal ends even though another terminal is active", async () => {
    seedTwo();
    mocks.ask.mockImplementation(async () => {
      setTab("tab-1", { exited: true });
      const ws = useUi.getState().workspaces;
      useUi.setState({
        workspaces: ws.map((w) => ({ ...w, panes: w.panes.map((p) => ({ ...p, activeTabId: "tab-2" })) })),
      });
      return true;
    });
    expect(await executeSnippet(multi([{ command: "a" }, { command: "b", confirm: true }, { command: "c" }]))).toBe(false);
    expect(written()).toEqual(["a\r"]);
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("1"));
  });
});
