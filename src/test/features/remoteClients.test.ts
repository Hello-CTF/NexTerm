/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  rfbs: [] as Array<{ emit: (type: string, detail?: unknown) => void; disconnected: boolean }>,
  uiConnect: vi.fn(),
  uiShutdown: vi.fn(),
  holdConnect: { release: null as null | (() => void) },
}));

vi.mock("../../features/remote/remoteSocket", () => ({
  openVncChannel: vi.fn(async () => ({ close: vi.fn() })),
  prepareRdpTransport: vi.fn(async () => undefined),
  remoteProxyAddress: () => "nxrd://remotedesktop",
}));
vi.mock("@novnc/novnc", () => ({
  default: class FakeRfb extends EventTarget {
    viewOnly = false;
    disconnected = false;
    constructor() {
      super();
      mocks.rfbs.push({ emit: (type: string, detail?: unknown) => this.dispatchEvent(new CustomEvent(type, { detail })), get disconnected() { return false; } } as never);
    }
    disconnect() {
      this.disconnected = true;
    }
    focus() {}
    blur() {}
  },
}));
vi.mock("@devolutions/iron-remote-desktop", () => ({}));
vi.mock("@devolutions/iron-remote-desktop-rdp", () => ({
  init: vi.fn(async () => undefined),
  Backend: {},
  displayControl: vi.fn(() => ({})),
}));

import { VncClient } from "../../features/remote/VncClient";
import { RdpClient } from "../../features/remote/RdpClient";
import { classifyFailure, mayAutoReconnect } from "../../features/remote/connectFlow";
import type { ClientCallbacks, ClientOptions } from "../../features/remote/remoteTypes";

const prepared = { ticket: "t", protocol: "vnc", assetId: "a", name: "n", host: "h", port: 1, username: "", domain: "", promptPassword: false, hasPassword: true, expiresAtMs: 1 };
const options = (patch: Partial<ClientOptions> = {}): ClientOptions => ({ prepared, secret: null, viewOnly: false, clipboard: true, initialSize: { width: 800, height: 600 }, ...patch });
const callbacks = (): ClientCallbacks => ({ onConnected: vi.fn(), onDisconnected: vi.fn() });

class FakeObserver {
  observe() {}
  disconnect() {}
}
beforeEach(() => {
  vi.stubGlobal("ResizeObserver", FakeObserver);
});

afterEach(() => {
  mocks.rfbs.length = 0;
  vi.clearAllMocks();
  document.body.replaceChildren();
});

describe("VncClient drops", () => {
  async function connected() {
    const cb = callbacks();
    const client = new VncClient(document.createElement("div"), options(), cb);
    const pending = client.connect();
    await vi.waitFor(() => expect(mocks.rfbs).toHaveLength(1));
    mocks.rfbs[0].emit("connect");
    await pending;
    return { client, cb };
  }

  it("treats a close it did not ask for as a drop even though noVNC calls it clean", async () => {
    const { cb } = await connected();
    mocks.rfbs[0].emit("disconnect", { clean: true });
    const failure = vi.mocked(cb.onDisconnected).mock.calls[0][0]!;
    expect(failure).toMatchObject({ clean: false, wasConnected: true, stage: "closed" });
    const classified = classifyFailure(failure);
    expect(classified.kind).toBe("retryable");
    expect(mayAutoReconnect(classified, { promptPassword: false, recent: 0 })).toBe(true);
  });

  it("reports nothing for a disconnect the client itself asked for", async () => {
    const { client, cb } = await connected();
    client.disconnect();
    mocks.rfbs[0].emit("disconnect", { clean: true });
    expect(cb.onDisconnected).not.toHaveBeenCalled();
  });

  it("reports a failure before connect through the rejected connect() only", async () => {
    const cb = callbacks();
    const client = new VncClient(document.createElement("div"), options(), cb);
    const pending = client.connect();
    await vi.waitFor(() => expect(mocks.rfbs).toHaveLength(1));
    mocks.rfbs[0].emit("securityfailure", { reason: "VNC 认证失败" });
    mocks.rfbs[0].emit("disconnect", { clean: false });
    await expect(pending).rejects.toMatchObject({ failure: { stage: "security" } });
    expect(cb.onDisconnected).not.toHaveBeenCalled();
  });
});

let current: { ui: unknown; canvas: HTMLCanvasElement } | null = null;

describe("RdpClient", () => {
  function defineComponent(onSession?: () => void) {
    const canvas = document.createElement("canvas");
    canvas.width = 800;
    canvas.height = 600;
    const ui = {
      setVisibility: vi.fn(), setScale: vi.fn(), setEnableClipboard: vi.fn(), setEnableAutoClipboard: vi.fn(), onWarningCallback: vi.fn(),
      setKeyboardUnicodeMode: vi.fn(), resize: vi.fn(), shutdown: mocks.uiShutdown,
      configBuilder: () => {
        const builder: Record<string, unknown> = {};
        for (const name of ["withUsername", "withPassword", "withDestination", "withProxyAddress", "withAuthToken", "withServerDomain", "withExtension", "withDesktopSize"]) {
          builder[name] = () => builder;
        }
        builder.build = () => ({});
        return builder;
      },
      connect: mocks.uiConnect.mockImplementation(async () => {
        onSession?.();
        return { run: () => new Promise(() => undefined) };
      }),
    };
    current = { ui, canvas };
    if (!customElements.get("iron-remote-desktop")) {
      customElements.define(
        "iron-remote-desktop",
        class extends HTMLElement {
          connectedCallback() {
            const root = this.attachShadow({ mode: "open" });
            root.append(current!.canvas);
            const detail = { irgUserInteraction: current!.ui };
            queueMicrotask(() => this.dispatchEvent(new CustomEvent("ready", { detail })));
          }
        },
      );
    }
    return canvas;
  }

  function attached(callbacksOverride = callbacks()): RdpClient {
    const stage = document.createElement("div");
    document.body.append(stage);
    return new RdpClient(stage, rdpOptions(), callbacksOverride);
  }

  const rdpOptions = () => options({ prepared: { ...prepared, protocol: "rdp" }, secret: { username: "u", domain: "", password: "pw" } });

  it("lets go of inputs still down on the remote when the keyboard is released or blurred", async () => {
    const canvas = defineComponent();
    const client = attached();
    await client.connect();
    const leave = vi.fn();
    canvas.addEventListener("mouseleave", leave);
    client.releaseAllKeys();
    expect(leave).toHaveBeenCalledTimes(1);
    client.blur();
    expect(leave).toHaveBeenCalledTimes(2);
  });

  it("does not hold on to the password after connecting", async () => {
    defineComponent();
    const client = attached();
    await client.connect();
    expect(JSON.stringify(client["options"])).not.toContain("pw");
    expect(client["secret"]).toBeNull();
  });

  it("does not start the session when it was closed while the component loaded", async () => {
    defineComponent();
    const client = attached();
    const pending = client.connect();
    client.disconnect();
    await expect(pending).rejects.toMatchObject({ failure: { stage: "closed" } });
    expect(mocks.uiConnect).not.toHaveBeenCalled();
  });

  it("drops a session that was closed while the handshake was running", async () => {
    const holder: { client?: RdpClient } = {};
    defineComponent(() => holder.client?.disconnect());
    const client = attached();
    holder.client = client;
    await expect(client.connect()).rejects.toMatchObject({ failure: { stage: "closed" } });
  });
});
