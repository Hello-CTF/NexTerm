import { vi } from "vitest";

export interface FakeTerminal {
  cols: number;
  rows: number;
  options: Record<string, unknown>;
  modes: { bracketedPasteMode: boolean };
  buffer: { active: { baseY: number; viewportY: number } };
  writeLog: string[];
  pendingWrites: (() => void)[];
  handlers: {
    data: ((data: string) => void) | null;
    scroll: (() => void) | null;
    title: ((title: string) => void) | null;
    selection: (() => void) | null;
  };
  drainWrites: (count?: number) => void;
  host: HTMLElement | null;
}

export const harness = {
  terminals: [] as unknown[],
  channel: null as {
    onBytes?: (bytes: Uint8Array) => void;
    onControl?: (control: unknown) => void;
    onReplay?: (control: unknown) => void;
    onClass?: (control: unknown) => void;
    onInputOwner?: (control: unknown) => void;
  } | null,
  channels: [] as { onBytes?: (bytes: Uint8Array) => void; onControl?: (control: unknown) => void }[],
  // When set, the fake terminal answers DA1 and DSR 6 queries through onData
  // at the moment the write that carries them is parsed (its callback runs).
  emitReplies: false,
  reopenCb: null as (() => void) | null,
  feedInput: vi.fn(),
  feedOSC133: vi.fn(),
  blocksClear: vi.fn(),
  attach: vi.fn(),
  attachTab: vi.fn(),
  detach: vi.fn(),
  setVisible: vi.fn(),
  write: vi.fn(),
  pushToast: vi.fn(),
  call: vi.fn(),
  execAttach: vi.fn(),
  openLineTab: vi.fn(),
};

export const xtermModule = () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: Record<string, unknown> = {};
    modes = { bracketedPasteMode: false };
    buffer = { active: { baseY: 0, viewportY: 0 }, onBufferChange: () => ({ dispose: () => {} }) };
    writeLog: string[] = [];
    pendingWrites: (() => void)[] = [];
    handlers: {
      data: ((data: string) => void) | null;
      scroll: (() => void) | null;
      title: ((title: string) => void) | null;
      selection: (() => void) | null;
    } = { data: null, scroll: null, title: null, selection: null };
    constructor() {
      harness.terminals.push(this);
    }
    parser = { registerOscHandler: () => ({ dispose: () => undefined }) };
    host: HTMLElement | null = null;
    open(host: HTMLElement) {
      this.host = host;
    }
    loadAddon() {}
    attachCustomKeyEventHandler() {}
    write(data: string | Uint8Array, callback?: () => void) {
      this.writeLog.push(typeof data === "string" ? data : new TextDecoder().decode(data));
      if (harness.emitReplies) {
        const text = typeof data === "string" ? data : new TextDecoder().decode(data);
        this.pendingWrites.push(() => {
          for (const query of text.match(/\x1b\[(?:c|6n)/g) ?? []) {
            this.handlers.data?.(query === "\x1b[c" ? "\x1b[?1;2c" : "\x1b[1;1R");
          }
          for (const title of text.matchAll(/\x1b\]0;([^\x07]*)\x07/g)) this.handlers.title?.(title[1]);
          callback?.();
        });
        return;
      }
      if (callback) this.pendingWrites.push(callback);
    }
    drainWrites(count?: number) {
      if (count === undefined) {
        let guard = 0;
        while (this.pendingWrites.length > 0 && guard++ < 10_000) this.pendingWrites.shift()!();
        return;
      }
      for (let i = 0; i < count && this.pendingWrites.length > 0; i += 1) {
        this.pendingWrites.shift()!();
      }
    }
    clear() {}
    clearSelection() {}
    dispose() {}
    focus() {}
    scrollToBottom() {}
    selectAll() {}
    getSelection() {
      return "";
    }
    onData(callback: (data: string) => void) {
      this.handlers.data = callback;
      return { dispose: () => undefined };
    }
    onScroll(callback: () => void) {
      this.handlers.scroll = callback;
      return { dispose: () => undefined };
    }
    onTitleChange(callback: (title: string) => void) {
      this.handlers.title = callback;
      return { dispose: () => undefined };
    }
    onSelectionChange(callback: () => void) {
      this.handlers.selection = callback;
      return { dispose: () => undefined };
    }
  },
});

export const webglModule = () => ({ WebglAddon: class {} });

export const searchModule = () => ({
  SearchAddon: class {
    findNext() {}
    findPrevious() {}
  },
});

export const webLinksModule = () => ({ WebLinksAddon: class {} });

export const commandBlocksModule = () => ({
  CommandBlockManager: class {
    feedInput = harness.feedInput;
    feedOSC133 = harness.feedOSC133;
    getBlockText() {
      return "";
    }
    scrollTo() {}
    navigate() {
      return null;
    }
    clear() {
      harness.blocksClear();
    }
    dispose() {}
  },
});

export const terminalGeometryModule = () => ({
  measureTerminalGeometry: () => ({
    viewport: { widthPx: 640, heightPx: 480 },
    metrics: { widthPx: 8, heightPx: 16 },
    grid: { cols: 80, rows: 30 },
  }),
  resizeTerminalToGridPreservingSelection: () => undefined,
});

export const gridRuntimeModule = () => ({
  productionGridRuntime: {
    resize: vi.fn().mockResolvedValue(undefined),
    flush: vi.fn().mockResolvedValue(undefined),
  },
});

export const commandsModule = () => ({
  terminalApi: {
    attach: harness.attach,
    attachTab: harness.attachTab,
    detach: harness.detach,
    setVisible: harness.setVisible,
    write: harness.write,
  },
  dockerApi: { execAttach: harness.execAttach },
  sessionApi: { openLineTab: harness.openLineTab },
  call: harness.call,
});

export const eventsModule = () => ({
  channelIdOf: () => "channel-1",
  createBinaryChannel: (
    onBytes: (bytes: Uint8Array) => void,
    onControl?: (control: unknown) => void,
    onReplay?: (control: unknown) => void,
    onClass?: (control: unknown) => void,
    onInputOwner?: (control: unknown) => void,
  ) => {
    const entry = { onBytes, onControl, onReplay, onClass, onInputOwner };
    harness.channel = entry;
    harness.channels.push(entry);
    return {};
  },
  disposeChannel: vi.fn(),
  sendChannelInput: (
    _channel: unknown,
    _tabId: string,
    data: Uint8Array,
    http: (data: Uint8Array) => Promise<unknown>,
  ) => void http(data),
  onChannelReopen: (_channel: unknown, callback: () => void) => {
    harness.reopenCb = callback;
    return () => {
      harness.reopenCb = null;
    };
  },
});

export const envModule = () => ({ WEB: false, clientId: () => "me" });

export const keybindingsModule = () => ({ matchAppKeybinding: () => null });

export const storeModule = () => ({
  useUi: {
    getState: () => ({
      sessions: [],
      pushToast: harness.pushToast,
      registerHelperOperationIntent: (
        _tabId: string,
        _kind: "deploy" | "reattach",
        _originalTabId?: string,
      ) => undefined,
    }),
  },
});

export const preferencesModule = () => ({
  getAppearancePrefs: () => ({ terminalFontSize: 13 }),
  getInputPrefs: () => ({ selectionAutoCopy: false }),
  terminalDefaultUsesAccount: () => false,
  getTerminalPrefs: () => ({ closeAction: "ask", defaultMode: "durable", gpuAcceleration: "auto" }),
  subscribeTerminalPrefs: () => () => undefined,
  waitAccountPreferences: () => null,
  getResolvedTerminalTheme: () => "dark",
  subscribeAppearancePrefs: () => () => undefined,
});

export const clipboardModule = () => ({ copyText: vi.fn().mockResolvedValue(undefined) });
