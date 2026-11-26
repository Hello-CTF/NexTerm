/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, mount, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  roots: vi.fn(),
  list: vi.fn(),
  listenEvent: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    fsApi: { watch: () => new Promise<never>(() => undefined), unwatch: async () => ({}), roots: mocks.roots, list: mocks.list },
  });
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: mocks.listenEvent };
});
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () => Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
    measure: () => {},
    scrollToIndex: () => {},
  }),
  defaultRangeExtractor: () => [],
}));

import { useUi } from "../../app/store";
import { clearFsRoots } from "../../ipc/fsRootsCache";
import type { FileEntryDto } from "../../ipc/types";
import { FileTree } from "../../features/files/FileTree";

const SID = "s1";

function entry(name: string, kind: string): FileEntryDto {
  return { name, path: `~/${name}`, kind, size: 10, mode: kind === "dir" ? "drwxr-xr-x" : "-rw-r--r--", owner: "u", group: "u", mtime: 1, symlinkTarget: null };
}

let mounted: MountedView | undefined;

function row(name: string): HTMLElement {
  const hit = [...mounted!.container.querySelectorAll<HTMLElement>('[role="treeitem"]')].find((r) => r.dataset.rowPath === `~/${name}`);
  if (!hit) throw new Error(`row not found: ${name}`);
  return hit;
}

async function openMenu(name: string): Promise<void> {
  const target = row(name);
  act(() => target.focus());
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true, cancelable: true }));
  });
  await waitForRendered(() => expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull());
}

function menuItem(label: string): HTMLButtonElement | undefined {
  return [...mounted!.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) => b.textContent?.includes(label));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  clearFsRoots();
  mocks.roots.mockResolvedValue({ pathStyle: "posix", roots: [], homePath: null, supported: true, capabilities: null });
  mocks.list.mockImplementation((_s: string, p: string) => Promise.resolve(p === "~" ? [entry("app.conf", "file"), entry("logs", "dir")] : []));
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  useUi.setState({
    pushToast: mocks.toast,
    setRightOpen: vi.fn(),
    aiPendingRefs: [],
    leftOpen: true,
    leftWidth: 260,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [{ id: SID, assetId: null, name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

async function mountTree(): Promise<void> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })));
  await waitForRendered(() => expect(row("app.conf")).toBeTruthy());
}

describe("FileTree references a file for the AI", () => {
  it("adds a closed file as a path reference from its context menu", async () => {
    await mountTree();
    await openMenu("app.conf");
    click(menuItem("引用到 AI")!);
    expect(useUi.getState().aiPendingRefs).toEqual([
      {
        kind: "file",
        id: "path:s1:~/app.conf",
        label: "app.conf",
        detail: "~/app.conf · web-01",
        refId: "path:s1:~/app.conf",
        path: "~/app.conf",
        sessionId: SID,
        sourceFile: { sessionId: SID, path: "~/app.conf" },
      },
    ]);
    expect(useUi.getState().setRightOpen).toHaveBeenCalledWith(true);
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("offers no file reference for a directory", async () => {
    await mountTree();
    await openMenu("logs");
    expect(menuItem("引用到 AI")).toBeUndefined();
  });
});
