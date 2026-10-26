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
import { SplitStack } from "../../app/workspace/SplitStack";
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
    splitRatio: 0.5,
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
      expect(view!.container.textContent).toContain("「prod」尚未连接");
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
      expect(view!.container.textContent).toContain("原终端进程已结束");
      remount(tab({ kind: "files", sessionId: "s1", restorePending: true }));
      expect(view!.container.textContent).toContain("连接后重新打开此标签");
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
      expect(view!.container.textContent).toContain("容器终端不会自动重新执行");
      click(button("关闭标签"));
      expect(onClose).toHaveBeenCalledWith("ct");
    });
  });
});

describe("SplitStack", () => {
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

  function renderStack(props: { ratio?: number; bottom?: boolean } = {}) {
    view = mount(
      createElement(SplitStack, {
        ratio: props.ratio ?? 0.5,
        onRatio,
        top: createElement("div", { id: "top" }, "top"),
        bottom: props.bottom === false ? undefined : createElement("div", { id: "bottom" }, "bottom"),
      }),
    );
    const sep = view.container.querySelector<HTMLElement>('[role="separator"]');
    if (sep) {
      // The container spans y=100..500 so clientY maps linearly onto the ratio.
      sep.parentElement!.getBoundingClientRect = () =>
        ({ top: 100, height: 400, left: 0, width: 300, right: 300, bottom: 500, x: 0, y: 100, toJSON: () => ({}) }) as DOMRect;
      sep.setPointerCapture = vi.fn();
      sep.releasePointerCapture = vi.fn();
      sep.hasPointerCapture = () => false;
    }
    return sep;
  }

  function pointer(el: Element, type: string, init: { clientY?: number; button?: number } = {}) {
    const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: init.button ?? 0, clientY: init.clientY ?? 0 });
    Object.assign(event, { pointerId: 1 });
    act(() => {
      el.dispatchEvent(event);
    });
  }

  it("renders only the top pane and no divider without a bottom pane", () => {
    const sep = renderStack({ bottom: false });
    expect(sep).toBeNull();
    expect(view!.container.querySelector("#top")).not.toBeNull();
    expect(view!.container.querySelector("#bottom")).toBeNull();
  });

  it("renders both panes with the ratio exposed on the divider", () => {
    const sep = renderStack({ ratio: 0.3 })!;
    expect(view!.container.querySelector("#top")).not.toBeNull();
    expect(view!.container.querySelector("#bottom")).not.toBeNull();
    expect(sep.getAttribute("aria-valuenow")).toBe("30");
    expect(sep.getAttribute("aria-valuetext")).toContain("30%");
  });

  it("clamps an out-of-range ratio for display", () => {
    expect(renderStack({ ratio: 0.99 })!.getAttribute("aria-valuenow")).toBe("85");
    view!.unmount();
    expect(renderStack({ ratio: -3 })!.getAttribute("aria-valuenow")).toBe("15");
  });

  it("dragging the divider reports the ratio under the pointer, clamped, and signals resize end", () => {
    const sep = renderStack()!;
    const resizeEnd = vi.fn();
    window.addEventListener("nexterm:resize-end", resizeEnd);
    pointer(sep, "pointerdown");
    expect(sep.className).toContain("is-dragging");
    pointer(sep, "pointermove", { clientY: 200 });
    expect(onRatio).toHaveBeenLastCalledWith(0.25);
    pointer(sep, "pointerup", { clientY: 1000 });
    expect(onRatio).toHaveBeenLastCalledWith(0.85);
    expect(sep.className).not.toContain("is-dragging");
    expect(resizeEnd).toHaveBeenCalledTimes(1);
    window.removeEventListener("nexterm:resize-end", resizeEnd);
  });

  it("ignores pointer movement when no drag started or for non-primary buttons", () => {
    const sep = renderStack()!;
    pointer(sep, "pointermove", { clientY: 200 });
    pointer(sep, "pointerdown", { button: 2 });
    pointer(sep, "pointermove", { clientY: 200 });
    expect(onRatio).not.toHaveBeenCalled();
  });

  it("stops dragging when the pointer is cancelled", () => {
    const sep = renderStack()!;
    pointer(sep, "pointerdown");
    pointer(sep, "pointercancel");
    expect(sep.className).not.toContain("is-dragging");
    pointer(sep, "pointermove", { clientY: 400 });
    expect(onRatio).not.toHaveBeenCalled();
  });

  it("resets to an even split on double click", () => {
    const sep = renderStack({ ratio: 0.2 })!;
    act(() => {
      sep.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    expect(onRatio).toHaveBeenCalledWith(0.5);
  });

  it("adjusts the ratio from the keyboard and ignores other keys", () => {
    const sep = renderStack({ ratio: 0.5 })!;
    const key = (k: string, shiftKey = false) =>
      act(() => {
        sep.dispatchEvent(new KeyboardEvent("keydown", { key: k, shiftKey, bubbles: true, cancelable: true }));
      });
    key("ArrowDown");
    expect(onRatio).toHaveBeenLastCalledWith(0.52);
    key("ArrowUp", true);
    expect(onRatio).toHaveBeenLastCalledWith(0.4);
    key("Home");
    expect(onRatio).toHaveBeenLastCalledWith(0.15);
    key("End");
    expect(onRatio).toHaveBeenLastCalledWith(0.85);
    key("Enter");
    expect(onRatio).toHaveBeenLastCalledWith(0.5);
    onRatio.mockClear();
    key("a");
    expect(onRatio).not.toHaveBeenCalled();
  });
});
