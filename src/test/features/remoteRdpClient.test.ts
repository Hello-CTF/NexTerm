/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  prepareRdpTransport: vi.fn(),
  init: vi.fn(),
}));

vi.mock("../../features/remote/remoteSocket", () => ({
  openVncChannel: vi.fn(),
  prepareRdpTransport: mocks.prepareRdpTransport,
  remoteProxyAddress: () => "nxrd://remotedesktop",
}));
vi.mock("@devolutions/iron-remote-desktop", () => ({}));
vi.mock("@devolutions/iron-remote-desktop-rdp", () => ({
  init: mocks.init,
  Backend: { backend: true },
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
        root.append(scenario.canvas);
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
      rdpKind: 4,
      httpStatus: 403,
      tlsAlert: 48,
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
    const { client } = await connected();
    expect(stage.children).toHaveLength(1);
    expect(() => client.disconnect()).not.toThrow();
    expect(stage.children).toHaveLength(0);
    expect(FakeObserver.instances.every((observer) => observer.disconnected)).toBe(true);
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

  it("types text through Unicode mode and skips characters that do not fit one code unit", async () => {
    const { client } = await connected();
    const events = keyEvents();
    client.sendText("a\u{1F600}b");
    expect(events.map((event) => `${event.type}:${event.key}`)).toEqual(["keydown:a", "keyup:a", "keydown:b", "keyup:b"]);
    expect(scenario.ui.setKeyboardUnicodeMode.mock.calls).toEqual([[true], [false]]);
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
  it("sends and fetches through the component", async () => {
    const { client } = await connected();
    expect(await client.sendClipboard()).toBe(true);
    expect(await client.fetchClipboard()).toBe(true);
    expect(scenario.ui.sendClipboardData).toHaveBeenCalledTimes(1);
    expect(scenario.ui.saveRemoteClipboardData).toHaveBeenCalledTimes(1);
  });

  it("reports false when the component or browser refuses", async () => {
    scenario.ui.sendClipboardData.mockRejectedValue(new Error("denied"));
    scenario.ui.saveRemoteClipboardData.mockRejectedValue(new Error("denied"));
    const { client } = await connected();
    expect(await client.sendClipboard()).toBe(false);
    expect(await client.fetchClipboard()).toBe(false);
  });

  it("does not send in read-only mode, and moves nothing when clipboard is off for the asset", async () => {
    const { client } = await connected();
    client.setViewOnly(true);
    expect(await client.sendClipboard()).toBe(false);
    expect(await client.fetchClipboard()).toBe(true);
    expect(scenario.ui.sendClipboardData).not.toHaveBeenCalled();

    scenario = newScenario();
    const other = document.body.appendChild(document.createElement("div"));
    const off = new RdpClient(other, options({ clipboard: false }), callbacks());
    await off.connect();
    expect(await off.sendClipboard()).toBe(false);
    expect(await off.fetchClipboard()).toBe(false);
    expect(scenario.ui.setEnableClipboard).toHaveBeenCalledWith(false);
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
