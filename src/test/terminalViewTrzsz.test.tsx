/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";
import { act } from "react";
import { harness } from "./lib/terminalViewHarness";
import { attachOk, enc, feedBytes, feedControl, installTerminalViewHooks, show, term } from "./lib/terminalViewDriver";
import { isTrzszTransferActive } from "../features/terminal/trzsz/trzszActivity";

const trzsz = vi.hoisted(() => ({
  filters: [] as {
    transferring: boolean;
    received: string[];
    input: string[];
    stopped: number;
  }[],
}));

vi.mock("@xterm/xterm", async () => (await import("./lib/terminalViewHarness")).xtermModule());
vi.mock("@xterm/addon-webgl", async () => (await import("./lib/terminalViewHarness")).webglModule());
vi.mock("@xterm/addon-search", async () => (await import("./lib/terminalViewHarness")).searchModule());
vi.mock("../features/terminal/commandBlocks", async () => (await import("./lib/terminalViewHarness")).commandBlocksModule());
vi.mock("../features/terminal/terminalGeometry", async () => (await import("./lib/terminalViewHarness")).terminalGeometryModule());
vi.mock("../features/terminal/gridRuntimeAdapter", async () => (await import("./lib/terminalViewHarness")).gridRuntimeModule());
vi.mock("../ipc/commands", async () => (await import("./lib/terminalViewHarness")).commandsModule());
vi.mock("../ipc/events", async () => (await import("./lib/terminalViewHarness")).eventsModule());
vi.mock("../ipc/env", async () => (await import("./lib/terminalViewHarness")).envModule());
vi.mock("../app/keybindings", async () => (await import("./lib/terminalViewHarness")).keybindingsModule());
vi.mock("../app/store", async () => (await import("./lib/terminalViewHarness")).storeModule());
vi.mock("../app/preferences", async () => (await import("./lib/terminalViewHarness")).preferencesModule());
vi.mock("../ui/clipboard", async () => (await import("./lib/terminalViewHarness")).clipboardModule());
vi.mock("../features/terminal/trzsz/trzszPickers", () => ({ installDefaultTrzszPickers: () => undefined }));
// A stand-in for trzsz.js: a request starts a transfer that takes all output
// and input until Ctrl+C.
vi.mock("trzsz", () => ({
  TrzszFilter: class {
    state = { transferring: false, received: [] as string[], input: [] as string[], stopped: 0 };
    constructor(readonly options: { writeToTerminal: (out: unknown) => void; sendToServer: (input: string) => void }) {
      trzsz.filters.push(this.state);
    }
    processServerOutput(output: Uint8Array) {
      const text = new TextDecoder().decode(output);
      if (this.state.transferring) this.state.received.push(text);
      else {
        if (text.includes("::TRZSZ:TRANSFER:")) {
          this.state.transferring = true;
          this.options.sendToServer("#ACT:accept\n");
        }
        this.options.writeToTerminal(output);
      }
    }
    processTerminalInput(input: string) {
      this.state.input.push(input);
      if (input === "\u0003") this.stopTransferringFiles();
    }
    setTerminalColumns() {}
    isTransferringFiles() {
      return this.state.transferring;
    }
    stopTransferringFiles() {
      this.state.stopped += 1;
      this.state.transferring = false;
    }
  },
}));

installTerminalViewHooks();

const REQUEST = "::TRZSZ:TRANSFER:S:1.1.6:1234567890123\r\n";

function upstream(): string[] {
  return harness.write.mock.calls.map(([, bytes]) => new TextDecoder().decode(bytes as Uint8Array));
}

async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

// The transfer state is polled; the overlay it toggles is a React update.
async function waitInAct(condition: () => boolean) {
  for (let i = 0; i < 100 && !condition(); i += 1) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
  }
  expect(condition()).toBe(true);
}

describe("XtermView trzsz transfers", () => {
  it("never starts a transfer from replayed output", async () => {
    harness.terminalPrefs = { trzsz: true };
    await show();
    await attachOk();
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "e1",
      streamSeq: 0,
      expired: true,
      classes: [{ from: 0, to: enc.encode(REQUEST).length, kind: "replay" }],
    });
    feedBytes(REQUEST);
    await settle();
    expect(trzsz.filters).toHaveLength(0);
    expect(term().writeLog.join("")).toContain("TRZSZ");
  });

  it("does nothing while the setting is off", async () => {
    harness.terminalPrefs = { trzsz: false };
    trzsz.filters.length = 0;
    await show();
    await attachOk();
    feedBytes(REQUEST);
    await settle();
    expect(trzsz.filters).toHaveLength(0);
  });

  it("runs a live transfer: protocol output skips xterm, typing reaches neither the host nor broadcast", async () => {
    harness.terminalPrefs = { trzsz: true };
    trzsz.filters.length = 0;
    const onData = vi.fn();
    await show({ onData });
    await attachOk();
    const t = term();
    feedBytes(REQUEST);
    await settle();
    expect(trzsz.filters).toHaveLength(1);
    // The protocol reply goes straight to this tab, never through onData (broadcast).
    expect(upstream()).toEqual(["#ACT:accept\n"]);
    expect(onData).not.toHaveBeenCalled();
    await waitInAct(() => isTrzszTransferActive("kernel-1") && document.body.textContent!.includes("trzsz"));

    feedBytes("#DATA:secret-payload\n");
    act(() => t.drainWrites());
    expect(t.writeLog.join("")).not.toContain("secret-payload");
    expect(trzsz.filters[0].received).toEqual(["#DATA:secret-payload\n"]);

    act(() => t.handlers.data?.("ls\r"));
    expect(onData).not.toHaveBeenCalled();
    expect(trzsz.filters[0].input).toEqual(["ls\r"]);

    act(() => t.handlers.data?.("\u0003"));
    expect(trzsz.filters[0].stopped).toBe(1);
    await waitInAct(() => !isTrzszTransferActive("kernel-1"));

    // After the transfer, output and input flow as before.
    feedBytes("prompt$ ");
    act(() => t.drainWrites());
    expect(t.writeLog.join("")).toContain("prompt$ ");
    act(() => t.handlers.data?.("x"));
    expect(onData).toHaveBeenCalledWith("x");
  });
});
