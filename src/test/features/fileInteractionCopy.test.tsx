/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
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
    systemApi: { platform: vi.fn().mockResolvedValue("windows") },
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
  class Compartment {
    of(extension: unknown) {
      return { compartment: this, extension };
    }
    reconfigure(extension: unknown) {
      return { compartment: this, extension };
    }
  }
  return {
    Compartment,
    EditorState: {
      create: ({ doc, extensions }: { doc: string; extensions: unknown[] }) => ({
        doc: new Doc(doc),
        extensions,
      }),
    },
    Prec: { highest: (extension: unknown) => extension },
  };
});
vi.mock("@codemirror/view", () => {
  class EditorView {
    static updateListener = { of: () => ({}) };
    constructor(_config: unknown) {}
    dispatch(_change: unknown) {}
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

describe("PromptModal multiline placeholder copy", () => {
  it("does not show the AI-specific placeholder in a generic multiline prompt", () => {
    const resolve = vi.fn();
    useUi.setState({
      textPrompt: {
        message: "Command to send to the terminal (multiple lines allowed; Enter inserts a newline)",
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

describe("FileEditor shortcut labels", () => {
  it("shows ⌘ in save and undo shortcuts when the platform modifier is ⌘", async () => {
    platformState.mod = "⌘";
    mounted = mountWithClient(createElement(FileEditor, { sessionId: SID, path: "/a.txt" }));
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-kbd")?.textContent).toBe("⌘ S"),
    );
    expect(
      mounted!.container.querySelector('button[title="撤销 (⌘+Z)"]'),
    ).not.toBeNull();
  });

  it("gives every icon-only toolbar button an accessible name", async () => {
    mounted = mountWithClient(createElement(FileEditor, { sessionId: SID, path: "/a.txt", onClose: () => {} }));
    await waitFor(() => expect(mounted!.container.querySelector(".nx-kbd")).not.toBeNull());
    const unnamed = [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-toolbar button")].filter(
      (b) => !(b.getAttribute("aria-label") || b.textContent?.trim()),
    );
    expect(unnamed).toHaveLength(0);
  });

  it("shows Ctrl S on non-mac platforms", async () => {
    mounted = mountWithClient(createElement(FileEditor, { sessionId: SID, path: "/a.txt" }));
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-kbd")?.textContent).toBe("Ctrl S"),
    );
  });
});

describe("FileTree tilde handling", () => {
  it("starts WinRM sessions at / and marks ~ as unsupported on the home button", async () => {
    useUi.setState({ sessions: [session(SID, "winrm")] });
    mounted = mountWithClient(createElement(FileTree, { sessionId: SID }));
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "/"));
    expect(mocks.list).not.toHaveBeenCalledWith(SID, "~");
    const home = mounted!.container.querySelector<HTMLButtonElement>(
      'button[title="WinRM 后端不支持 ~ 展开"]',
    );
    expect(home?.disabled).toBe(true);
  });

  it("falls back from ~ to / after WinRM session data arrives late, without leaving the error row", async () => {
    useUi.setState({ sessions: [] });
    mocks.list.mockImplementation((_s: string, p: string) =>
      p === "~" ? Promise.reject(new Error("tilde unsupported")) : Promise.resolve([]),
    );
    mounted = mountWithClient(createElement(FileTree, { sessionId: SID }));
    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "~"));

    act(() => {
      useUi.setState({ sessions: [session(SID, "winrm")] });
    });

    await waitFor(() => expect(mocks.list).toHaveBeenCalledWith(SID, "/"));
    expect(mounted!.container.textContent).not.toContain("tilde unsupported");
    const home = mounted!.container.querySelector<HTMLButtonElement>(
      'button[title="WinRM 后端不支持 ~ 展开"]',
    );
    expect(home?.disabled).toBe(true);
  });

  it("shows the real error when an SSH ~ listing fails instead of blaming ~ expansion or navigating away", async () => {
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

describe("FileTree toolbar", () => {
  it("no longer carries redundant download or delete buttons", async () => {
    useUi.setState({ sessions: [session(SID, "ssh")] });
    mocks.list.mockResolvedValue([entry("a.txt", "file")]);
    mounted = mountWithClient(createElement(FileTree, { sessionId: SID }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("a.txt"));
    expect(mounted!.container.querySelector('button[title="删除选中项"]')).toBeNull();
    expect(mounted!.container.querySelector('button[title="下载选中项"]')).toBeNull();
  });
});

describe("MountPanel disconnect confirmation", () => {
  it("uses a warning confirmation when disconnecting a mount", async () => {
    useUi.setState({ sessions: [session(SID, "ssh")] });
    mocks.mountList.mockResolvedValue([
      { id: "m1", localPoint: "Z:", remote: "user@host:/data", sessionId: SID, managed: true, createdAt: 1 },
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
      expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("断开挂载点 Z:？"), { kind: "warning" }),
    );
    expect(mocks.mountRemove).toHaveBeenCalledWith("Z:", SID);
  });
});
