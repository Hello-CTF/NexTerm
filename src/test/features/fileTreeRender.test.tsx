/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  roots: vi.fn(),
  list: vi.fn(),
  connectLocal: vi.fn(),
  listenEvent: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    fsApi: { roots: mocks.roots, list: mocks.list },
    sessionApi: { connectLocal: mocks.connectLocal },
  });
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: mocks.listenEvent };
});

import { FileTree } from "../../features/files/FileTree";
import { resetCwdFollowForTest } from "../../features/files/cwdFollow";
import { useUi, type AppTab } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";

const SID = "s1";
const COUNT = 3000;

function entry(name: string, kind: string): FileEntryDto {
  return { name, path: `~/${name}`, kind, size: 10, mode: "644", owner: "u", group: "u", mtime: 1, symlinkTarget: null };
}

// Deliberately unsorted: descending numeric names plus a directory that must sort first.
const HOME = [
  ...Array.from({ length: COUNT }, (_, i) => entry(`file${COUNT - i}`, "file")),
  entry("zdir", "dir"),
];

let mounted: MountedView | undefined;
let client: QueryClient;

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
  return mount(createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })));
}

function renderedPaths(): string[] {
  return [...(mounted?.container.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])].map((el) => el.dataset.rowPath ?? "");
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  resetCwdFollowForTest();
  // jsdom has no layout: give the scroller a viewport and every row a fixed height so the virtualizer renders rows.
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "tree" ? 600 : this.hasAttribute("data-index") ? 28 : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "tree" ? 600 : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, get: () => 260 });
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.roots.mockResolvedValue({ pathStyle: "posix", roots: [], homePath: "~", supported: true });
  mocks.list.mockImplementation(() => Promise.resolve(HOME.map((e) => ({ ...e }))));
  mocks.connectLocal.mockResolvedValue({ id: SID, assetId: null, name: "Local", kind: "local", status: "connected", tabs: [], createdAt: 1 });
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  seedWorkspace();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
  Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
  Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  Reflect.deleteProperty(HTMLElement.prototype, "offsetWidth");
});

describe("FileTree large directory rendering", () => {
  it("lists directories first and orders names numerically", async () => {
    mounted = mountTree();
    await waitFor(() => expect(renderedPaths().length).toBeGreaterThan(3));
    expect(renderedPaths().slice(0, 4)).toEqual(["~/zdir", "~/file1", "~/file2", "~/file3"]);
    expect(renderedPaths().indexOf("~/file10")).toBeGreaterThan(renderedPaths().indexOf("~/file9"));
  });

  it("does not re-sort the listing when unrelated store state changes", async () => {
    const compareGetter = vi.spyOn(Intl.Collator.prototype, "compare", "get");
    mounted = mountTree();
    await waitFor(() => expect(renderedPaths().length).toBeGreaterThan(3));
    expect(compareGetter).toHaveBeenCalled();
    const afterFirstSort = compareGetter.mock.calls.length;

    for (let i = 0; i < 3; i++) {
      act(() => useUi.getState().pushToast("info", `unrelated ${i}`));
    }
    act(() => useUi.setState({ leftWidth: 300 }));
    act(() => useUi.setState({ leftWidth: 320 }));

    expect(compareGetter.mock.calls.length).toBe(afterFirstSort);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(renderedPaths()[0]).toBe("~/zdir");
  });

  it("reuses a fresh listing on remount but refetches after explicit invalidation", async () => {
    mounted = mountTree();
    await waitFor(() => expect(renderedPaths().length).toBeGreaterThan(3));
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.roots).toHaveBeenCalledTimes(1);

    mounted.unmount();
    mounted = mountTree();
    await waitFor(() => expect(renderedPaths().length).toBeGreaterThan(3));
    expect(mocks.list).toHaveBeenCalledTimes(1);
    expect(mocks.roots).toHaveBeenCalledTimes(1);

    await act(async () => {
      await client.invalidateQueries({ queryKey: ["fs", SID] });
    });
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("restores the view and its cached listings when the tree mounts again", async () => {
    const inner: FileEntryDto = { ...entry("inner", "file"), path: "~/zdir/inner" };
    mocks.list.mockImplementation((_sid: string, dir: string) =>
      Promise.resolve(dir === "~/zdir" ? [inner] : HOME.map((e) => ({ ...e }))),
    );
    mounted = mountTree();
    await waitFor(() => expect(renderedPaths()[0]).toBe("~/zdir"));
    const dir = mounted.container.querySelector<HTMLElement>('[data-row-path="~/zdir"]')!;
    act(() => dir.focus());
    act(() => {
      dir.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }));
    });
    await waitFor(() => expect(renderedPaths()).toContain("~/zdir/inner"));
    const reads = mocks.list.mock.calls.length;
    const rootReads = mocks.roots.mock.calls.length;

    // Switching to another workspace unmounts the tree; by the time it is shown again its listings are stale.
    mounted.unmount();
    await act(async () => {
      await client.invalidateQueries({ refetchType: "none" });
    });
    mounted = mountTree();
    await waitFor(() => expect(renderedPaths()).toContain("~/zdir/inner"));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(mocks.list).toHaveBeenCalledTimes(reads);
    expect(mocks.roots).toHaveBeenCalledTimes(rootReads);
  });
});
