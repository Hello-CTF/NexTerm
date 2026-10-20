/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount } from "./reactTestUtils";

const mountedRoots = vi.hoisted(() => [] as Array<{ unmount: () => void }>);

const terminalSpies = vi.hoisted(() => ({
  instances: [] as Array<{
    options: { disableStdin?: boolean };
    writes: Uint8Array[];
    disposed: boolean;
  }>,
}));

vi.mock("react-dom/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-dom/client")>();
  return {
    ...actual,
    default: {
      createRoot: (container: Parameters<typeof actual.createRoot>[0]) => {
        const root = actual.createRoot(container);
        mountedRoots.push(root);
        return root;
      },
    },
  };
});

// The route tests assert routing, connection, and rendered text, and the
// lifecycle tests assert the create-once/live-readOnly/dispose contract, not
// terminal emulation, so the real canvas/WebGL stack is replaced with no-op
// doubles.
vi.mock("@xterm/xterm", async () => {
  const m = await import("./xtermMock");
  class TrackingTerminal extends m.FakeTerminal {
    readonly writes: Uint8Array[] = [];
    disposed = false;
    constructor(options: { disableStdin?: boolean }) {
      super(options);
      terminalSpies.instances.push(this);
    }
    override write(bytes: Uint8Array) {
      this.writes.push(bytes);
    }
    override dispose() {
      this.disposed = true;
    }
  }
  return { Terminal: TrackingTerminal };
});
vi.mock("@xterm/addon-fit", async () => {
  const m = await import("./xtermMock");
  return { FitAddon: m.FakeFitAddon };
});
vi.mock("@xterm/addon-webgl", async () => {
  const m = await import("./xtermMock");
  return { WebglAddon: m.FakeWebglAddon };
});
vi.mock("../../features/terminal/selectionAutoCopy", async () => {
  const m = await import("./xtermMock");
  return { createSelectionAutoCopy: m.fakeSelectionAutoCopy };
});

import { PublicShareTerminal, type PublicShareTerminalHandle } from "../../features/sharing/PublicShareTerminal";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  static readonly OPEN = 1;

  readonly url: string;
  binaryType = "blob";
  readyState = FakeWebSocket.OPEN;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  addEventListener() {}
  send() {}
  close() {
    this.readyState = 3;
  }
}

async function importMainAt(pathname: string): Promise<void> {
  window.history.replaceState({}, "", pathname);
  document.body.innerHTML = '<div id="root"></div>';
  vi.resetModules();
  await act(async () => {
    await import("../../main");
    await flush();
  });
}

describe("main.tsx public share route", () => {
  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      unobserve() {}
      disconnect() {}
    });
  });

  afterEach(() => {
    for (const root of mountedRoots.splice(0)) act(() => root.unmount());
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("mounts the anonymous viewer at /share/public/{token} without dialing any account channel", async () => {
    await importMainAt("/share/public/route-token-1");
    expect(document.body.textContent).toContain("NexTerm 公开分享");
    expect(document.body.textContent).toContain("正在连接分享终端");
    expect(FakeWebSocket.instances.length).toBeGreaterThan(0);
    for (const socket of FakeWebSocket.instances) {
      expect(socket.url).toContain("/share/public/route-token-1");
    }
    expect(document.body.textContent).not.toContain("route-token-1");
  }, 15000);

  it("ignores malformed share paths and falls through to the app branch", async () => {
    FakeWebSocket.instances = [];
    await importMainAt("/share/public/");
    expect(FakeWebSocket.instances.some((socket) => socket.url.includes("/share/public/"))).toBe(false);
  }, 15000);
});

describe("PublicShareTerminal lifecycle", () => {
  beforeEach(() => {
    terminalSpies.instances.length = 0;
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      unobserve() {}
      disconnect() {}
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates one terminal, applies readOnly live without rebuilding, and disposes on unmount", () => {
    const onInput = vi.fn();
    const handles: PublicShareTerminalHandle[] = [];
    const onHandle = (handle: PublicShareTerminalHandle) => handles.push(handle);
    const view = mount(createElement(PublicShareTerminal, { onInput, readOnly: false, onHandle }));

    expect(terminalSpies.instances).toHaveLength(1);
    const terminal = terminalSpies.instances[0];
    expect(terminal.options.disableStdin).toBe(false);
    expect(handles).toHaveLength(1);

    act(() => {
      view.root.render(createElement(PublicShareTerminal, { onInput, readOnly: true, onHandle }));
    });

    expect(terminalSpies.instances).toHaveLength(1);
    expect(terminal.options.disableStdin).toBe(true);
    expect(handles).toHaveLength(1);

    handles[0].write(new TextEncoder().encode("ls\r"));
    expect(terminal.writes).toHaveLength(1);

    view.unmount();
    expect(terminal.disposed).toBe(true);
  });
});
