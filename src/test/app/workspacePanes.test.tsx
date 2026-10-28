/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, type MountedView } from "../features/reactTestUtils";
import type { AppTab, Workspace } from "../../app/store";

const mocks = vi.hoisted(() => ({
  reconnectTab: vi.fn(),
  reconnectWorkspace: vi.fn(),
}));

vi.mock("../../app/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/store")>();
  return { ...actual, reconnectRestoredTab: mocks.reconnectTab, reconnectRestoredWorkspace: mocks.reconnectWorkspace };
});

vi.mock("../../app/workspace/EmptyStates", () => ({
  EmptyState: () => createElement("div", { "data-panel": "empty" }, "empty"),
}));

vi.mock("../../features/fleet/DevicesView", () => ({
  DevicesView: () => createElement("div", { "data-panel": "devices" }),
}));

// Each lazy panel becomes a marker element exposing the props PaneForTab hands it as data attributes.
vi.mock("../../app/lazyPanels", async () => {
  const stub = (name: string, props: string[] = []) => (p: Record<string, unknown>) =>
    createElement(
      "div",
      {
        "data-panel": name,
        ...Object.fromEntries(props.map((key) => [`data-${key.toLowerCase()}`, String(p[key])])),
      },
      typeof p.onClose === "function"
        ? createElement("button", { type: "button", onClick: p.onClose as () => void }, `close-${name}`)
        : null,
    );
  return {
    AuditView: stub("audit"),
    BackgroundSessions: stub("background", ["visible", "workspaceId", "sessionId"]),
    CredentialsPanel: stub("credentials", ["credId"]),
    DbPanel: stub("db", ["connId", "kind", "workspaceId"]),
    DockerPanel: stub("docker", ["sessionId", "visible"]),
    ExecBatchPanel: stub("execBatch"),
    FileBrowser: stub("browser", ["sessionId"]),
    FileEditor: stub("editor", ["sessionId", "path"]),
    FleetHealthPanel: stub("fleetHealth", ["visible"]),
    ForwardPanel: stub("forward", ["sessionId"]),
    LogViewer: stub("logViewer", ["sessionId", "path"]),
    MountPanel: stub("mount", ["sessionId"]),
    MultiHostLogTail: stub("multiLog"),
    SettingsView: stub("settings", ["active"]),
    TerminalPane: stub("terminal", ["sessionId", "title", "storeTabId", "resumeTabId", "visible", "containerId"]),
    TranscriptHistoryPanel: stub("history", ["visible", "workspaceId", "assetId"]),
  };
});

import { useUi } from "../../app/store";
import { PaneForTab } from "../../app/workspace/PaneForTab";
import { SplitView, paneStyle, splitHandles } from "../../app/workspace/SplitView";
import type { SplitNode } from "../../app/splitTree";
import { MULTI_HOST_LOG_TAB_ID } from "../../features/logs/logTailTab";
import { FLEET_HEALTH_TAB_ID } from "../../features/fleet/fleetHealthTab";

let view: MountedView | null = null;

function workspace(patch: Partial<Workspace> = {}): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "prod",
    assetKind: "ssh",
    panes: [{ id: "p1", tabs: [], activeTabId: "" }],
    activePaneId: "p1",
    closable: true,
    ...patch,
  };
}

function tab(patch: Partial<AppTab> & Pick<AppTab, "kind">): AppTab {
  return { id: `${patch.kind}-tab`, title: patch.kind, closable: true, ...patch } as AppTab;
}

function render(t: AppTab, opts: { active?: boolean; onClose?: (id: string) => Promise<void> } = {}) {
  view = mount(
    createElement(PaneForTab, {
      tab: t,
      active: opts.active ?? true,
      workspaceId: "ws",
      onClose: opts.onClose ?? (() => Promise.resolve()),
    }),
  );
  return view;
}

function remount(t: AppTab, opts: { active?: boolean; onClose?: (id: string) => Promise<void> } = {}) {
  view!.unmount();
  return render(t, opts);
}

function isEmpty(): boolean {
  return view!.container.querySelector('[data-panel="empty"]') !== null;
}

function button(text: string): HTMLButtonElement {
  return [...view!.container.querySelectorAll("button")].find((b) => b.textContent === text)!;
}

async function panel(name: string): Promise<HTMLElement> {
  await flushUntil(() => view!.container.querySelector(`[data-panel="${name}"]`) !== null);
  return view!.container.querySelector<HTMLElement>(`[data-panel="${name}"]`)!;
}

beforeEach(() => {
  mocks.reconnectTab.mockReset().mockResolvedValue({ ok: true });
  mocks.reconnectWorkspace.mockReset().mockResolvedValue({ ok: true });
  useUi.setState({ workspaces: [workspace()], activeWorkspaceId: "ws", sessions: [], toasts: [], workspaceRuntimeEpoch: 0 });
});

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("PaneForTab", () => {
  it("renders the terminal for a tab with a session and forwards visibility", async () => {
    render(tab({ kind: "terminal", id: "t1", title: "shell", sessionId: "s1", tabId: "k1", containerId: "c1" }), { active: false });
    const el = await panel("terminal");
    expect(el.dataset.sessionid).toBe("s1");
    expect(el.dataset.title).toBe("shell");
    expect(el.dataset.storetabid).toBe("t1");
    expect(el.dataset.resumetabid).toBe("k1");
    expect(el.dataset.containerid).toBe("c1");
    expect(el.dataset.visible).toBe("false");
  });

  it("shows the empty state for a terminal without a session", () => {
    render(tab({ kind: "terminal" }));
    expect(isEmpty()).toBe(true);
  });

  it("passes the session to mount and forward panels", async () => {
    render(tab({ kind: "mount", sessionId: "s1" }));
    expect((await panel("mount")).dataset.sessionid).toBe("s1");
    remount(tab({ kind: "forward", sessionId: "s2" }));
    expect((await panel("forward")).dataset.sessionid).toBe("s2");
  });

  it("opens the file browser without a path and the editor with one, which closes its tab", async () => {
    render(tab({ kind: "files", sessionId: "s1" }));
    expect((await panel("browser")).dataset.sessionid).toBe("s1");

    const onClose = vi.fn().mockResolvedValue(undefined);
    remount(tab({ kind: "files", id: "f1", sessionId: "s1", path: "/etc/hosts" }), { onClose });
    expect((await panel("editor")).dataset.path).toBe("/etc/hosts");
    click(button("close-editor"));
    expect(onClose).toHaveBeenCalledWith("f1");
  });

  it("shows the empty state for files without a session", () => {
    render(tab({ kind: "files" }));
    expect(isEmpty()).toBe(true);
  });

  it("renders the log viewer only when both session and path are known", async () => {
    const onClose = vi.fn().mockResolvedValue(undefined);
    render(tab({ kind: "log", id: "l1", sessionId: "s1", path: "/var/log/syslog" }), { onClose });
    expect((await panel("logViewer")).dataset.path).toBe("/var/log/syslog");
    click(button("close-logViewer"));
    expect(onClose).toHaveBeenCalledWith("l1");
    remount(tab({ kind: "log", id: "l2", sessionId: "s1" }));
    expect(isEmpty()).toBe(true);
  });

  it("routes multi-host log and fleet health tabs to their aggregate panels", async () => {
    render(tab({ kind: "log", id: MULTI_HOST_LOG_TAB_ID }));
    await panel("multiLog");
    remount(tab({ kind: "log", id: `${FLEET_HEALTH_TAB_ID}:1` }), { active: false });
    expect((await panel("fleetHealth")).dataset.visible).toBe("false");
  });

  it("renders docker with visibility, and empty without a session", async () => {
    render(tab({ kind: "docker", sessionId: "s1" }), { active: false });
    const el = await panel("docker");
    expect(el.dataset.sessionid).toBe("s1");
    expect(el.dataset.visible).toBe("false");
    remount(tab({ kind: "docker" }));
    expect(isEmpty()).toBe(true);
  });

  it("renders the database panel with its kind and defaults to mysql", async () => {
    render(tab({ kind: "db", connId: "c1", dbKind: "postgres" }));
    let el = await panel("db");
    expect(el.dataset.connid).toBe("c1");
    expect(el.dataset.kind).toBe("postgres");
    expect(el.dataset.workspaceid).toBe("ws");
    remount(tab({ kind: "db", connId: "c2" }));
    el = await panel("db");
    expect(el.dataset.kind).toBe("mysql");
  });

  it("remounts the database panel when the workspace runtime epoch changes", async () => {
    render(tab({ kind: "db", connId: "c1" }));
    const before = await panel("db");
    act(() => useUi.setState({ workspaceRuntimeEpoch: 1 }));
    const after = view!.container.querySelector('[data-panel="db"]');
    expect(after).not.toBeNull();
    expect(after).not.toBe(before);
  });

  it("shows the empty state for a database tab without a connection", () => {
    render(tab({ kind: "db" }));
    expect(isEmpty()).toBe(true);
  });

  it.each([
    ["execBatch", "execBatch"],
    ["audit", "audit"],
    ["devices", "devices"],
  ] as const)("renders the %s tab", async (kind, name) => {
    render(tab({ kind }));
    await panel(name);
  });

  it("passes the active flag to settings", async () => {
    render(tab({ kind: "settings" }), { active: false });
    expect((await panel("settings")).dataset.active).toBe("false");
  });

  it("passes workspace context to background sessions and history", async () => {
    render(tab({ kind: "background", sessionId: "s1" }));
    let el = await panel("background");
    expect(el.dataset.workspaceid).toBe("ws");
    expect(el.dataset.sessionid).toBe("s1");
    remount(tab({ kind: "history", assetId: "a1" }), { active: false });
    el = await panel("history");
    expect(el.dataset.assetid).toBe("a1");
    expect(el.dataset.visible).toBe("false");
  });

  it("renders the credentials panel", async () => {
    render(tab({ kind: "credentials", credId: "cred1" }));
    expect((await panel("credentials")).dataset.credid).toBe("cred1");
  });

  it("falls back to the empty state for an unknown tab kind", () => {
    render({ id: "x", kind: "mystery", title: "x", closable: true } as unknown as AppTab);
    expect(isEmpty()).toBe(true);
  });

  describe("restored tabs", () => {
    it("asks to connect a restored workspace instead of mounting panels", async () => {
      useUi.setState({ workspaces: [workspace({ restorePending: true })] });
      render(tab({ kind: "terminal", sessionId: "s1" }));
      expect(view!.container.querySelector('[data-panel="terminal"]')).toBeNull();
      expect(view!.container.textContent).toContain("「prod」还没有连接");
      expect(view!.container.textContent).toContain("后台会话");
      click(button("连接"));
      await flushUntil(() => mocks.reconnectWorkspace.mock.calls.length === 1);
      await flush();
      expect(mocks.reconnectWorkspace).toHaveBeenCalledWith("ws");
    });

    it("reports a failed workspace reconnect as a toast", async () => {
      mocks.reconnectWorkspace.mockResolvedValue({ ok: false, error: "refused" });
      useUi.setState({ workspaces: [workspace({ restorePending: true, assetKind: "local" })] });
      render(tab({ kind: "terminal" }));
      expect(view!.container.textContent).not.toContain("后台会话");
      click(button("连接"));
      await flushUntil(() => useUi.getState().toasts.some((t) => t.text === "refused"));
      await flush();
    });

    it("explains that a workspace whose asset was deleted cannot reconnect", () => {
      useUi.setState({ workspaces: [workspace({ restorePending: true, deletedTarget: true })] });
      render(tab({ kind: "terminal" }));
      expect(view!.container.textContent).toContain("资产已删除");
      expect(view!.container.querySelector("button")).toBeNull();
    });

    it("offers to reopen a restored terminal tab and surfaces failures", async () => {
      mocks.reconnectTab.mockResolvedValue({ ok: false, error: "no route" });
      render(tab({ kind: "terminal", id: "t9", restorePending: true }));
      expect(view!.container.textContent).toContain("持久终端");
      click(button("新开终端"));
      await flushUntil(() => useUi.getState().toasts.some((t) => t.text === "no route"));
      expect(mocks.reconnectTab).toHaveBeenCalledWith("t9");
    });

    it("uses the plain message for restored non-durable terminals and file tabs", () => {
      useUi.setState({ workspaces: [workspace({ assetKind: "local" })] });
      render(tab({ kind: "terminal", restorePending: true }));
      expect(view!.container.textContent).toContain("原终端已经结束");
      remount(tab({ kind: "files", sessionId: "s1", restorePending: true }));
      expect(view!.container.textContent).toContain("连接主机后才能打开这个标签");
      expect(button("连接并打开")).toBeDefined();
    });

    it("opens the background sessions tab after reconnecting a durable host", async () => {
      useUi.setState({ workspaces: [workspace({ sessionId: "live" })] });
      render(tab({ kind: "terminal", restorePending: true }));
      click(button("查看后台会话"));
      await flushUntil(() => mocks.reconnectWorkspace.mock.calls.length === 1);
      await flushUntil(() =>
        useUi.getState().workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.id === "background-ws"))),
      );
    });

    it("tells container terminals to be reopened from the container list and closes the tab", () => {
      const onClose = vi.fn().mockResolvedValue(undefined);
      render(tab({ kind: "terminal", id: "ct", containerId: "c1", restorePending: true }), { onClose });
      expect(view!.container.textContent).toContain("容器终端不会自动重新打开");
      click(button("关闭标签"));
      expect(onClose).toHaveBeenCalledWith("ct");
    });
  });
});

describe("SplitView", () => {
  const onRatio = vi.fn();
  beforeEach(() => {
    onRatio.mockReset();
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      cb(0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
  });
  afterEach(() => vi.unstubAllGlobals());

  const leaf = (paneId: string): SplitNode => ({ type: "leaf", id: `leaf-${paneId}`, paneId });
  // [ left | [ top / bottom ] ]: the root splits left/right, its right half top/bottom.
  const nested = (rootRatio = 0.5, rightRatio = 0.5): SplitNode => ({
    type: "split",
    id: "root",
    dir: "row",
    ratio: rootRatio,
    a: leaf("left"),
    b: { type: "split", id: "right", dir: "col", ratio: rightRatio, a: leaf("top"), b: leaf("bottom") },
  });

  function renderView(tree: SplitNode | undefined, singlePane = false, activePaneId = "left") {
    const ids = tree ? ["left", "top", "bottom"] : ["left"];
    view = mount(
      createElement(SplitView, {
        workspace: workspace({
          panes: ids.map((id) => ({ id, tabs: [], activeTabId: null })),
          activePaneId,
          layoutTree: tree,
        }),
        singlePane,
        onRatio,
        renderPane: (pane) => createElement("div", { id: `pane-${pane.id}` }, pane.id),
        renderTab: (_pane, tab) => createElement("div", { id: `tab-${tab.id}` }, tab.id),
        onActivatePane: vi.fn(),
      }),
    );
    const box = view.container.querySelector<HTMLElement>(".nx-split-view")!;
    // The workspace spans x=0..400, y=100..500 so pointer positions map linearly onto ratios.
    box.getBoundingClientRect = () =>
      ({ top: 100, height: 400, left: 0, width: 400, right: 400, bottom: 500, x: 0, y: 100, toJSON: () => ({}) }) as DOMRect;
    for (const sep of view.container.querySelectorAll<HTMLElement>('[role="separator"]')) {
      sep.setPointerCapture = vi.fn();
      sep.releasePointerCapture = vi.fn();
      sep.hasPointerCapture = () => false;
    }
    return view;
  }

  function separator(id: string): HTMLElement {
    return view!.container.querySelector<HTMLElement>(`[data-split-id="${id}"]`)!;
  }

  function pointer(el: Element, type: string, init: { clientX?: number; clientY?: number; button?: number } = {}) {
    const event = new MouseEvent(type, {
      bubbles: true,
      cancelable: true,
      button: init.button ?? 0,
      clientX: init.clientX ?? 0,
      clientY: init.clientY ?? 0,
    });
    Object.assign(event, { pointerId: 1 });
    act(() => {
      el.dispatchEvent(event);
    });
  }

  it("lists every split with the area it divides", () => {
    expect(splitHandles(nested(0.3, 0.6))).toEqual([
      { id: "root", dir: "row", ratio: 0.3, rect: { x: 0, y: 0, width: 1, height: 1 } },
      { id: "right", dir: "col", ratio: 0.6, rect: { x: 0.3, y: 0, width: 0.7, height: 1 } },
    ]);
    expect(splitHandles(leaf("solo"))).toEqual([]);
  });

  it("insets panes by half a divider only on inner edges", () => {
    expect(paneStyle({ x: 0, y: 0, width: 1, height: 1 })).toEqual({
      left: "calc(0% + 0px)",
      top: "calc(0% + 0px)",
      width: "calc(100% - 0px)",
      height: "calc(100% - 0px)",
    });
    expect(paneStyle({ x: 0.5, y: 0, width: 0.5, height: 0.5 })).toEqual({
      left: "calc(50% + 3px)",
      top: "calc(0% + 0px)",
      width: "calc(50% - 3px)",
      height: "calc(50% - 3px)",
    });
  });

  it("renders one pane without dividers when the workspace never split", () => {
    renderView(undefined);
    expect(view!.container.querySelector("#pane-left")).not.toBeNull();
    expect(view!.container.querySelector('[role="separator"]')).toBeNull();
  });

  it("renders a nested layout with one oriented divider per split", () => {
    renderView(nested(0.3, 0.6));
    for (const id of ["left", "top", "bottom"]) expect(view!.container.querySelector(`#pane-${id}`)).not.toBeNull();
    const root = separator("root");
    expect(root.getAttribute("aria-orientation")).toBe("vertical");
    expect(root.getAttribute("aria-valuenow")).toBe("30");
    expect(root.getAttribute("aria-valuetext")).toBe("左栏 30%");
    expect(root.className).toContain("is-row");
    const right = separator("right");
    expect(right.getAttribute("aria-orientation")).toBe("horizontal");
    expect(right.getAttribute("aria-valuetext")).toBe("上栏 60%");
    expect(right.className).toContain("is-col");
  });

  it("clamps an out-of-range ratio for display", () => {
    renderView(nested(0.99, -3));
    expect(separator("root").getAttribute("aria-valuenow")).toBe("85");
    expect(separator("right").getAttribute("aria-valuenow")).toBe("15");
  });

  it("drags a left/right divider along x and a nested top/bottom divider within its own area", () => {
    renderView(nested(0.5, 0.5));
    const resizeEnd = vi.fn();
    window.addEventListener("nexterm:resize-end", resizeEnd);
    const root = separator("root");
    pointer(root, "pointerdown");
    expect(root.className).toContain("is-dragging");
    pointer(root, "pointermove", { clientX: 100, clientY: 400 });
    expect(onRatio).toHaveBeenLastCalledWith("root", 0.25);
    pointer(root, "pointerup", { clientX: 1000 });
    expect(onRatio).toHaveBeenLastCalledWith("root", 0.85);
    expect(root.className).not.toContain("is-dragging");
    expect(resizeEnd).toHaveBeenCalledTimes(1);

    // The right half spans y=100..500; y=200 is a quarter of it.
    const right = separator("right");
    pointer(right, "pointerdown");
    pointer(right, "pointermove", { clientX: 10, clientY: 200 });
    expect(onRatio).toHaveBeenLastCalledWith("right", 0.25);
    pointer(right, "pointercancel");
    expect(right.className).not.toContain("is-dragging");
    window.removeEventListener("nexterm:resize-end", resizeEnd);
  });

  it("ignores pointer movement without a primary-button drag", () => {
    renderView(nested());
    const root = separator("root");
    pointer(root, "pointermove", { clientX: 100 });
    pointer(root, "pointerdown", { button: 2 });
    pointer(root, "pointermove", { clientX: 100 });
    expect(onRatio).not.toHaveBeenCalled();
  });

  it("resets to an even split on double click", () => {
    renderView(nested(0.2));
    act(() => {
      separator("root").dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    expect(onRatio).toHaveBeenCalledWith("root", 0.5);
  });

  it("adjusts each divider with the arrow keys of its own axis", () => {
    renderView(nested(0.5, 0.5));
    const key = (el: HTMLElement, k: string, shiftKey = false) =>
      act(() => {
        el.dispatchEvent(new KeyboardEvent("keydown", { key: k, shiftKey, bubbles: true, cancelable: true }));
      });
    const root = separator("root");
    key(root, "ArrowRight");
    expect(onRatio).toHaveBeenLastCalledWith("root", 0.52);
    key(root, "ArrowLeft", true);
    expect(onRatio).toHaveBeenLastCalledWith("root", 0.4);
    onRatio.mockClear();
    key(root, "ArrowDown");
    expect(onRatio).not.toHaveBeenCalled();
    const right = separator("right");
    key(right, "ArrowDown");
    expect(onRatio).toHaveBeenLastCalledWith("right", 0.52);
    key(right, "Home");
    expect(onRatio).toHaveBeenLastCalledWith("right", 0.15);
    key(right, "End");
    expect(onRatio).toHaveBeenLastCalledWith("right", 0.85);
    key(right, "Enter");
    expect(onRatio).toHaveBeenLastCalledWith("right", 0.5);
  });

  it("shows only the active pane, full size and without dividers, in single-pane mode", () => {
    renderView(nested(), true, "top");
    expect(view!.container.querySelector('[role="separator"]')).toBeNull();
    const shown = view!.container.querySelector<HTMLElement>('[data-pane-id="top"]')!;
    expect(shown.className).not.toContain("hidden");
    expect(shown.style.inset).toBe("0px");
    for (const id of ["left", "bottom"]) {
      const hidden = view!.container.querySelector<HTMLElement>(`[data-pane-id="${id}"]`)!;
      expect(hidden.className).toBe("hidden");
      // Hidden panes stay mounted so their terminals keep running.
      expect(hidden.querySelector(`#pane-${id}`)).not.toBeNull();
    }
  });
});
