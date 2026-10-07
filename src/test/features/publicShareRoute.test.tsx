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

describe("main.tsx public share route", () => {
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

  it("mounts the anonymous viewer at /share/public/{token} without dialing any account channel", async () => {
    await importMainAt("/share/public/route-token-1");
    expect(document.body.textContent).toContain("NexTerm 公开分享");
    expect(document.body.textContent).toContain("正在连接分享终端");
    expect(FakeWebSocket.instances.length).toBeGreaterThan(0);
    for (const socket of FakeWebSocket.instances) {
      expect(socket.url).toContain("/share/public/route-token-1");
    }
    expect(document.body.textContent).not.toContain("route-token-1");
  });

  it("ignores malformed share paths and falls through to the app branch", async () => {
    FakeWebSocket.instances = [];
    await importMainAt("/share/public/");
    expect(FakeWebSocket.instances.some((socket) => socket.url.includes("/share/public/"))).toBe(false);
  });
});
