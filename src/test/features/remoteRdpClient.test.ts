/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  prepareRdpTransport: vi.fn(),
  init: vi.fn(),
  copyText: vi.fn(async (_text: string) => undefined),
  readText: vi.fn(async () => ""),
  nativeWebKit: false,
  web: true,
  pickLocalFolder: vi.fn(async (): Promise<string | null> => null),
  writeFile: vi.fn(async (path: string, _contentBase64: string) => ({ path, bytes: 0, exists: false })),
  writeClipboardImage: vi.fn(async (_pngBase64: string) => ({ bytes: 0 })),
}));

// A stand-in for the IronRDP wasm module: the session builder records the clipboard callback it is given and the
// session records every clipboard it is handed, the way the real module wires its clipboard channel.
const wasm = vi.hoisted(() => {
  class ClipboardData {
    entries: Array<{ mime: string; value: string | Uint8Array }> = [];
    addText(mime: string, text: string) {
      this.entries.push({ mime, value: text });
    }
    addBinary(mime: string, bytes: Uint8Array) {
      this.entries.push({ mime, value: bytes });
    }
    items() {
      return this.entries.map((entry) => ({ mimeType: () => entry.mime, value: () => entry.value }));
    }
    isEmpty() {
      return this.entries.length === 0;
    }
  }
  // The file channel: records its listeners and serves each download from `downloads`.
  class FileTransfer {
    static last: FileTransfer | null = null;
    handlers = new Map<string, (files: unknown) => void>();
    downloads = new Map<number, Blob>();
    constructor() {
      FileTransfer.last = this;
    }
    getBuilderExtensions() {
      return [];
    }
    setSession() {}
    uploadFiles() {}
    on(event: string, handler: (files: unknown) => void) {
      this.handlers.set(event, handler);
    }
    downloadFile(_file: unknown, index: number) {
      const blob = this.downloads.get(index);
      return { transferId: index, completion: blob ? Promise.resolve(blob) : Promise.reject(new Error("transfer failed")) };
    }
  }
  class InputTransaction {
    events: unknown[] = [];
    addEvent(event: unknown) {
      this.events.push(event);
    }
  }
  // DeviceEvent stands in for the wasm events, which carry the whole character; IronRDP encodes it as UTF-16.
  const DeviceEvent = {
    unicodePressed: (unicode: string) => `down:${unicode}`,
    unicodeReleased: (unicode: string) => `up:${unicode}`,
  };
  const state = {
    remoteCallback: null as ((data: ClipboardData) => void) | null,
    pastes: [] as ClipboardData[],
    inputs: [] as InputTransaction[],
    applyInputs: true,
    ClipboardData,
    FileTransfer,
    fileChannel: false,
  };
  class SessionBuilder {
    remoteClipboardChangedCallback(cb: (data: ClipboardData) => void) {
      state.remoteCallback = cb;
      return this;
    }
    forceClipboardUpdateCallback() {
      return this;
    }
    extension() {
      return this;
    }
    async connect() {
      return {
        onClipboardPaste: async (data: ClipboardData) => {
          state.pastes.push(data);
        },
        ...(state.applyInputs ? { applyInputs: (transaction: InputTransaction) => state.inputs.push(transaction) } : {}),
      };
    }
  }
  return { state, Backend: { SessionBuilder, ClipboardData, InputTransaction, DeviceEvent }, FileTransfer };
});

vi.mock("../../features/remote/remoteSocket", () => ({
  openVncChannel: vi.fn(),
  prepareRdpTransport: mocks.prepareRdpTransport,
  remoteProxyAddress: () => "nxrd://remotedesktop",
}));
vi.mock("@devolutions/iron-remote-desktop", () => ({}));
vi.mock("../../ui/clipboard", () => ({ copyText: mocks.copyText, readText: mocks.readText, isNativeWebKit: () => mocks.nativeWebKit }));
vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return {
    ...actual,
    get WEB() {
      return mocks.web;
    },
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => ({ ...(await importOriginal<object>()), pickLocalFolder: mocks.pickLocalFolder }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, localFileApi: { ...actual.localFileApi, write: mocks.writeFile, writeClipboardImage: mocks.writeClipboardImage } };
});
vi.mock("@devolutions/iron-remote-desktop-rdp", () => ({
  init: mocks.init,
  Backend: wasm.Backend,
  get RdpFileTransferProvider() {
    return wasm.state.fileChannel ? wasm.FileTransfer : undefined;
  },
  displayControl: vi.fn((enabled: boolean) => ({ displayControl: enabled })),
}));

import { RdpClient } from "../../features/remote/RdpClient";
import { classifyFailure } from "../../features/remote/connectFlow";
import { RemoteEngineError, type ClientCallbacks, type ClientOptions } from "../../features/remote/remoteTypes";

interface Scenario {
  canvas: HTMLCanvasElement;
  ready: boolean;
  builderCalls: Array<[string, unknown]>;
  ui: Record<string, ReturnType<typeof vi.fn>>;
  connect: () => Promise<{ run: () => Promise<{ reason: () => string }> }>;
}

let scenario: Scenario;

function newScenario(): Scenario {
  const canvas = document.createElement("canvas");
  canvas.width = 1000;
  canvas.height = 500;
  const builderCalls: Array<[string, unknown]> = [];
  const builder: Record<string, unknown> = {};
  for (const name of ["withUsername", "withPassword", "withDestination", "withProxyAddress", "withAuthToken", "withServerDomain", "withExtension", "withDesktopSize"]) {
    builder[name] = (value: unknown) => {
      builderCalls.push([name, value]);
      return builder;
    };
  }
  builder.build = () => ({ built: true });
  const ui: Scenario["ui"] = {};
  for (const name of [
    "setVisibility",
    "setScale",
    "setEnableClipboard",
    "setEnableAutoClipboard",
    "onWarningCallback",
    "setKeyboardUnicodeMode",
    "resize",
    "shutdown",
    "sendClipboardData",
    "saveRemoteClipboardData",
    "onClipboardRemoteUpdateCallback",
  ]) {
    ui[name] = vi.fn();
  }
  ui.configBuilder = vi.fn(() => builder);
  const result: Scenario = {
    canvas,
    ready: true,
    builderCalls,
    ui,
    connect: async () => ({ run: () => new Promise(() => undefined) }),
  };
  ui.connect = vi.fn(async () => result.connect());
  return result;
}

if (!customElements.get("iron-remote-desktop")) {
  customElements.define(
    "iron-remote-desktop",
    class extends HTMLElement {
      connectedCallback() {
        const root = this.attachShadow({ mode: "open" });
        const viewer = document.createElement("div");
        viewer.className = "screen-viewer";
        viewer.style.cssText = "width:333px;height:222px;min-width:333px;min-height:222px";
        viewer.append(scenario.canvas);
        root.append(viewer);
        if (!scenario.ready) return;
        const detail = { irgUserInteraction: scenario.ui };
        queueMicrotask(() => this.dispatchEvent(new CustomEvent("ready", { detail })));
      }
    },
  );
}

const prepared = { ticket: "tick", protocol: "rdp", assetId: "a", name: "n", host: "10.0.0.5", port: 3389, username: "stored-user", domain: "STORED", promptPassword: false, hasPassword: true, expiresAtMs: 1 };
const secret = { username: "alice", domain: "CORP", password: "s3cret" };
const options = (patch: Partial<ClientOptions> = {}): ClientOptions => ({
  prepared: prepared as ClientOptions["prepared"],
  secret,
  viewOnly: false,
  clipboard: true,
  clipboardAutoSync: true,
  initialSize: { width: 1280, height: 720 },
  ...patch,
});
const callbacks = () => ({ onConnected: vi.fn(), onDisconnected: vi.fn(), onDesktopSize: vi.fn() }) satisfies ClientCallbacks;

let stage: HTMLElement;

async function failureOf(pending: Promise<void>): Promise<RemoteEngineError> {
  const reason = await pending.then(
    () => null,
    (error: unknown) => error,
  );
  if (!(reason instanceof RemoteEngineError)) throw new Error("connect did not fail with an engine error");
  return reason;
}

function setStageSize(width: number, height: number) {
  Object.defineProperty(stage, "clientWidth", { configurable: true, value: width });
  Object.defineProperty(stage, "clientHeight", { configurable: true, value: height });
}

class FakeObserver {
  static instances: FakeObserver[] = [];
  disconnected = false;
  constructor(readonly callback: () => void) {
    FakeObserver.instances.push(this);
  }
  observe() {}
  disconnect() {
    this.disconnected = true;
  }
}

// jsdom's real MutationObserver delivers its records as a microtask.
async function flushMutations() {
  await Promise.resolve();
  await Promise.resolve();
}

function setCanvasSize(width: number, height: number) {
  scenario.canvas.width = width;
  scenario.canvas.height = height;
}

beforeEach(() => {
  scenario = newScenario();
  stage = document.createElement("div");
  document.body.append(stage);
  FakeObserver.instances = [];
  vi.stubGlobal("ResizeObserver", FakeObserver);
  mocks.prepareRdpTransport.mockReset();
  mocks.init.mockReset();
  mocks.prepareRdpTransport.mockResolvedValue(undefined);
  mocks.init.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.replaceChildren();
});

async function connected(opts = options(), cb = callbacks()) {
  const client = new RdpClient(stage, opts, cb);
  await client.connect();
  return { client, cb };
}

// Builds the session through the module, the way the component does, so the client holds the wasm session.
function connectThroughModule() {
  wasm.state.inputs = [];
  scenario.connect = async () => {
    const element = document.querySelector("iron-remote-desktop") as HTMLElement & { module: { SessionBuilder: new () => { connect(): Promise<unknown> } } };
    await new element.module.SessionBuilder().connect();
    return { run: () => new Promise(() => undefined) };
  };
}

const calls = (name: string) => scenario.builderCalls.filter(([key]) => key === name).map(([, value]) => value);
const keyEvents = () => {
  const events: KeyboardEvent[] = [];
  for (const type of ["keydown", "keyup"]) window.addEventListener(type, (event) => events.push(event as KeyboardEvent));
  return events;
};

describe("RdpClient connect", () => {
  it("builds the session from the secret, destination, relay and ticket, and starts at the requested size", async () => {
    const { cb } = await connected();
    expect(calls("withUsername")).toEqual(["alice"]);
    expect(calls("withPassword")).toEqual(["s3cret"]);
    expect(calls("withServerDomain")).toEqual(["CORP"]);
    expect(calls("withDestination")).toEqual(["10.0.0.5:3389"]);
    expect(calls("withProxyAddress")).toEqual(["nxrd://remotedesktop"]);
    expect(calls("withAuthToken")).toEqual(["tick"]);
    expect(calls("withExtension")).toEqual([{ displayControl: true }]);
    expect(calls("withDesktopSize")).toEqual([{ width: 1280, height: 720 }]);
    expect(scenario.ui.setEnableClipboard).toHaveBeenCalledWith(true);
    expect(scenario.ui.setEnableAutoClipboard).toHaveBeenCalledWith(false);
    expect(cb.onConnected).toHaveBeenCalledTimes(1);
    expect(cb.onDesktopSize).toHaveBeenCalledWith(1000, 500);
  });

  it("falls back to the stored login and an empty password when no secret was typed, and skips the size when none was given", async () => {
    await connected(options({ secret: null, initialSize: null }));
    expect(calls("withUsername")).toEqual(["stored-user"]);
    expect(calls("withPassword")).toEqual([""]);
    expect(calls("withServerDomain")).toEqual(["STORED"]);
    expect(calls("withDesktopSize")).toEqual([]);
  });

  it("does not keep the password after a successful connect", async () => {
    const { client } = await connected();
    expect(JSON.stringify(client)).not.toContain("s3cret");
    expect(client["secret" as keyof typeof client]).toBeNull();
  });

  it("drops the password when the handshake fails, and reports the failure", async () => {
    scenario.connect = async () => {
      throw new Error("handshake failed");
    };
    const client = new RdpClient(stage, options(), callbacks());
    await expect(client.connect()).rejects.toMatchObject({ failure: { protocol: "rdp", stage: "connect", message: "handshake failed" } });
    expect(client["secret" as keyof typeof client]).toBeNull();
    expect(scenario.ui.shutdown).toHaveBeenCalled();
    expect(stage.children).toHaveLength(0);
  });

  it("maps an IronRDP error to its kind, relay status and TLS alert", async () => {
    scenario.connect = async () => {
      throw {
        kind: () => 4,
        rdcleanpathDetails: () => ({ httpStatusCode: 403, tlsAlertCode: 48 }),
        backtrace: () => "Negotiation failed\nstack frame 1",
      };
    };
    const client = new RdpClient(stage, options(), callbacks());
    const error = await failureOf(client.connect());
    expect(error).toBeInstanceOf(RemoteEngineError);
    expect(error.failure).toEqual({
      protocol: "rdp",
      stage: "connect",
      message: "Negotiation failed",
      detail: "Negotiation failed\nstack frame 1",
      rdpKind: 4,
      httpStatus: 403,
      tlsAlert: 48,
      wsaError: undefined,
    });
    expect(classifyFailure(error.failure)).toBeDefined();
  });

  it("falls back to the error message, then to a default, for a malformed IronRDP error", async () => {
    scenario.connect = async () => {
      throw { kind: () => { throw new Error("wasm gone"); }, message: "unused" };
    };
    const first = await failureOf(new RdpClient(stage, options(), callbacks()).connect());
    expect(first.failure.message).toBeTruthy();
    expect(first.failure.rdpKind).toBeUndefined();

    scenario = newScenario();
    scenario.connect = async () => {
      throw null;
    };
    const second = await failureOf(new RdpClient(document.body.appendChild(document.createElement("div")), options(), callbacks()).connect());
    expect(second.failure.message).toBe("RDP 连接失败");
  });

  it("bounds a very long error message", async () => {
    scenario.connect = async () => {
      throw new Error("x".repeat(500));
    };
    const error = await failureOf(new RdpClient(stage, options(), callbacks()).connect());
    expect(error.failure.message).toHaveLength(200);
  });

  it("reports a transport preparation failure as a connect failure", async () => {
    mocks.prepareRdpTransport.mockRejectedValue(new Error("no transport"));
    const error = await failureOf(new RdpClient(stage, options(), callbacks()).connect());
    expect(error.failure).toMatchObject({ stage: "connect", message: "no transport" });
    expect(scenario.ui.connect).not.toHaveBeenCalled();
  });

  it("gives up when the component never becomes ready", async () => {
    scenario.ready = false;
    const timeouts: Array<() => void> = [];
    vi.spyOn(window, "setTimeout").mockImplementation(((callback: () => void, delay?: number) => {
      if (delay === 15000) timeouts.push(callback);
      return 1;
    }) as never);
    const pending = failureOf(new RdpClient(stage, options(), callbacks()).connect());
    await vi.waitFor(() => expect(timeouts).toHaveLength(1));
    timeouts[0]();
    const error = await pending;
    expect(error.failure.stage).toBe("connect");
    expect(error.failure.message).toBe("远程桌面组件加载超时");
    expect(stage.children).toHaveLength(0);
  });

  it("does not start when closed while the web assembly was initialising", async () => {
    const client = new RdpClient(stage, options(), callbacks());
    mocks.init.mockImplementation(async () => {
      client.disconnect();
    });
    await expect(client.connect()).rejects.toMatchObject({ failure: { stage: "closed", clean: true } });
    expect(scenario.ui.connect).not.toHaveBeenCalled();
  });

  it("does not report connected when closed while the session was being established", async () => {
    const cb = callbacks();
    const client = new RdpClient(stage, options(), cb);
    scenario.connect = async () => {
      client.disconnect();
      return { run: () => new Promise(() => undefined) };
    };
    await expect(client.connect()).rejects.toMatchObject({ failure: { stage: "closed" } });
    expect(cb.onConnected).not.toHaveBeenCalled();
  });
});

describe("RdpClient session end", () => {
  it("reports a normal end with the server's reason as clean", async () => {
    scenario.connect = async () => ({ run: async () => ({ reason: () => "Logged off" }) });
    const { cb } = await connected();
    await vi.waitFor(() => expect(cb.onDisconnected).toHaveBeenCalledTimes(1));
    expect(cb.onDisconnected).toHaveBeenCalledWith({ protocol: "rdp", stage: "closed", clean: true, wasConnected: true, message: "Logged off" });
  });

  it("uses a default message when the server gave no reason", async () => {
    scenario.connect = async () => ({ run: async () => ({ reason: () => "" }) });
    const { cb } = await connected();
    await vi.waitFor(() => expect(cb.onDisconnected).toHaveBeenCalledTimes(1));
    expect(cb.onDisconnected.mock.calls[0][0].message).toBe("远程桌面已断开");
  });

  it("reports a failed session as a closed-stage failure of an established session", async () => {
    scenario.connect = async () => ({ run: async () => Promise.reject({ kind: () => 7, backtrace: () => "Connection reset" }) });
    const { cb } = await connected();
    await vi.waitFor(() => expect(cb.onDisconnected).toHaveBeenCalledTimes(1));
    expect(cb.onDisconnected.mock.calls[0][0]).toMatchObject({ stage: "closed", wasConnected: true, rdpKind: 7, message: "Connection reset" });
  });

  it("stays silent about an end it asked for", async () => {
    let finish!: () => void;
    scenario.connect = async () => ({
      run: () => new Promise((resolve) => {
        finish = () => resolve({ reason: () => "bye" });
      }),
    });
    const { client, cb } = await connected();
    client.disconnect();
    finish();
    await Promise.resolve();
    await Promise.resolve();
    expect(cb.onDisconnected).not.toHaveBeenCalled();
  });

  it("tears down: shuts the component down, removes it and stops observing, even if shutdown throws", async () => {
    scenario.ui.shutdown.mockImplementation(() => {
      throw new Error("gone");
    });
    const disconnect = vi.spyOn(MutationObserver.prototype, "disconnect");
    const { client, cb } = await connected();
    expect(stage.children).toHaveLength(1);
    expect(() => client.disconnect()).not.toThrow();
    expect(stage.children).toHaveLength(0);
    expect(FakeObserver.instances.every((observer) => observer.disconnected)).toBe(true);
    expect(disconnect).toHaveBeenCalled();
    const reports = (cb as { onDesktopSize: ReturnType<typeof vi.fn> }).onDesktopSize.mock.calls.length;
    setCanvasSize(640, 480);
    await flushMutations();
    expect((cb as { onDesktopSize: ReturnType<typeof vi.fn> }).onDesktopSize.mock.calls).toHaveLength(reports);
    expect(client.inputElement()).toBeNull();
    expect(client.desktopSize()).toEqual({ width: 0, height: 0 });
  });
});

describe("RdpClient resize", () => {
  it("clamps to the supported range and rounds down to an even size", async () => {
    const { client } = await connected();
    client.resize(1281, 721);
    client.resize(100, 100);
    client.resize(20000, 9000);
    expect(scenario.ui.resize.mock.calls).toEqual([
      [1280, 720],
      [320, 240],
      [8192, 8192],
    ]);
  });

  it("does nothing before connect or after a drop, and survives the server refusing", async () => {
    const early = new RdpClient(stage, options(), callbacks());
    early.resize(800, 600);
    expect(scenario.ui.resize).not.toHaveBeenCalled();
    scenario.ui.resize.mockImplementation(() => {
      throw new Error("not allowed");
    });
    const { client } = await connected();
    expect(() => client.resize(800, 600)).not.toThrow();
    expect(client.resize(800, 600)).toBe("failed");
  });

  it("does not send the same size again while it waits for the server", async () => {
    const { client } = await connected();
    expect(client.resize(1280, 720)).toBe("awaiting");
    for (let i = 0; i < 20; i += 1) expect(client.resize(1280, 720)).toBe("awaiting");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(1);
    expect(client.resize(1024, 768)).toBe("awaiting");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(2);
  });

  it("counts a rounded canvas size as the server's answer", async () => {
    const { client } = await connected();
    expect(client.resize(1366, 768)).toBe("awaiting");
    setCanvasSize(1364, 768);
    await flushMutations();
    expect(client.resize(1366, 768)).toBe("applied");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(1);
    // A later, different canvas size is not the answer to that request any more.
    setCanvasSize(1000, 500);
    await flushMutations();
    expect(client.resize(1366, 768)).toBe("awaiting");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(2);
  });

  it("does not let an earlier request's answer satisfy a newer one", async () => {
    const { client } = await connected();
    expect(client.resize(1280, 720)).toBe("awaiting");
    expect(client.resize(1024, 768)).toBe("awaiting");
    setCanvasSize(1280, 720);
    await flushMutations();
    expect(client.resize(1024, 768)).toBe("awaiting");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(2);
    setCanvasSize(1022, 768);
    await flushMutations();
    expect(client.resize(1024, 768)).toBe("applied");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(2);
  });

  it("recognises the answer even before the canvas observer has run", async () => {
    const { client } = await connected();
    client.resize(1366, 768);
    setCanvasSize(1364, 768);
    expect(client.resize(1366, 768)).toBe("applied");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(1);
  });

  it("waits for the actual canvas size before reporting a resize as applied", async () => {
    const early = new RdpClient(stage, options(), callbacks());
    expect(early.resize(800, 600)).toBe("pending");
    const { client } = await connected();
    expect(client.resize(800, 600)).toBe("awaiting");
    expect(client.resize(800, 600)).toBe("awaiting");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(1);
    scenario.canvas.width = 800;
    scenario.canvas.height = 600;
    await flushMutations();
    expect(client.resize(800, 600)).toBe("applied");
    expect(scenario.ui.resize).toHaveBeenCalledTimes(1);
    scenario.ui.resize.mockImplementationOnce(() => {
      throw new Error("Could not encode a resize: Display Control Virtual Channel is not yet connected");
    });
    expect(client.resize(1024, 768)).toBe("pending");
    scenario.ui.resize.mockImplementationOnce(() => {
      throw { backtrace: () => "Could not encode a resize: Display Control Virtual Channel is not available" };
    });
    expect(client.resize(1024, 768)).toBe("unavailable");
  });
});

describe("RdpClient layout", () => {
  it("fits the picture into the pane, centred, and follows pane resizes", async () => {
    setStageSize(500, 500);
    const { client } = await connected();
    const sizer = stage.firstElementChild as HTMLElement;
    const holder = sizer.firstElementChild as HTMLElement;
    expect(sizer.style.width).toBe("500px");
    expect(sizer.style.height).toBe("250px");
    expect(sizer.style.top).toBe("125px");
    expect(holder.style.transform).toBe("scale(0.5)");
    expect(holder.style.width).toBe("1000px");
    expect(stage.style.overflow).toBe("hidden");
    const viewer = stage.querySelector("iron-remote-desktop")!.shadowRoot!.querySelector<HTMLElement>(".screen-viewer")!;
    expect(viewer.style.width).toBe("1000px");
    expect(viewer.style.height).toBe("500px");
    expect(viewer.style.minWidth).toBe("0px");
    setStageSize(2000, 1000);
    FakeObserver.instances[0].callback();
    expect(holder.style.transform).toBe("scale(2)");
    client.setScale(1);
    expect(holder.style.transform).toBe("scale(1)");
    expect(sizer.style.left).toBe("500px");
    client.setScale(2);
    expect(sizer.style.width).toBe("2000px");
    client.setScale(1.5);
    setStageSize(500, 500);
    client.setScale(1.5);
    expect(sizer.style.left).toBe("0px");
  });

  it("uses the actual canvas size after the server confirms a resize", async () => {
    setStageSize(500, 500);
    const { client, cb } = await connected();
    const sizer = stage.firstElementChild as HTMLElement;
    const holder = sizer.firstElementChild as HTMLElement;
    const viewer = stage.querySelector("iron-remote-desktop")!.shadowRoot!.querySelector<HTMLElement>(".screen-viewer")!;
    viewer.style.cssText = "width:1366px;height:768px;min-width:1366px;min-height:768px";
    await flushMutations();
    expect(viewer.style.width).toBe("1000px");
    expect(viewer.style.height).toBe("500px");
    expect(cb.onDesktopSize).toHaveBeenCalledTimes(1);
    scenario.canvas.width = 600;
    scenario.canvas.height = 800;
    await flushMutations();
    expect(holder.style.transform).toBe("scale(0.625)");
    expect(sizer.style.width).toBe("375px");
    expect(sizer.style.height).toBe("500px");
    expect(sizer.style.left).toBe("62.5px");
    expect(sizer.style.top).toBe("0px");
    expect(viewer.style.width).toBe("600px");
    expect(viewer.style.height).toBe("800px");
    expect(cb.onDesktopSize).toHaveBeenLastCalledWith(600, 800);
    expect(stage.style.overflow).toBe("hidden");
    client.setScale(1);
    expect(stage.style.overflow).toBe("auto");
    expect(sizer.style.width).toBe("600px");
  });

  it("leaves the layout alone while the pane has no size yet", async () => {
    setStageSize(0, 0);
    await connected();
    const sizer = stage.firstElementChild as HTMLElement;
    expect(sizer.style.width).toBe("");
  });
});

describe("RdpClient input", () => {
  it("only lets the canvas take focus while the pane asked for it", async () => {
    const focus = vi.spyOn(HTMLElement.prototype, "focus");
    const { client } = await connected();
    scenario.canvas.focus();
    expect(focus).not.toHaveBeenCalled();
    client.focus();
    expect(focus).toHaveBeenCalledTimes(1);
    scenario.canvas.focus();
    expect(focus).toHaveBeenCalledTimes(1);
  });

  it("sends a key as keyboard events carrying the modifiers held at that moment", async () => {
    const { client } = await connected();
    const events = keyEvents();
    client.sendKey("ControlLeft", true);
    client.sendKey("KeyL", true);
    client.sendKey("KeyL", false);
    client.sendKey("ControlLeft", false);
    expect(events.map((event) => `${event.type}:${event.code}:${event.key}:${event.ctrlKey}`)).toEqual([
      "keydown:ControlLeft:Control:true",
      "keydown:KeyL:l:true",
      "keyup:KeyL:l:true",
      "keyup:ControlLeft:Control:false",
    ]);
  });

  it("names digit, space and other keys the way the browser does", async () => {
    const { client } = await connected();
    const events = keyEvents();
    client.sendKey("Digit7", true);
    client.sendKey("Space", true);
    client.sendKey("F5", true);
    expect(events.map((event) => event.key)).toEqual(["7", " ", "F5"]);
  });

  it("sends nothing in read-only mode or before connect", async () => {
    const events = keyEvents();
    const early = new RdpClient(stage, options(), callbacks());
    early.sendKey("KeyA", true);
    await early.sendCombo(["KeyA"]);
    early.sendText("a");
    const { client } = await connected();
    client.setViewOnly(true);
    client.sendKey("KeyA", true);
    await client.sendCombo(["KeyA"]);
    client.sendText("a");
    expect(events.filter((event) => event.code === "KeyA" || event.key === "a")).toEqual([]);
  });

  it("presses a combo in order and releases it in reverse", async () => {
    const { client } = await connected();
    vi.useFakeTimers();
    const events = keyEvents();
    const pending = client.sendCombo(["AltLeft", "Tab"]);
    await vi.runAllTimersAsync();
    await pending;
    expect(events.map((event) => `${event.type}:${event.code}`)).toEqual(["keydown:AltLeft", "keydown:Tab", "keyup:Tab", "keyup:AltLeft"]);
  });

  it("types text through Unicode mode and sends characters beyond one code unit to the session directly", async () => {
    connectThroughModule();
    const { client } = await connected();
    const events = keyEvents();
    expect(client.sendText("a\u{1F600}b")).toBe(0);
    expect(events.map((event) => `${event.type}:${event.key}`)).toEqual(["keydown:a", "keyup:a", "keydown:b", "keyup:b"]);
    expect(wasm.state.inputs.map((transaction) => transaction.events)).toEqual([["down:\u{1F600}", "up:\u{1F600}"]]);
    expect(scenario.ui.setKeyboardUnicodeMode.mock.calls).toEqual([[true], [false]]);
  });

  it("counts characters it cannot send when the session takes no direct input", async () => {
    wasm.state.applyInputs = false;
    connectThroughModule();
    try {
      const { client } = await connected();
      const events = keyEvents();
      expect(client.sendText("a\u{1F600}\u{1F44D}")).toBe(2);
      expect(events.map((event) => `${event.type}:${event.key}`)).toEqual(["keydown:a", "keyup:a"]);
    } finally {
      wasm.state.applyInputs = true;
    }
  });

  it("restores normal keyboard mode even if typing fails", async () => {
    const { client } = await connected();
    vi.spyOn(window, "dispatchEvent").mockImplementation(() => {
      throw new Error("boom");
    });
    expect(() => client.sendText("a")).toThrow("boom");
    expect(scenario.ui.setKeyboardUnicodeMode).toHaveBeenLastCalledWith(false);
  });

  it("releases every key still held plus the component's own inputs, and entering read-only does the same", async () => {
    const { client } = await connected();
    const events = keyEvents();
    const leave = vi.fn();
    scenario.canvas.addEventListener("mouseleave", leave);
    client.sendKey("ControlLeft", true);
    client.sendKey("AltLeft", true);
    events.length = 0;
    client.releaseAllKeys();
    expect(events.map((event) => `${event.type}:${event.code}`)).toEqual(["keyup:ControlLeft", "keyup:AltLeft"]);
    expect(leave).toHaveBeenCalledTimes(1);
    client.releaseAllKeys();
    expect(events).toHaveLength(2);
    client.sendKey("ShiftLeft", true);
    client.setViewOnly(true);
    expect(events[events.length - 1]).toMatchObject({ type: "keyup", code: "ShiftLeft" });
    client.setViewOnly(false);
  });

  it("releases held inputs before the canvas loses focus", async () => {
    const { client } = await connected();
    const order: string[] = [];
    scenario.canvas.addEventListener("mouseleave", () => order.push("release"));
    scenario.canvas.addEventListener("blur", () => order.push("blur"));
    client.focus();
    scenario.canvas.focus = () => undefined;
    client.blur();
    expect(order[0]).toBe("release");
  });

  it("releases keys when the client disconnects", async () => {
    const { client } = await connected();
    client.sendKey("MetaLeft", true);
    const events = keyEvents();
    client.disconnect();
    expect(events.map((event) => `${event.type}:${event.code}`)).toEqual(["keyup:MetaLeft"]);
  });
});

describe("RdpClient clipboard", () => {
  type Clip = InstanceType<typeof wasm.Backend.ClipboardData>;
  const entries = (clip: Clip | undefined) => clip?.entries.map((entry) => [entry.mime, entry.value]);
  const remoteCopy = (fill: (clip: Clip) => void) => {
    const clip = new wasm.Backend.ClipboardData();
    fill(clip);
    wasm.state.remoteCallback?.(clip);
  };
  const flush = async () => {
    for (let i = 0; i < 5; i += 1) await Promise.resolve();
  };

  beforeEach(() => {
    wasm.state.remoteCallback = null;
    wasm.state.pastes = [];
    mocks.copyText.mockReset();
    mocks.copyText.mockResolvedValue(undefined);
    mocks.readText.mockReset();
    mocks.readText.mockResolvedValue("");
    // Like the component: ui.connect() builds the session from the module before initClipboard() has registered
    // any clipboard callback of its own.
    scenario.connect = async () => {
      const element = document.querySelector("iron-remote-desktop") as HTMLElement & { module: { SessionBuilder: new () => { connect(): Promise<unknown> } } };
      await new element.module.SessionBuilder().connect();
      return { run: () => new Promise(() => undefined) };
    };
  });

  it("opens the clipboard channel itself, since the component registers its callback too late", async () => {
    await connected();
    expect(wasm.state.remoteCallback).toBeTypeOf("function");
  });

  it("opens no clipboard channel when clipboard is off for the asset", async () => {
    const { client } = await connected(options({ clipboard: false }));
    expect(wasm.state.remoteCallback).toBeNull();
    expect(await client.sendClipboard()).toEqual({ status: "refused" });
    expect(await client.fetchClipboard()).toEqual({ status: "refused" });
  });

  it("sends text, and an image together with its text, from a native paste", async () => {
    const { client } = await connected();
    const textData = {
      items: [],
      files: [],
      getData: (type: string) => (type === "text/plain" ? "copied text" : type === "text/html" ? "<b>copied</b>" : ""),
    } as unknown as DataTransfer;
    expect(await client.sendClipboard(textData)).toEqual({ status: "done" });
    expect(entries(wasm.state.pastes[0])).toEqual([
      ["text/plain", "copied text"],
      ["text/html", "<b>copied</b>"],
    ]);

    const pngMagic = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
    const imageFile = new File([pngMagic], "screenshot.png", { type: "image/png" });
    const imageData = {
      items: [{ kind: "file", type: "image/png", getAsFile: () => imageFile }],
      files: [imageFile],
      getData: (type: string) => (type === "text/plain" ? "caption" : ""),
    } as unknown as DataTransfer;
    expect(await client.sendClipboard(imageData)).toEqual({ status: "done" });
    expect(entries(wasm.state.pastes[1])).toEqual([
      ["image/png", pngMagic],
      ["text/plain", "caption"],
    ]);
  });

  it("names the images it left out and refuses files it has no channel for", async () => {
    const { client } = await connected();
    const png = () => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "a.png", { type: "image/png" });
    const images = [png(), png()];
    const two = { items: images.map((file) => ({ kind: "file", type: "image/png", getAsFile: () => file })), files: images, getData: () => "" } as unknown as DataTransfer;
    const outcome = await client.sendClipboard(two);
    expect(outcome.status).toBe("done");
    expect(outcome.note).toBeTruthy();

    const doc = new File(["x"], "a.txt", { type: "text/plain" });
    const files = { items: [{ kind: "file", type: "text/plain", getAsFile: () => doc }], files: [doc], getData: () => "" } as unknown as DataTransfer;
    const refused = await client.sendClipboard(files);
    expect(refused.status).toBe("unsupported");
    expect(refused.note).toBeTruthy();
  });

  it("reads the browser clipboard for the toolbar, and says when it is empty or refused", async () => {
    const read = vi.fn(async () => [{ types: ["text/plain"], getType: async () => new Blob(["from toolbar"], { type: "text/plain" }) }]);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { read } });
    const { client } = await connected();
    expect(await client.sendClipboard()).toEqual({ status: "done" });
    expect(entries(wasm.state.pastes[0])).toEqual([["text/plain", "from toolbar"]]);
    read.mockResolvedValueOnce([]);
    expect((await client.sendClipboard()).status).toBe("empty");
    read.mockRejectedValueOnce(new Error("denied"));
    expect(await client.sendClipboard()).toEqual({ status: "refused" });
  });

  it("writes a remote copy to the local clipboard only while the tab is in front and the window has focus", async () => {
    const focus = vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const { client } = await connected();
    remoteCopy((clip) => clip.addText("text/plain", "remote text"));
    await flush();
    expect(mocks.copyText).toHaveBeenCalledWith("remote text");
    client.setForeground(false);
    remoteCopy((clip) => clip.addText("text/plain", "in background"));
    client.setForeground(true);
    focus.mockReturnValue(false);
    remoteCopy((clip) => clip.addText("text/plain", "unfocused"));
    await flush();
    expect(mocks.copyText).toHaveBeenCalledTimes(1);
    // The latest remote copy is still there for a manual fetch.
    expect(await client.fetchClipboard()).toEqual({ status: "done" });
    expect(mocks.copyText).toHaveBeenLastCalledWith("unfocused");
  });

  it("leaves remote copies for a manual fetch when automatic sync is off", async () => {
    const { client } = await connected(options({ clipboardAutoSync: false }));
    expect((await client.fetchClipboard()).status).toBe("empty");
    remoteCopy((clip) => clip.addText("text/plain", "kept"));
    await flush();
    expect(mocks.copyText).not.toHaveBeenCalled();
    expect(await client.fetchClipboard()).toEqual({ status: "done" });
    expect(mocks.copyText).toHaveBeenCalledWith("kept");
  });

  it("reports a remote image it could not write, keeps it for a retry, and tells the pane when it was automatic", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const cb = { ...callbacks(), onClipboardNotice: vi.fn() };
    const { client } = await connected(options(), cb);
    remoteCopy((clip) => clip.addBinary("image/png", new Uint8Array([1, 2, 3])));
    await flush();
    expect(cb.onClipboardNotice).toHaveBeenCalledTimes(1);
    const outcome = await client.fetchClipboard();
    expect(outcome.status).toBe("refused");
    expect(outcome.note).toBeTruthy();
  });

  describe("on the desktop", () => {
    beforeEach(() => {
      mocks.web = false;
      mocks.pickLocalFolder.mockReset();
      mocks.writeFile.mockReset();
      mocks.writeFile.mockImplementation(async (path: string) => ({ path, bytes: 1, exists: false }));
      mocks.writeClipboardImage.mockReset();
      mocks.writeClipboardImage.mockResolvedValue({ bytes: 3 });
    });
    afterEach(() => {
      mocks.web = true;
      mocks.nativeWebKit = false;
      wasm.state.fileChannel = false;
      wasm.FileTransfer.last = null;
    });

    const filesCopied = (files: unknown[]) => wasm.FileTransfer.last?.handlers.get("files-available")?.(files);

    it("saves files the remote copied into a picked folder, keeping their folders and never replacing a local file", async () => {
      vi.spyOn(document, "hasFocus").mockReturnValue(true);
      wasm.state.fileChannel = true;
      const cb = { ...callbacks(), onClipboardNotice: vi.fn() };
      const { client } = await connected(options(), cb);
      const channel = wasm.FileTransfer.last!;
      channel.downloads.set(0, new Blob(["abc"]));
      channel.downloads.set(2, new Blob(["de"]));
      channel.downloads.set(3, new Blob(["f"]));
      filesCopied([
        { name: "a.txt", size: 3, lastModified: 0 },
        { name: "docs", size: 0, lastModified: 0, isDirectory: true },
        { name: "b.txt", path: "docs", size: 2, lastModified: 0 },
        { name: "taken.txt", size: 1, lastModified: 0 },
        { name: "evil.txt", path: "..\\..", size: 1, lastModified: 0 },
      ]);
      expect(cb.onClipboardNotice).toHaveBeenCalledTimes(1);
      mocks.pickLocalFolder.mockResolvedValue("/home/me/Downloads");
      mocks.writeFile.mockImplementation(async (path: string) => ({ path, bytes: 1, exists: path.endsWith("taken.txt") }));
      const outcome = await client.fetchClipboard();
      expect(outcome.status).toBe("done");
      expect(outcome.note).toContain("/home/me/Downloads");
      expect(outcome.note).toContain("taken.txt");
      expect(outcome.note).toContain("evil.txt");
      expect(mocks.writeFile.mock.calls.map(([path, content]) => [path, atob(content)])).toEqual([
        ["/home/me/Downloads/a.txt", "abc"],
        ["/home/me/Downloads/docs/b.txt", "de"],
        ["/home/me/Downloads/taken.txt", "f"],
      ]);
      expect(mocks.copyText).not.toHaveBeenCalled();
    });

    it("saves nothing when the folder pick is cancelled, and a later text copy replaces the file copy", async () => {
      wasm.state.fileChannel = true;
      const { client } = await connected(options({ clipboardAutoSync: false }));
      filesCopied([{ name: "a.txt", size: 1, lastModified: 0 }]);
      mocks.pickLocalFolder.mockResolvedValue(null);
      expect((await client.fetchClipboard()).status).toBe("refused");
      expect(mocks.writeFile).not.toHaveBeenCalled();
      remoteCopy((clip) => clip.addText("text/plain", "newer text"));
      expect(await client.fetchClipboard()).toEqual({ status: "done" });
      expect(mocks.copyText).toHaveBeenCalledWith("newer text");
      expect(mocks.pickLocalFolder).toHaveBeenCalledTimes(1);
    });

    it("writes a remote image through the app on desktop WebKit, and says why when that fails", async () => {
      mocks.nativeWebKit = true;
      const { client } = await connected(options({ clipboardAutoSync: false }));
      remoteCopy((clip) => clip.addBinary("image/png", new Uint8Array([1, 2, 3])));
      expect(await client.fetchClipboard()).toEqual({ status: "done" });
      expect(mocks.writeClipboardImage).toHaveBeenCalledWith(btoa(String.fromCharCode(1, 2, 3)));
      mocks.writeClipboardImage.mockRejectedValueOnce(new Error("xclip missing"));
      const failed = await client.fetchClipboard();
      expect(failed.status).toBe("refused");
      expect(failed.note).toContain("xclip missing");
    });
  });

  it("does not send in read-only mode", async () => {
    const { client } = await connected();
    client.setViewOnly(true);
    expect(await client.sendClipboard()).toEqual({ status: "refused" });
    expect(wasm.state.pastes).toEqual([]);
  });
});

describe("RdpClient picture", () => {
  it("exposes the canvas as the input element and its size as the desktop size", async () => {
    const { client } = await connected();
    expect(client.inputElement()).toBe(scenario.canvas);
    expect(client.desktopSize()).toEqual({ width: 1000, height: 500 });
    expect(client.protocol).toBe("rdp");
    expect(client.supportsResize).toBe(true);
  });

  it("produces no thumbnail before there is a canvas", () => {
    const client = new RdpClient(stage, options(), callbacks());
    expect(client.thumbnail(240)).toBeNull();
  });
});
