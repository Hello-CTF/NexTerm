/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  promptText: vi.fn(),
  list: vi.fn(),
  mountList: vi.fn(),
  mountRemove: vi.fn(),
  toast: vi.fn(),
  read: vi.fn(),
  write: vi.fn(),
  listenEvent: vi.fn(),
  termWrite: vi.fn(),
}));

const platformState = vi.hoisted(() => ({ mod: "Ctrl" }));

vi.mock("../../app/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/platform")>();
  return { ...actual, modHint: () => platformState.mod };
});

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: { list: mocks.list, read: mocks.read, write: mocks.write },
    mountApi: { list: mocks.mountList, create: vi.fn(), remove: mocks.mountRemove },
    terminalApi: { write: mocks.termWrite },
    sessionApi: {},
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: mocks.listenEvent,
    EVENTS: { fsProgress: "fs://progress" },
  };
});

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));

vi.mock("@codemirror/state", () => {
  class Doc {
    constructor(private readonly text: string) {}
    toString() {
      return this.text;
    }
    get length() {
      return this.text.length;
    }
  }
  return {
    EditorState: {
      create: ({ doc }: { doc: string }) => ({ doc: new Doc(doc), extensions: [] as unknown[] }),
    },
    Prec: { highest: (extension: unknown) => extension },
  };
});
vi.mock("@codemirror/view", () => {
  class EditorView {
    static updateListener = { of: () => ({}) };
    constructor() {}
    destroy() {}
  }
  return { EditorView, keymap: { of: (bindings: unknown) => bindings } };
});
vi.mock("codemirror", () => ({ basicSetup: [] }));
vi.mock("@codemirror/commands", () => ({ indentWithTab: {}, redo: vi.fn(), undo: vi.fn() }));
vi.mock("@codemirror/search", () => ({ search: () => [], openSearchPanel: vi.fn() }));
vi.mock("@codemirror/language", () => ({ StreamLanguage: { define: (mode: unknown) => mode } }));
vi.mock("@codemirror/lang-sql", () => ({ sql: () => [] }));
vi.mock("@codemirror/legacy-modes/mode/shell", () => ({ shell: {} }));
vi.mock("@codemirror/legacy-modes/mode/nginx", () => ({ nginx: {} }));
vi.mock("@codemirror/legacy-modes/mode/yaml", () => ({ yaml: {} }));
vi.mock("@codemirror/legacy-modes/mode/properties", () => ({ properties: {} }));
vi.mock("@codemirror/legacy-modes/mode/javascript", () => ({ javascript: {} }));
vi.mock("@codemirror/legacy-modes/mode/python", () => ({ python: {} }));
vi.mock("../../ui/editorTheme", () => ({ nxHighlight: [] }));

import { useUi } from "../../app/store";
import type { FileEntryDto } from "../../ipc/types";
import { PromptModal } from "../../ui/PromptModal";
import { FileEditor } from "../../features/files/FileEditor";
import { FileTree } from "../../features/files/FileTree";
import { FileBrowser } from "../../features/files/FileBrowser";
import { MountPanel } from "../../features/files/MountPanel";

const SID = "s1";

function session(id: string, kind: string) {
  return {
    id,
    assetId: "a1",
    name: "host",
    kind,
    status: "connected" as const,
    tabs: [],
    createdAt: 0,
  };
}

function entry(name: string, kind: string): FileEntryDto {
  return {
    name,
    path: `~/${name}`,
    kind,
    size: 42,
    mode: "644",
    owner: "u",
    group: "u",
    mtime: 1,
    symlinkTarget: null,
  };
}

function originalDoc(): { path: string; size: number; contentBase64: string } {
  const bytes = new TextEncoder().encode("original");
  let binary = "";
  bytes.forEach((byte) => (binary += String.fromCharCode(byte)));
  return { path: "/a.txt", size: bytes.length, contentBase64: btoa(binary) };
}

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  platformState.mod = "Ctrl";
  mocks.ask.mockResolvedValue(true);
  mocks.promptText.mockResolvedValue(null);
  mocks.list.mockResolvedValue([]);
  mocks.read.mockResolvedValue(originalDoc());
  mocks.listenEvent.mockReturnValue(Promise.resolve(() => {}));
  mocks.mountList.mockResolvedValue([]);
  useUi.setState({
    pushToast: mocks.toast,
    leftOpen: true,
    leftWidth: 260,
    sessions: [],
    textPrompt: null,
    appDialog: null,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ textPrompt: null });
});

describe("PromptModal 多行占位文案", () => {
  it("通用多行弹窗不显示 AI 专用占位提示", () => {
    const resolve = vi.fn();
    useUi.setState({
      textPrompt: {
        message: "要发送到终端的命令（可多行，回车换行）",
        value: "",
        multiLine: true,
        resolve,
      },
    });
    mounted = mount(createElement(PromptModal));
    const textarea = mounted.container.querySelector("textarea");
    if (!textarea) throw new Error("Prompt textarea not found");
    expect(textarea.placeholder).toBe("");
    expect(textarea.placeholder).not.toContain("目标");
  });
});

describe("FileEditor 快捷键标签", () => {
  it("保存按钮与撤销提示跟随平台 mod 约定显示 ⌘", async () => {
    platformState.mod = "⌘";
    mounted = mount(createElement(FileEditor, { sessionId: SID, path: "/a.txt" }));
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-kbd")?.textContent).toBe("⌘ S"),
    );
    expect(
      mounted!.container.querySelector('button[title="撤销 (⌘+Z)"]'),
    ).not.toBeNull();
  });

  it("非 mac 平台显示 Ctrl S", async () => {
    mounted = mount(createElement(FileEditor, { sessionId: SID, path: "/a.txt" }));
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-kbd")?.textContent).toBe("Ctrl S"),
    );
  });
});

describe("FileTree ~ 归因", () => {
  it("WinRM 会话直接从 / 开始，home 按钮如实说明不支持 ~", async () => {
    useUi.setState({ sessions: [session(SID, "winrm")] });
    mounted = mountWithClient(createElement(FileTree, { sessionId: SID }));
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "/"));
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "~");
    const home = mounted!.container.querySelector<HTMLButtonElement>(
      'button[title="WinRM 后端不支持 ~ 展开"]',
    );
    expect(home?.disabled).toBe(true);
  });

  it("SSH 会话 ~ 列表失败时显示真实错误，不甩锅 ~ 展开也不跳走", async () => {
    useUi.setState({ sessions: [session(SID, "ssh")] });
    mocks.list.mockRejectedValue(new Error("connection reset"));
    mounted = mountWithClient(createElement(FileTree, { sessionId: SID }));
    await waitFor(() =>
      expect(mounted!.container.textContent).toContain("connection reset"),
    );
    expect(mocks.list).toHaveBeenCalledWith(SID, "~");
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "/");
    expect(mocks.toast).not.toHaveBeenCalledWith("info", expect.stringContaining("不支持"));
    expect(
      mounted!.container.querySelector('button[title="回到家目录 (~)"]'),
    ).not.toBeNull();
  });
});

describe("FileBrowser 删除按钮", () => {
  it("工具栏删除按钮有 hover 提示", async () => {
    useUi.setState({ sessions: [session(SID, "ssh")] });
    mocks.list.mockResolvedValue([entry("a.txt", "file")]);
    mounted = mountWithClient(createElement(FileBrowser, { sessionId: SID }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    const del = mounted!.container.querySelector<HTMLButtonElement>(
      'button[title="删除选中项"]',
    );
    expect(del).not.toBeNull();
    expect(del?.disabled).toBe(true);
  });
});

describe("MountPanel 断开确认", () => {
  it("断开挂载用 warning 级别确认", async () => {
    useUi.setState({ sessions: [session(SID, "ssh")] });
    mocks.mountList.mockResolvedValue([
      { localPoint: "Z:", remote: "user@host:/data", createdAt: 1 },
    ]);
    mounted = mountWithClient(createElement(MountPanel, { sessionId: SID }));
    await waitFor(() =>
      expect(
        [...mounted!.container.querySelectorAll("button")].some(
          (b) => b.textContent?.trim() === "断开",
        ),
      ).toBe(true),
    );
    clickButton(mounted!.container, "断开");
    await waitFor(() =>
      expect(mocks.ask).toHaveBeenCalledWith("断开 Z:？", { kind: "warning" }),
    );
    expect(mocks.mountRemove).toHaveBeenCalledWith("Z:", SID);
  });
});
