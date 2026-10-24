/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  roots: vi.fn(),
  list: vi.fn(),
  write: vi.fn(),
  rename: vi.fn(),
  chmod: vi.fn(),
  checksum: vi.fn(),
  mkdir: vi.fn(),
  remove: vi.fn(),
  upload: vi.fn(),
  download: vi.fn(),
  packDownload: vi.fn(),
  extract: vi.fn(),
  termWrite: vi.fn(),
  copy: vi.fn(),
  lineExec: vi.fn(),
  connect: vi.fn(),
  connectLocal: vi.fn(),
  connectDevice: vi.fn(),
  knownHostAccept: vi.fn(),
  listenEvent: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    ask: mocks.ask,
    promptText: mocks.promptText,
    pickLocalFile: mocks.pickLocalFile,
    pickSavePath: mocks.pickSavePath,
    finishSave: mocks.finishSave,
    discardStaged: mocks.discardStaged,
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    fsApi: {
      roots: mocks.roots,
      list: mocks.list,
      write: mocks.write,
      rename: mocks.rename,
      chmod: mocks.chmod,
      checksum: mocks.checksum,
      mkdir: mocks.mkdir,
      delete: mocks.remove,
      upload: mocks.upload,
      download: mocks.download,
      packDownload: mocks.packDownload,
      extract: mocks.extract,
    },
    sessionApi: {
      connect: mocks.connect,
      connectLocal: mocks.connectLocal,
      connectDevice: mocks.connectDevice,
      lineExec: mocks.lineExec,
    },
    terminalApi: { write: mocks.termWrite },
    assetApi: { knownHostAccept: mocks.knownHostAccept },
  });
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { ...actual.EVENTS, fsProgress: "fs://progress" },
  };
});

vi.mock("../../ui/clipboard", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/clipboard")>();
  return { ...actual, copyText: mocks.copy };
});

import { FileTree } from "../../features/files/FileTree";
import { resetCwdFollowForTest } from "../../features/files/cwdFollow";
import { useUi, type AppTab } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const SID = "s1";
const TOTAL = 5000;
const ROW_HEIGHT = 28;
const VIEWPORT = 600;

function entry(name: string, kind: string, dir = "~"): FileEntryDto {
  return {
    name,
    path: `${dir}/${name}`,
    kind,
    size: 10,
    mode: kind === "dir" ? "755" : "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

// One directory first (dirs sort before files), then thousands of files.
const FILES = Array.from({ length: TOTAL }, (_, i) => entry(`f${String(i).padStart(5, "0")}.txt`, "file"));
const HOME = [entry("sub", "dir"), ...FILES];
const SUB = [entry("inner.txt", "file", "~/sub")];
const LAST = FILES[FILES.length - 1];

let mounted: MountedView | undefined;

function seedWorkspace(): void {
  const tab: AppTab = { id: "t1", kind: "terminal", title: "Local", sessionId: SID, closable: true };
  useUi.setState({
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "Local",
        sessionId: SID,
        assetId: undefined,
        assetKind: "local",
        panes: [{ id: "p1", tabs: [tab], activeTabId: tab.id }],
        activePaneId: "p1",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [{ id: SID, assetId: null, name: "Local", kind: "local", status: "connected", tabs: [], createdAt: 0 }],
    leftOpen: true,
    leftWidth: 260,
    toasts: [],
  });
}

function mountTree(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })));
}

function items(): HTMLElement[] {
  return [...(mounted?.container.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])];
}

function itemByPath(path: string): HTMLElement | undefined {
  return items().find((el) => el.dataset.rowPath === path);
}

function keyDown(target: HTMLElement, key: string): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  resetCwdFollowForTest();
  // jsdom has no layout: give the scroller a fixed viewport and every row a fixed height.
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "tree" ? VIEWPORT : this.hasAttribute("data-index") ? ROW_HEIGHT : 0;
    },
  });
  // The virtualizer derives the max scroll offset of the last row from these.
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "tree" ? (HOME.length + 1) * ROW_HEIGHT : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "tree" ? VIEWPORT : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 260,
  });
  Object.defineProperty(Element.prototype, "scrollTo", {
    configurable: true,
    writable: true,
    value(this: Element, options?: ScrollToOptions | number) {
      this.scrollTop = typeof options === "object" ? (options.top ?? 0) : 0;
      // Browsers deliver scroll events asynchronously; a synchronous one would re-enter React mid-event.
      setTimeout(() => this.dispatchEvent(new Event("scroll")), 0);
    },
  });
  mocks.roots.mockResolvedValue({ pathStyle: "posix", roots: [], homePath: "~", supported: true });
  mocks.list.mockImplementation((_s: string, p: string) =>
    Promise.resolve(p === "~" ? HOME.map((e) => ({ ...e })) : p === "~/sub" ? SUB.map((e) => ({ ...e })) : []),
  );
  mocks.connectLocal.mockResolvedValue({ id: SID, assetId: null, name: "Local", kind: "local", status: "connected", tabs: [], createdAt: 1 });
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  seedWorkspace();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
  Reflect.deleteProperty(HTMLElement.prototype, "offsetWidth");
  Reflect.deleteProperty(HTMLElement.prototype, "scrollHeight");
  Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  Reflect.deleteProperty(Element.prototype, "scrollTo");
});

describe("FileTree virtualization", () => {
  it("renders only a bounded window of rows for a huge directory and keeps ARIA set metadata", async () => {
    mounted = mountTree();
    await waitFor(() => expect(items().length).toBeGreaterThan(0));

    expect(items().length).toBeLessThan(60);
    expect(mounted.container.textContent).toContain("5001 项");
    const first = itemByPath("~/sub")!;
    expect(first.getAttribute("aria-level")).toBe("1");
    expect(first.getAttribute("aria-setsize")).toBe(String(HOME.length));
    expect(first.getAttribute("aria-posinset")).toBe("1");
    expect(itemByPath(LAST.path)).toBeUndefined();
  });

  it("End focuses the last entry and scrolls it into the rendered window; Home returns to the first", async () => {
    mounted = mountTree();
    await waitFor(() => expect(itemByPath("~/sub")).toBeTruthy());

    const event = keyDown(itemByPath("~/sub")!, "End");
    expect(event.defaultPrevented).toBe(true);
    await waitFor(() => expect(itemByPath(LAST.path)).toBeTruthy());
    await waitFor(() => expect(document.activeElement).toBe(itemByPath(LAST.path)));
    expect(items().length).toBeLessThan(60);
    expect(itemByPath(LAST.path)!.getAttribute("aria-posinset")).toBe(String(HOME.length));

    keyDown(itemByPath(LAST.path)!, "Home");
    await waitFor(() => expect(document.activeElement).toBe(itemByPath("~/sub")));
  });

  it("ArrowDown steps through rows and keeps the focused row mounted after scrolling away", async () => {
    mounted = mountTree();
    await waitFor(() => expect(itemByPath("~/sub")).toBeTruthy());

    const sub = itemByPath("~/sub")!;
    sub.focus();
    keyDown(sub, "ArrowDown");
    await waitFor(() => expect(document.activeElement).toBe(itemByPath(FILES[0].path)));

    const scroller = mounted.container.querySelector<HTMLElement>('[role="tree"]')!;
    act(() => {
      scroller.scrollTop = ROW_HEIGHT * 3000;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(itemByPath(FILES[2999].path)).toBeTruthy());
    expect(itemByPath(FILES[0].path)).toBeTruthy();
    expect(document.activeElement).toBe(itemByPath(FILES[0].path));
  });

  it("expands and collapses a directory among virtualized rows", async () => {
    mounted = mountTree();
    await waitFor(() => expect(itemByPath("~/sub")).toBeTruthy());

    click(itemByPath("~/sub")!);
    await waitFor(() => expect(itemByPath("~/sub/inner.txt")).toBeTruthy());
    expect(itemByPath("~/sub")!.getAttribute("aria-expanded")).toBe("true");
    expect(itemByPath("~/sub/inner.txt")!.getAttribute("aria-level")).toBe("2");
    expect(itemByPath("~/sub/inner.txt")!.getAttribute("aria-setsize")).toBe("1");
    expect(items().length).toBeLessThan(60);

    click(itemByPath("~/sub")!);
    await waitFor(() => expect(itemByPath("~/sub/inner.txt")).toBeUndefined());
    expect(itemByPath("~/sub")!.getAttribute("aria-expanded")).toBe("false");
  });
});
