/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { mount, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  roots: vi.fn(),
  list: vi.fn(),
  connectLocal: vi.fn(),
  listenEvent: vi.fn(),
  watch: vi.fn(),
  unwatch: vi.fn(),
  handlers: new Map<string, (payload: unknown) => void>(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(
    await importOriginal<typeof import("../../ipc/commands")>(),
    {
      fsApi: {
        watch: mocks.watch,
        unwatch: mocks.unwatch,
        roots: mocks.roots,
        list: mocks.list,
      },
      sessionApi: { connectLocal: mocks.connectLocal },
    },
  );
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
const COUNT = 3;

function entry(name: string, kind: string): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: 10,
    mode: "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

// Deliberately unsorted: descending numeric names plus a directory that must sort first.
const HOME = [
  ...Array.from({ length: COUNT }, (_, i) => entry(`file${COUNT - i}`, "file")),
  entry("zdir", "dir"),
];

let mounted: MountedView | undefined;
let client: QueryClient;

function seedWorkspace(): void {
  const tab: AppTab = {
    id: "t1",
    kind: "terminal",
    title: "Local",
    sessionId: SID,
    closable: true,
  };
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
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: SID,
        assetId: null,
        name: "Local",
        kind: "local",
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
    ],
    leftOpen: true,
    leftWidth: 260,
    toasts: [],
  });
}

function mountTree(): MountedView {
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(FileTree, { sessionId: SID }),
    ),
  );
}

function renderedPaths(): string[] {
  return [
    ...(mounted?.container.querySelectorAll<HTMLElement>('[role="treeitem"]') ??
      []),
  ].map((el) => el.dataset.rowPath ?? "");
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
      return this.getAttribute("role") === "tree"
        ? 600
        : this.hasAttribute("data-index")
          ? 28
          : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "tree" ? 600 : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 260,
  });
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.roots.mockResolvedValue({
    pathStyle: "posix",
    roots: [],
    homePath: "~",
    supported: true,
  });
  mocks.list.mockImplementation(() =>
    Promise.resolve(HOME.map((e) => ({ ...e }))),
  );
  mocks.connectLocal.mockResolvedValue({
    id: SID,
    assetId: null,
    name: "Local",
    kind: "local",
    status: "connected",
    tabs: [],
    createdAt: 1,
  });
  mocks.listenEvent.mockImplementation(
    (topic: string, handler: (payload: unknown) => void) => {
      mocks.handlers.set(topic, handler);
      return Promise.resolve(() => mocks.handlers.delete(topic));
    },
  );
  mocks.watch.mockResolvedValue({
    sessionId: SID,
    mode: "poll",
    channel: "sftp",
    reason: "主机上运行的 helper 版本较旧",
  });
  mocks.unwatch.mockResolvedValue({});
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

describe("FileTree follows changes on the host", () => {
  it("watches the shown directories, refetches a changed one and shows how changes are followed", async () => {
    mounted = mountTree();
    await waitForRendered(() =>
      expect(renderedPaths().length).toBeGreaterThan(1),
    );
    await waitForRendered(() =>
      expect(mocks.watch).toHaveBeenCalledWith(SID, expect.any(String), ["~"]),
    );
    await waitForRendered(() =>
      expect(
        mounted!.container.querySelector('[data-testid="fs-watch-badge"]'),
      ).not.toBeNull(),
    );
    const badge = mounted!.container.querySelector<HTMLElement>(
      '[data-testid="fs-watch-badge"]',
    )!;
    expect(badge.dataset.mode).toBe("poll");
    expect(badge.title).toContain("SFTP");
    expect(badge.title).toContain("helper 版本较旧");

    const listsBefore = mocks.list.mock.calls.length;
    mocks.list.mockImplementation(() =>
      Promise.resolve([...HOME, entry("made-by-claude.txt", "file")]),
    );
    act(() =>
      mocks.handlers.get("fs://changed")?.({
        sessionId: "other",
        dir: "~",
        names: ["x"],
      }),
    );
    act(() =>
      mocks.handlers.get("fs://changed")?.({
        sessionId: SID,
        dir: "~",
        names: ["made-by-claude.txt"],
      }),
    );
    await waitForRendered(() =>
      expect(renderedPaths()).toContain("~/made-by-claude.txt"),
    );
    expect(mocks.list.mock.calls.length).toBe(listsBefore + 1);

    act(() =>
      mocks.handlers.get("fs://watch")?.({
        sessionId: SID,
        mode: "push",
        channel: "helper",
      }),
    );
    await waitForRendered(() =>
      expect(
        mounted!.container.querySelector<HTMLElement>(
          '[data-testid="fs-watch-badge"]',
        )?.dataset.mode,
      ).toBe("push"),
    );

    mounted.unmount();
    mounted = undefined;
    expect(mocks.unwatch).toHaveBeenCalledWith(
      SID,
      mocks.watch.mock.calls[0]?.[1],
    );
  });
});
