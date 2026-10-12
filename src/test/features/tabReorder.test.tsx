/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: mocks.assetSearch },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import { useUi } from "../../app/store";

const TAB_MIME = "application/x-nexterm-tab";

interface StubDataTransfer {
  types: string[];
  effectAllowed: string;
  dropEffect: string;
  setData: (type: string, value: string) => void;
  getData: (type: string) => string;
}

function makeDataTransfer(): StubDataTransfer {
  const data = new Map<string, string>();
  return {
    get types() {
      return [...data.keys()];
    },
    effectAllowed: "",
    dropEffect: "",
    setData: (type, value) => {
      data.set(type, value);
    },
    getData: (type) => data.get(type) ?? "",
  };
}

function dragEvent(type: string, dt: StubDataTransfer, init: MouseEventInit = {}): MouseEvent {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, ...init });
  Object.defineProperty(event, "dataTransfer", { value: dt });
  return event;
}

function stubRect(el: HTMLElement, left: number, width: number): void {
  el.getBoundingClientRect = () =>
    ({
      left,
      width,
      right: left + width,
      top: 0,
      bottom: 28,
      height: 28,
      x: left,
      y: 0,
      toJSON: () => ({}),
    }) as DOMRect;
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function tablist(container: ParentNode): HTMLElement {
  const list = container.querySelector<HTMLElement>('[role="tablist"][aria-label="标签页"]');
  if (!list) throw new Error("tablist not found");
  return list;
}

function tabsOf(list: HTMLElement): HTMLElement[] {
  return [...list.querySelectorAll<HTMLElement>('[role="tab"]')];
}

function tabByText(list: HTMLElement, text: string): HTMLElement {
  const tab = tabsOf(list).find((candidate) => candidate.textContent?.includes(text));
  if (!tab) throw new Error(`tab not found: ${text}`);
  return tab;
}

function tabByTextIn(container: ParentNode, text: string): HTMLElement {
  const tab = [...container.querySelectorAll<HTMLElement>('[role="tab"]')].find((candidate) =>
    candidate.textContent?.includes(text),
  );
  if (!tab) throw new Error(`tab not found: ${text}`);
  return tab;
}

function paneTabIds(index: number): string[] {
  return useUi.getState().workspaces[0]?.panes[index]?.tabs.map((t) => t.id) ?? [];
}

function tabOrder(): string[] {
  return paneTabIds(0);
}

function activeTabId(): string | null | undefined {
  return useUi.getState().workspaces[0]?.panes[0]?.activeTabId;
}

function menuButton(label: string): HTMLButtonElement {
  const item = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
    b.textContent?.includes(label),
  );
  if (!item) throw new Error(`menu item not found: ${label}`);
  return item;
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
    sessions: [],
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws-1",
        closable: true,
        sessionId: "s1",
        panes: [
          {
            id: "p1",
            activeTabId: "t1",
            tabs: [
              { id: "t1", kind: "settings", title: "设置", closable: true },
              { id: "t2", kind: "audit", title: "审计", closable: true },
              { id: "t3", kind: "credentials", title: "凭据", closable: true },
            ],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

describe("tab drag reorder", () => {
  it("dragstart 设置自定义 mime、move effect 与拖拽态样式", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const audit = tabByText(list, "审计");
    expect(audit.getAttribute("draggable")).toBe("true");

    const dt = makeDataTransfer();
    act(() => {
      audit.dispatchEvent(dragEvent("dragstart", dt));
    });
    expect(dt.getData(TAB_MIME)).toBe("t2");
    expect(dt.effectAllowed).toBe("move");
    expect(audit.className).toContain("nx-drag-source");

    act(() => {
      audit.dispatchEvent(dragEvent("dragend", dt));
    });
    expect(audit.className).not.toContain("nx-drag-source");
  });

  it("dragover 按指针位置给出 before/after 插入指示", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const dt = makeDataTransfer();
    act(() => {
      tabByText(list, "设置").dispatchEvent(dragEvent("dragstart", dt));
    });

    const cred = tabByText(list, "凭据");
    stubRect(cred, 100, 80);
    const overBefore = dragEvent("dragover", dt, { clientX: 110 });
    act(() => {
      cred.dispatchEvent(overBefore);
    });
    expect(overBefore.defaultPrevented).toBe(true);
    expect(dt.dropEffect).toBe("move");
    expect(cred.className).toContain("nx-drop-before");

    const overAfter = dragEvent("dragover", dt, { clientX: 170 });
    act(() => {
      cred.dispatchEvent(overAfter);
    });
    expect(cred.className).toContain("nx-drop-after");
    expect(cred.className).not.toContain("nx-drop-before");
  });

  it("dragover 拖到自身时不消费也不显示指示", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const audit = tabByText(list, "审计");
    stubRect(audit, 0, 80);
    const dt = makeDataTransfer();
    act(() => {
      audit.dispatchEvent(dragEvent("dragstart", dt));
    });

    const over = dragEvent("dragover", dt, { clientX: 10 });
    act(() => {
      audit.dispatchEvent(over);
    });
    expect(over.defaultPrevented).toBe(false);
    expect(audit.className).not.toContain("nx-drop-before");
    expect(audit.className).not.toContain("nx-drop-after");
  });

  it("drop 到目标左半侧时插入其前，activeTabId 不变", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const dt = makeDataTransfer();
    act(() => {
      tabByText(list, "设置").dispatchEvent(dragEvent("dragstart", dt));
    });

    const cred = tabByText(list, "凭据");
    stubRect(cred, 100, 80);
    const drop = dragEvent("drop", dt, { clientX: 110 });
    act(() => {
      cred.dispatchEvent(drop);
    });
    expect(drop.defaultPrevented).toBe(true);
    expect(tabOrder()).toEqual(["t2", "t1", "t3"]);
    expect(activeTabId()).toBe("t1");
    expect(tabByText(list, "设置").getAttribute("aria-selected")).toBe("true");
    expect(cred.className).not.toContain("nx-drop-before");
  });

  it("drop 到目标右半侧时插入其后", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const dt = makeDataTransfer();
    act(() => {
      tabByText(list, "凭据").dispatchEvent(dragEvent("dragstart", dt));
    });

    const settings = tabByText(list, "设置");
    stubRect(settings, 0, 80);
    act(() => {
      settings.dispatchEvent(dragEvent("drop", dt, { clientX: 70 }));
    });
    expect(tabOrder()).toEqual(["t1", "t3", "t2"]);
    expect(activeTabId()).toBe("t1");
  });

  it("drop 到自身不改变顺序", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const audit = tabByText(list, "审计");
    const dt = makeDataTransfer();
    act(() => {
      audit.dispatchEvent(dragEvent("dragstart", dt));
      audit.dispatchEvent(dragEvent("drop", dt, { clientX: 10 }));
    });
    expect(tabOrder()).toEqual(["t1", "t2", "t3"]);
  });

  it("拖拽中的 click 不激活标签，dragend 后恢复激活", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const audit = tabByText(list, "审计");
    const dt = makeDataTransfer();
    act(() => {
      audit.dispatchEvent(dragEvent("dragstart", dt));
    });
    click(audit);
    expect(activeTabId()).toBe("t1");

    act(() => {
      audit.dispatchEvent(dragEvent("dragend", dt));
    });
    click(audit);
    expect(activeTabId()).toBe("t2");
  });
});

describe("cross-pane drag guard", () => {
  function useSplitPanes(): void {
    useUi.setState({
      workspaces: [
        {
          id: "ws1",
          kind: "session",
          title: "ws-1",
          closable: true,
          sessionId: "s1",
          panes: [
            {
              id: "p1",
              activeTabId: "t1",
              tabs: [
                { id: "t1", kind: "settings", title: "设置", closable: true },
                { id: "t2", kind: "audit", title: "审计", closable: true },
              ],
            },
            {
              id: "p2",
              activeTabId: "t3",
              tabs: [{ id: "t3", kind: "credentials", title: "凭据", closable: true }],
            },
          ],
          activePaneId: "p1",
          splitRatio: 0.5,
        },
      ],
      activeWorkspaceId: "ws1",
    });
  }

  it("跨 pane 拖拽不显示放置指示，drop 不消费且两侧顺序不变", async () => {
    useSplitPanes();
    mounted = mountApp();
    await flush();

    const settings = tabByTextIn(mounted.container, "设置");
    const cred = tabByTextIn(mounted.container, "凭据");
    stubRect(cred, 100, 80);
    const dt = makeDataTransfer();
    act(() => {
      settings.dispatchEvent(dragEvent("dragstart", dt));
    });
    expect(settings.className).toContain("nx-drag-source");

    const over = dragEvent("dragover", dt, { clientX: 110 });
    act(() => {
      cred.dispatchEvent(over);
    });
    expect(over.defaultPrevented).toBe(false);
    expect(cred.className).not.toContain("nx-drop-before");
    expect(cred.className).not.toContain("nx-drop-after");

    const drop = dragEvent("drop", dt, { clientX: 110 });
    act(() => {
      cred.dispatchEvent(drop);
    });
    expect(drop.defaultPrevented).toBe(false);
    expect(paneTabIds(0)).toEqual(["t1", "t2"]);
    expect(paneTabIds(1)).toEqual(["t3"]);

    act(() => {
      settings.dispatchEvent(dragEvent("dragend", dt));
    });
  });

  it("分屏下同 pane 拖拽排序不受影响", async () => {
    useSplitPanes();
    mounted = mountApp();
    await flush();

    const settings = tabByTextIn(mounted.container, "设置");
    const audit = tabByTextIn(mounted.container, "审计");
    stubRect(audit, 0, 80);
    const dt = makeDataTransfer();
    act(() => {
      settings.dispatchEvent(dragEvent("dragstart", dt));
    });
    act(() => {
      audit.dispatchEvent(dragEvent("drop", dt, { clientX: 70 }));
    });
    expect(paneTabIds(0)).toEqual(["t2", "t1"]);
    expect(paneTabIds(1)).toEqual(["t3"]);
    expect(activeTabId()).toBe("t1");
  });
});

describe("tab context menu move", () => {
  function openMenu(tab: HTMLElement): HTMLElement {
    keyDown(tab, "F10", { shiftKey: true });
    const menu = document.querySelector<HTMLElement>('[role="menu"]');
    if (!menu) throw new Error("menu not found");
    return menu;
  }

  it("边界标签对应移动项 disabled", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    openMenu(tabByText(list, "设置"));
    expect(menuButton("左移标签").disabled).toBe(true);
    expect(menuButton("右移标签").disabled).toBe(false);
    keyDown(document.activeElement as HTMLElement, "Escape");
    await waitFor(() => expect(document.querySelector('[role="menu"]')).toBeNull());

    openMenu(tabByText(list, "凭据"));
    expect(menuButton("左移标签").disabled).toBe(false);
    expect(menuButton("右移标签").disabled).toBe(true);
  });

  it("右移标签移动后 activeTabId 不变", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    openMenu(tabByText(list, "设置"));
    click(menuButton("右移标签"));
    await waitFor(() => expect(tabOrder()).toEqual(["t2", "t1", "t3"]));
    expect(activeTabId()).toBe("t1");
    expect(tabByText(list, "设置").getAttribute("aria-selected")).toBe("true");
  });

  it("左移标签移动中间标签", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    openMenu(tabByText(list, "凭据"));
    click(menuButton("左移标签"));
    await waitFor(() => expect(tabOrder()).toEqual(["t1", "t3", "t2"]));
    expect(activeTabId()).toBe("t1");
  });
});

describe("tab keyboard move", () => {
  it("Ctrl+Shift+ArrowRight 移动当前标签且焦点跟随", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const settings = tabByText(list, "设置");
    act(() => settings.focus());

    const event = keyDown(settings, "ArrowRight", { ctrlKey: true, shiftKey: true });
    expect(event.defaultPrevented).toBe(true);
    expect(tabOrder()).toEqual(["t2", "t1", "t3"]);
    expect(activeTabId()).toBe("t1");
    expect(document.activeElement).toBe(tabByText(list, "设置"));
  });

  it("Ctrl+Shift+ArrowLeft 移动末尾标签", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const cred = tabByText(list, "凭据");
    act(() => cred.focus());

    keyDown(cred, "ArrowLeft", { ctrlKey: true, shiftKey: true });
    expect(tabOrder()).toEqual(["t1", "t3", "t2"]);
    expect(document.activeElement).toBe(tabByText(list, "凭据"));
  });

  it("边界处 Ctrl+Shift+ArrowLeft 不消费事件也不移动", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const settings = tabByText(list, "设置");
    act(() => settings.focus());

    const event = keyDown(settings, "ArrowLeft", { ctrlKey: true, shiftKey: true });
    expect(event.defaultPrevented).toBe(false);
    expect(tabOrder()).toEqual(["t1", "t2", "t3"]);
    expect(document.activeElement).toBe(settings);
  });

  it("普通 ArrowRight 仍然只做激活导航", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container);
    const settings = tabByText(list, "设置");
    act(() => settings.focus());

    keyDown(settings, "ArrowRight");
    expect(tabOrder()).toEqual(["t1", "t2", "t3"]);
    expect(activeTabId()).toBe("t2");
  });
});
