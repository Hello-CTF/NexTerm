/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, deferred, flush, mount, setSelectValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  read: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));
interface FakeEditorState {
  doc: { toString: () => string; length: number };
  extensions: unknown[];
}
interface FakeEditorView {
  state: FakeEditorState;
  dispatch: (change: { changes: { from: number; to: number; insert: string } }) => void;
}
const editors = vi.hoisted(() => ({ views: [] as FakeEditorView[] }));

vi.mock("../../ipc/commands", () => ({
  fsApi: { read: mocks.read, write: mocks.write },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
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
    static updateListener = {
      of: (listener: unknown) => ({ codeMirrorListener: listener }),
    };
    state: FakeEditorState;
    private readonly listeners: Array<(update: unknown) => void> = [];
    constructor(config: { state: FakeEditorState }) {
      this.state = config.state;
      const visit = (value: unknown): void => {
        if (Array.isArray(value)) {
          value.forEach(visit);
        } else if (value && typeof value === "object") {
          if ("codeMirrorListener" in value) {
            this.listeners.push((value as { codeMirrorListener: (update: unknown) => void }).codeMirrorListener);
          } else {
            Object.values(value).forEach(visit);
          }
        }
      };
      visit(this.state.extensions);
      editors.views.push(this);
    }
    dispatch({ changes }: { changes: { from: number; to: number; insert: string } }) {
      const current = this.state.doc.toString() as string;
      const next = `${current.slice(0, changes.from)}${changes.insert}${current.slice(changes.to)}`;
      this.state = { ...this.state, doc: { toString: () => next, length: next.length } };
      this.listeners.forEach((listener) => listener({ docChanged: true, state: this.state }));
    }
    destroy() {}
  }
  return { EditorView, keymap: { of: (bindings: unknown) => bindings } };
});
vi.mock("codemirror", () => ({ basicSetup: [] }));
vi.mock("@codemirror/commands", () => ({
  indentWithTab: {},
  redo: vi.fn(),
  undo: vi.fn(),
}));
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

import { FileEditor } from "../../features/files/FileEditor";
import { useUi } from "../../app/store";

function originalDoc(): { path: string; size: number; contentBase64: string } {
  const bytes = new TextEncoder().encode("original");
  let binary = "";
  bytes.forEach((byte) => (binary += String.fromCharCode(byte)));
  return { path: "/a.txt", size: bytes.length, contentBase64: btoa(binary) };
}

async function mountEditor(): Promise<MountedView> {
  const mounted = mount(createElement(FileEditor, { sessionId: "s", path: "/a.txt" }));
  await waitFor(() => expect(editors.views).toHaveLength(1));
  return mounted;
}

function editDocument(text: string): void {
  const view = editors.views[editors.views.length - 1];
  const length = view.state.doc.length as number;
  act(() => {
    view.dispatch({ changes: { from: 0, to: length, insert: text } });
  });
}

async function startSave(container: HTMLElement) {
  editDocument("changed");
  await flush();
  const save = container.querySelector<HTMLButtonElement>('button[title^="保存（远端"]');
  if (!save) throw new Error("Save button not found");
  click(save);
  await waitFor(() => expect(mocks.write).toHaveBeenCalledOnce());
}

async function resolveInAct<T>(
  pending: { promise: Promise<T>; resolve: (value: T) => void },
  value: T,
): Promise<void> {
  await act(async () => {
    pending.resolve(value);
    await pending.promise;
  });
  await flush();
}

describe("FileEditor in-flight save safeguards", () => {
  let mounted: MountedView | undefined;
  beforeEach(() => {
    vi.clearAllMocks();
    editors.views = [];
    document.body.replaceChildren();
    mocks.read.mockResolvedValue(originalDoc());
    mocks.ask.mockResolvedValue(true);
    useUi.setState({ pushToast: mocks.toast });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("blocks reload until a deferred write completes instead of discarding then clearing dirty", async () => {
    const write = deferred<void>();
    mocks.write.mockReturnValue(write.promise);
    mounted = await mountEditor();
    await startSave(mounted.container);
    const reload = mounted.container.querySelector<HTMLButtonElement>('button[title="重新读取（丢弃未保存改动）"]');
    if (!reload) throw new Error("Reload button not found");
    expect(reload.disabled).toBe(true);
    reload.disabled = false;
    click(reload);
    expect(mocks.read).toHaveBeenCalledOnce();
    expect(mocks.ask).not.toHaveBeenCalled();

    await resolveInAct(write, undefined);
    expect(mounted.container.textContent).not.toContain("未保存");
    expect(reload.disabled).toBe(false);
  });

  it("blocks encoding changes and their discard confirmation during a deferred write", async () => {
    const write = deferred<void>();
    mocks.write.mockReturnValue(write.promise);
    mounted = await mountEditor();
    await startSave(mounted.container);
    const select = mounted.container.querySelector<HTMLSelectElement>("select");
    if (!select) throw new Error("Encoding select not found");
    expect(select.disabled).toBe(true);
    select.disabled = false;
    setSelectValue(select, "gbk");
    expect(mocks.read).toHaveBeenCalledOnce();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringMatching(/保存进行中/));
    expect(mounted.container.textContent).toContain("未保存");

    await resolveInAct(write, undefined);
    expect(mounted.container.textContent).not.toContain("未保存");
  });

  it("rechecks save ownership after an encoding confirmation started before the write", async () => {
    const write = deferred<void>();
    const confirmation = deferred<boolean>();
    mocks.write.mockReturnValue(write.promise);
    mocks.ask.mockReturnValueOnce(confirmation.promise);
    mounted = await mountEditor();
    editDocument("changed");
    await flush();
    const select = mounted.container.querySelector<HTMLSelectElement>("select");
    if (!select) throw new Error("Encoding select not found");
    setSelectValue(select, "gbk");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledOnce());

    await startSave(mounted.container);
    await resolveInAct(confirmation, true);
    expect(mocks.read).toHaveBeenCalledOnce();
    expect(editors.views).toHaveLength(1);
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringMatching(/保存进行中/));

    await resolveInAct(write, undefined);
  });
});
