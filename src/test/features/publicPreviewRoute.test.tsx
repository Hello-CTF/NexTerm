/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { flush } from "./reactTestUtils";

const mountedRoots = vi.hoisted(() => [] as Array<{ unmount: () => void }>);

// The route decision under test is main.tsx's token parsing and branch selection; the full
// App bundle (xterm and every feature) is a heavy leaf no case's assertions touch, so it is
// layered out — cold transform per case drops from ~21s to a few seconds without changing
// any route behavior.
vi.mock("../../app/App", () => ({ default: () => null }));

// The share/preview viewers mount the xterm-backed terminal, which dominates the per-case
// mount cost; the overlay chrome each case asserts on is rendered by the viewers themselves,
// so the terminal is layered out the same way. The viewers tolerate a missing terminal
// (terminalRef stays null and writes are optional-chained no-ops).
vi.mock("../../features/sharing/PublicShareTerminal", () => ({ PublicShareTerminal: () => null }));

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

describe("main.tsx public preview route", () => {
  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.stubGlobal("ResizeObserver", class {
      observe() {}
      unobserve() {}
      disconnect() {}
    });
  });

  afterEach(async () => {
    await act(async () => {
      for (const root of mountedRoots.splice(0)) root.unmount();
    });
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("mounts the anonymous preview at /share/preview/{token} without dialing any account channel", async () => {
    await importMainAt("/share/preview/route-token-1");
    expect(document.body.textContent).toContain("NexTerm 预览");
    expect(document.body.textContent).toContain("正在连接预览终端");
    expect(FakeWebSocket.instances.length).toBeGreaterThan(0);
    for (const socket of FakeWebSocket.instances) {
      expect(socket.url).toContain("/share/preview/route-token-1");
    }
    expect(document.body.textContent).not.toContain("route-token-1");
  }, 15000);

  it("ignores malformed preview paths and falls through to the app branch", async () => {
    FakeWebSocket.instances = [];
    await importMainAt("/share/preview/");
    expect(FakeWebSocket.instances.some((socket) => socket.url.includes("/share/preview/"))).toBe(false);
  }, 15000);

  it("keeps the legacy session-share viewer route", async () => {
    await importMainAt("/share/public/legacy-session-token");
    expect(document.body.textContent).toContain("NexTerm 公开分享");
    expect(FakeWebSocket.instances.some((socket) => socket.url.includes("/share/public/legacy-session-token"))).toBe(true);
  }, 15000);

  it("does not mount a viewer for retired device public tokens", async () => {
    await importMainAt("/share/public/device/retired-device-token");
    expect(document.body.textContent).not.toContain("NexTerm 公开分享");
    expect(FakeWebSocket.instances.some((socket) => socket.url.includes("/share/public/device/"))).toBe(false);
  }, 15000);
});
