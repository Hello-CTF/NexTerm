/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { deferred, flush, mountWithClient, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  read: vi.fn(),
  readRange: vi.fn(),
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
  destroy: () => void;
}
const editors = vi.hoisted(() => ({ views: [] as FakeEditorView[], destroyed: 0 }));

vi.mock("../../ipc/commands", () => ({
  fsApi: { read: mocks.read, readRange: mocks.readRange, write: vi.fn() },
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
    dispatch({
      changes,
    }: {
      changes?: { from: number; to: number; insert: string };
      effects?: unknown;
    }) {
      if (!changes) return;
      const current = this.state.doc.toString() as string;
      const next = `${current.slice(0, changes.from)}${changes.insert}${current.slice(changes.to)}`;
      this.state = { ...this.state, doc: { toString: () => next, length: next.length } };
      this.listeners.forEach((listener) => listener({ docChanged: true, state: this.state }));
    }
    destroy() {
      editors.destroyed += 1;
    }
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

const SESSION = "s-winrm";

function docResult(text: string) {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  bytes.forEach((byte) => (binary += String.fromCharCode(byte)));
  return { path: "/A.txt", size: bytes.length, contentBase64: btoa(binary) };
}

function seedWinRmSession(): void {
  useUi.setState({
    pushToast: mocks.toast,
    sessions: [
      {
        id: SESSION,
        assetId: "asset-1",
        name: "windows-host",
        kind: "winrm",
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
    ],
  });
}

function removeSession(): void {
  act(() => {
    useUi.setState({ sessions: [] });
  });
}

async function mountEditor(onClose: () => void): Promise<MountedView> {
  const mounted = mountWithClient(
    createElement(FileEditor, { sessionId: SESSION, path: "/A.txt", onClose }),
  );
  await flush();
  return mounted;
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  editors.views = [];
  editors.destroyed = 0;
  document.body.replaceChildren();
  mocks.ask.mockResolvedValue(true);
  seedWinRmSession();
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("FileEditor WinRM session removal", () => {
  it("does not close the editor when the session disappears mid-read", async () => {
    const read = deferred<ReturnType<typeof docResult>>();
    mocks.read.mockReturnValue(read.promise);
    mocks.readRange.mockResolvedValue({ path: "/A.txt", offset: 0, size: 8, contentBase64: "" });
    const onClose = vi.fn();
    mounted = await mountEditor(onClose);
    await waitForRendered(() => expect(mocks.read).toHaveBeenCalledOnce());

    removeSession();
    await flush();

    expect(mocks.read).toHaveBeenCalledOnce();
    expect(onClose).not.toHaveBeenCalled();
    expect(editors.destroyed).toBe(0);

    await act(async () => {
      read.resolve(docResult("original"));
      await read.promise;
    });
    await flush();
    expect(onClose).not.toHaveBeenCalled();
    expect(editors.views).toHaveLength(1);
    expect(editors.views[0]?.state.doc.toString()).toBe("original");
  }, 15000);

  it("does not reload or close an already-loaded editor when the session disappears", async () => {
    mocks.read.mockResolvedValue(docResult("original"));
    mocks.readRange.mockResolvedValue({ path: "/A.txt", offset: 0, size: 8, contentBase64: "" });
    const onClose = vi.fn();
    mounted = await mountEditor(onClose);
    await waitForRendered(() => expect(editors.views).toHaveLength(1));

    removeSession();
    await flush();

    expect(mocks.read).toHaveBeenCalledOnce();
    expect(onClose).not.toHaveBeenCalled();
    expect(editors.destroyed).toBe(0);
    expect(editors.views).toHaveLength(1);
    expect(editors.views[0]?.state.doc.toString()).toBe("original");
  }, 15000);
});
