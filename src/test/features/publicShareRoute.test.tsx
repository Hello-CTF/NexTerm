/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { mount } from "./reactTestUtils";

const terminalSpies = vi.hoisted(() => ({
  instances: [] as Array<{
    options: { disableStdin?: boolean };
    writes: Uint8Array[];
    disposed: boolean;
    onDataCallback: ((data: string) => void) | null;
  }>,
}));

// The lifecycle tests assert the create-once/live-readOnly/dispose contract, not
// terminal emulation, so the real canvas/WebGL stack is replaced with no-op
// doubles.
vi.mock("@xterm/xterm", async () => {
  const m = await import("./xtermMock");
  class TrackingTerminal extends m.FakeTerminal {
    readonly writes: Uint8Array[] = [];
    disposed = false;
    onDataCallback: ((data: string) => void) | null = null;
    override onData(callback: (data: string) => void) {
      this.onDataCallback = callback;
      return { dispose() {} };
    }
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

  it("never answers terminal queries for the host, but passes the guest's own reply-shaped keys", () => {
    const onInput = vi.fn();
    const view = mount(createElement(PublicShareTerminal, { onInput, readOnly: false }));
    const terminal = terminalSpies.instances[0];
    act(() => terminal.onDataCallback?.("\x1b[?1;2c"));
    act(() => terminal.onDataCallback?.("\x1b[3;7R"));
    act(() => terminal.onDataCallback?.("ls\r"));
    const host = view.container.querySelector("div") as HTMLElement;
    const textarea = document.createElement("textarea");
    host.appendChild(textarea);
    textarea.addEventListener("keydown", () => terminal.onDataCallback?.("\x1b[1;5R"));
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", ctrlKey: true, bubbles: true }));
    });
    expect(onInput.mock.calls.map(([data]) => data)).toEqual(["ls\r", "\x1b[1;5R"]);
    view.unmount();
  });
});
