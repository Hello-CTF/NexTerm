/** @vitest-environment jsdom */
// The copy mode overlay: the status hint appears only while copy mode is active, and the search prompt runs the search
// on Enter and closes on Escape.
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Terminal } from "@xterm/xterm";

vi.mock("../../ipc/env", () => ({ WEB: false }));

import { createCopyMode, type CopyModeController } from "../../features/terminal/copyMode";
import { CopyModeOverlay } from "../../features/terminal/CopyModeOverlay";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let term: Terminal | null = null;
let controller: CopyModeController | null = null;

beforeEach(() => {
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
  container = document.createElement("div");
  document.body.append(container);
  const host = document.createElement("div");
  container.append(host);
  term = new Terminal({ cols: 30, rows: 5 });
  term.open(host);
  controller = createCopyMode(term, host, { copy: async () => {} });
  root = createRoot(container.appendChild(document.createElement("div")));
});

afterEach(async () => {
  await act(async () => root?.unmount());
  controller?.dispose();
  term?.dispose();
  container?.remove();
  root = null;
  container = null;
  vi.unstubAllGlobals();
});

async function write(data: string) {
  await new Promise<void>((resolve) => term!.write(data, resolve));
}

function keydown(target: HTMLElement, key: string) {
  target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
}

describe("CopyModeOverlay", () => {
  it("shows the hint only while copy mode is active", async () => {
    await act(async () => root!.render(createElement(CopyModeOverlay, { controller: controller!, term: term! })));
    expect(container!.querySelector("[data-testid=copy-mode]")).toBeNull();
    await act(async () => controller!.enter());
    expect(container!.querySelector("[role=status]")?.textContent).toBe("复制模式 · v 选择 · y 复制 · Esc 退出");
    await act(async () => controller!.exit());
    expect(container!.querySelector("[data-testid=copy-mode]")).toBeNull();
  });

  it("runs a search from the prompt and closes it", async () => {
    await write("one\r\ntwo needle\r\n");
    await act(async () => root!.render(createElement(CopyModeOverlay, { controller: controller!, term: term! })));
    await act(async () => controller!.enter());
    await act(async () => keydown(term!.textarea!, "/"));
    const input = container!.querySelector<HTMLInputElement>("input")!;
    expect(input).not.toBeNull();
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "needle");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => keydown(input, "Enter"));
    expect(container!.querySelector("input")).toBeNull();
    expect(controller!.state().cursor).toEqual({ x: 4, y: 1 });

    await act(async () => keydown(term!.textarea!, "?"));
    await act(async () => keydown(container!.querySelector<HTMLInputElement>("input")!, "Escape"));
    expect(container!.querySelector("input")).toBeNull();
    expect(controller!.isActive()).toBe(true);
  });
});
