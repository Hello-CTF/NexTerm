/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, setInputValue, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  roots: vi.fn(),
  list: vi.fn(),
  remove: vi.fn(),
  chmod: vi.fn(),
  listenEvent: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    fsApi: { roots: mocks.roots, list: mocks.list, delete: mocks.remove, chmod: mocks.chmod },
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
import { setMacPlatform } from "../../app/platform";
import { forgetFsRootsForTest } from "../../ipc/fsRootsCache";
import type { FileEntryDto } from "../../ipc/types";
import { FileTree } from "../../features/files/FileTree";
import { sessionConflictCaseInsensitive } from "../../features/files/transferActions";

const SID = "s1";

function entry(name: string, kind: string, extra: Partial<FileEntryDto> = {}): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: 10,
    mode: kind === "dir" ? "drwxr-xr-x" : "-rw-rw-rw-",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
    ...extra,
  };
}

const HOME_ENTRIES: FileEntryDto[] = [
  entry("a.zip", "file"),
  entry("sub", "dir"),
  entry("linkdir", "symlink", { symlinkTarget: "/srv/data", targetKind: "dir" }),
  entry("broken", "symlink", { symlinkTarget: "/missing" }),
  entry(".secret", "file", { hidden: true }),
];

const POSIX_CAPS = { chmod: "posix", pack: true, extract: true, caseInsensitive: false };

let client: QueryClient;
let mounted: MountedView | undefined;

function mountTree(): MountedView {
  return mount(createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })));
}

function rows(): HTMLElement[] {
  return [...(mounted?.container.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])];
}

function row(name: string): HTMLElement {
  const hit = rows().find((r) => r.dataset.rowPath === `~/${name}`);
  if (!hit) throw new Error(`row not found: ${name}`);
  return hit;
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): void {
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
  });
}

function menuLabels(): string[] {
  return [...(mounted?.container.querySelectorAll('[role="menuitem"]') ?? [])].map((b) => b.textContent ?? "");
}

async function openMenu(name: string): Promise<void> {
  const target = row(name);
  act(() => target.focus());
  keyDown(target, "ContextMenu");
  await waitForRendered(() => expect(mounted!.container.querySelector('[role="menu"]')).not.toBeNull());
}

async function mountWith(capabilities: typeof POSIX_CAPS | null, pathStyle = "posix"): Promise<void> {
  mocks.roots.mockResolvedValue({ pathStyle, roots: [], homePath: null, supported: true, capabilities });
  mounted = mountTree();
  await waitForRendered(() => expect(row("a.zip")).toBeTruthy());
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  forgetFsRootsForTest();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mocks.ask.mockResolvedValue(true);
  mocks.promptText.mockResolvedValue(null);
  mocks.list.mockImplementation((_s: string, p: string) =>
    Promise.resolve(p === "~" ? HOME_ENTRIES.map((e) => ({ ...e })) : []),
  );
  mocks.remove.mockResolvedValue(undefined);
  mocks.chmod.mockResolvedValue(undefined);
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  useUi.setState({
    pushToast: mocks.toast,
    leftOpen: true,
    leftWidth: 260,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [{ id: SID, assetId: null, name: "host", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("FileTree symbolic links", () => {
  it("browses a link to a directory like a directory and marks a broken link", async () => {
    await mountWith(POSIX_CAPS);
    const link = row("linkdir");
    expect(link.getAttribute("aria-expanded")).toBe("false");
    expect(row("broken").hasAttribute("data-broken-link")).toBe(true);
    expect(row("broken").getAttribute("title")).toContain("无法解析链接目标");
    expect(link.hasAttribute("data-broken-link")).toBe(false);

    click(link.querySelector("button.nx-tree-caret")!);
    await waitForRendered(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~/linkdir"));
  });

  it("deletes a link to a directory as the link alone, never recursively", async () => {
    await mountWith(POSIX_CAPS);
    const link = row("linkdir");
    act(() => link.focus());
    keyDown(link, "Delete");
    await waitForRendered(() => expect(mocks.remove).toHaveBeenCalledWith(SID, "~/linkdir", false));
    const [message] = mocks.ask.mock.calls[0] as [string];
    expect(message).toContain("只删除链接本身");
    expect(message).not.toContain("递归删除");
  });
});

describe("FileTree hidden entries", () => {
  it("hides hidden entries by default and keeps the choice when the tree mounts again", async () => {
    await mountWith(POSIX_CAPS);
    expect(rows().some((r) => r.dataset.rowPath === "~/.secret")).toBe(false);
    const toggle = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="显示隐藏文件"]')!;
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    click(toggle);
    await waitForRendered(() => expect(row(".secret")).toBeTruthy());
    expect(toggle.getAttribute("aria-pressed")).toBe("true");

    mounted!.unmount();
    mounted = mountTree();
    await waitForRendered(() => expect(row(".secret")).toBeTruthy());
  });
});

describe("FileTree capability-gated actions", () => {
  it("offers chmod, pack download and extract when the backend declares them", async () => {
    await mountWith(POSIX_CAPS);
    await openMenu("a.zip");
    expect(menuLabels().some((l) => l.includes("权限…"))).toBe(true);
    expect(menuLabels().some((l) => l.includes("解压"))).toBe(true);
    keyDown(document.activeElement as HTMLElement, "Escape");
    await openMenu("sub");
    expect(menuLabels().some((l) => l.includes("打包下载当前目录"))).toBe(true);
  });

  it("replaces chmod with a read-only toggle and hides pack and extract on a Windows-attribute backend", async () => {
    await mountWith({ chmod: "readonly", pack: false, extract: false, caseInsensitive: true });
    await openMenu("a.zip");
    const labels = menuLabels();
    expect(labels.some((l) => l.includes("权限…"))).toBe(false);
    expect(labels.some((l) => l.includes("解压"))).toBe(false);
    const item = [...mounted!.container.querySelectorAll('[role="menuitem"]')].find((b) =>
      b.textContent?.includes("设为只读"),
    );
    expect(item).toBeTruthy();
    click(item!);
    await waitForRendered(() => expect(mocks.chmod).toHaveBeenCalledWith(SID, "~/a.zip", 0o444));

    await openMenu("sub");
    expect(menuLabels().some((l) => l.includes("打包下载当前目录"))).toBe(false);
  });

  it("offers no permission action when the backend cannot change permissions", async () => {
    await mountWith({ chmod: "none", pack: false, extract: true, caseInsensitive: false });
    await openMenu("a.zip");
    const labels = menuLabels();
    expect(labels.some((l) => l.includes("权限…") || l.includes("设为只读") || l.includes("取消只读"))).toBe(false);
    expect(labels.some((l) => l.includes("解压"))).toBe(true);
  });

  it("uses the declared case folding for upload conflicts of an SSH session", async () => {
    await mountWith({ ...POSIX_CAPS, caseInsensitive: true });
    expect(sessionConflictCaseInsensitive(SID)).toBe(true);
  });
});

describe("FileTree keyboard navigation", () => {
  it("goes to the parent with Backspace or Alt+Up without deleting the focused row", async () => {
    await mountWith(POSIX_CAPS);
    const target = row("a.zip");
    act(() => target.focus());
    keyDown(target, "Backspace");
    await waitForRendered(() => expect(mocks.list).toHaveBeenCalledWith(SID, "/"));
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("deletes with Cmd+Backspace on macOS", async () => {
    setMacPlatform(true);
    try {
      await mountWith(POSIX_CAPS);
      const target = row("a.zip");
      act(() => target.focus());
      keyDown(target, "Backspace", { metaKey: true });
      await waitForRendered(() => expect(mocks.remove).toHaveBeenCalledWith(SID, "~/a.zip", false));
      expect(mocks.list).not.toHaveBeenCalledWith(SID, "/");
    } finally {
      setMacPlatform(false);
    }
  });

  it("goes to the parent with Alt+Up", async () => {
    await mountWith(POSIX_CAPS);
    const target = row("sub");
    act(() => target.focus());
    keyDown(target, "ArrowUp", { altKey: true });
    await waitForRendered(() => expect(mocks.list).toHaveBeenCalledWith(SID, "/"));
  });

  it("renames with F2 and refreshes with F5", async () => {
    await mountWith(POSIX_CAPS);
    const target = row("a.zip");
    act(() => target.focus());
    // The rename settles after the prompt resolves, so the key press and its async tail share one act scope.
    await act(async () => {
      target.dispatchEvent(new KeyboardEvent("keydown", { key: "F2", bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    expect(mocks.promptText).toHaveBeenCalledWith("重命名 ~/a.zip", "a.zip");

    const before = mocks.list.mock.calls.length;
    act(() => target.focus());
    keyDown(target, "F5");
    await waitForRendered(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(before));
    await flush();
  });

  it("leaves Backspace to the path input while it is edited", async () => {
    await mountWith(POSIX_CAPS);
    click(mounted!.container.querySelector('button[aria-label="输入路径跳转"]')!);
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-pathbar input")!;
    keyDown(input, "Backspace");
    await flush();
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "/");
  });
});

describe("FileTree typed path", () => {
  it("resolves a relative path against the shown directory", async () => {
    await mountWith(POSIX_CAPS);
    click(mounted!.container.querySelector('button[aria-label="输入路径跳转"]')!);
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-pathbar input")!;
    setInputValue(input, "sub");
    keyDown(input, "Enter");
    await waitForRendered(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~/sub"));
  });
});
