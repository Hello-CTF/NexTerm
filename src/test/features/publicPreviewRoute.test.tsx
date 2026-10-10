/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush } from "./reactTestUtils";

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
  await import("../../main");
  await flush();
}

describe("main.tsx public preview route", () => {
  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      disconnect() {}
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("mounts the anonymous preview at /share/preview/{token} without dialing any account channel", async () => {
    await importMainAt("/share/preview/route-token-1");
    expect(document.body.textContent).toContain("NexTerm 只读预览");
    expect(document.body.textContent).toContain("正在连接预览终端");
    expect(FakeWebSocket.instances.length).toBeGreaterThan(0);
    for (const socket of FakeWebSocket.instances) {
      expect(socket.url).toContain("/share/preview/route-token-1");
    }
    expect(document.body.textContent).not.toContain("route-token-1");
  });

  it("ignores malformed preview paths and falls through to the app branch", async () => {
    FakeWebSocket.instances = [];
    await importMainAt("/share/preview/");
    expect(FakeWebSocket.instances.some((socket) => socket.url.includes("/share/preview/"))).toBe(false);
  });
});
