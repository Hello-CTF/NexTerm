/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";


const mocks = vi.hoisted(() => ({
  rfbs: [] as unknown[],
  openVncChannel: vi.fn(),
  channelClose: vi.fn(),
}));

vi.mock("../../features/remote/remoteSocket", () => ({
  openVncChannel: mocks.openVncChannel,
  prepareRdpTransport: vi.fn(),
  remoteProxyAddress: () => "nxrd://remotedesktop",
}));

vi.mock("@novnc/novnc", () => ({
  default: class FakeRfb extends EventTarget {
    viewOnly = false;
    focusOnClick = false;
    background = "";
    showDotCursor = false;
    clipViewport = false;
    dragViewport = false;
    scaleViewport = false;
    args: unknown[];
    keys: Array<[number, string | null, boolean | undefined]> = [];
    pasted: string[] = [];
    ctrlAltDel = 0;
    disconnects = 0;
    focusOptions: unknown[] = [];
    blurs = 0;
    sendCredentials = vi.fn();
    constructor(...args: unknown[]) {
      super();
      this.args = args;
      mocks.rfbs.push(this);
    }
    emit(type: string, detail?: unknown) {
      this.dispatchEvent(new CustomEvent(type, { detail }));
    }
    disconnect() {
      this.disconnects += 1;
    }
    focus(options?: unknown) {
      this.focusOptions.push(options);
    }
    blur() {
      this.blurs += 1;
    }
    sendKey(keysym: number, code: string | null, down?: boolean) {
      this.keys.push([keysym, code, down]);
    }
    sendCtrlAltDel() {
      this.ctrlAltDel += 1;
    }
    clipboardPasteFrom(text: string) {
      this.pasted.push(text);
    }
  },
}));

import { VncClient, createVncClient } from "../../features/remote/VncClient";
import type { ClientCallbacks, ClientOptions, EngineFailure } from "../../features/remote/remoteTypes";

type Rfb = {
  args: unknown[];
  viewOnly: boolean;
  focusOnClick: boolean;
  background: string;
  showDotCursor: boolean;
  clipViewport: boolean;
  dragViewport: boolean;
  scaleViewport: boolean;
  keys: Array<[number, string | null, boolean | undefined]>;
  pasted: string[];
  ctrlAltDel: number;
  disconnects: number;
  focusOptions: unknown[];
  blurs: number;
  sendCredentials: ReturnType<typeof vi.fn>;
  emit: (type: string, detail?: unknown) => void;
};

const prepared = { ticket: "tick", protocol: "vnc", assetId: "a", name: "n", host: "h", port: 1, username: "", domain: "", promptPassword: false, hasPassword: true, expiresAtMs: 1 };
const options = (patch: Partial<ClientOptions> = {}): ClientOptions => ({
  prepared: prepared as ClientOptions["prepared"],
  secret: null,
  viewOnly: false,
  clipboard: true,
  clipboardAutoSync: true,
  initialSize: null,
  ...patch,
});
const callbacks = () => ({ onConnected: vi.fn(), onDisconnected: vi.fn(), onClipboardNotice: vi.fn(), onDesktopSize: vi.fn() }) satisfies ClientCallbacks;
const rfb = () => mocks.rfbs[0] as Rfb;

let stage: HTMLElement;

beforeEach(() => {
  stage = document.createElement("div");
  document.body.append(stage);
  mocks.openVncChannel.mockReset();
  mocks.openVncChannel.mockImplementation(async () => ({ close: mocks.channelClose }));
  mocks.channelClose.mockReset();
});

afterEach(() => {
  mocks.rfbs.length = 0;
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

async function connected(opts: ClientOptions = options(), cb = callbacks()) {
  const client = new VncClient(stage, opts, cb);
  const pending = client.connect();
  await vi.waitFor(() => expect(mocks.rfbs).toHaveLength(1));
  rfb().emit("connect");
  await pending;
  return { client, cb };
}

describe("VncClient connect", () => {
  it("opens the relay channel with the ticket and never hands noVNC any credentials", async () => {
    await connected(options({ secret: { username: "u", domain: "d", password: "hunter2" } }));
    expect(mocks.openVncChannel).toHaveBeenCalledWith("tick", expect.any(AbortSignal));
    const [target, channel, rfbOptions] = rfb().args as [HTMLElement, unknown, Record<string, unknown>];
    expect(target).toBe(stage);
    expect(channel).toEqual({ close: mocks.channelClose });
    expect(rfbOptions).toEqual({ shared: true });
    expect(JSON.stringify(rfb().args)).not.toContain("hunter2");
    expect(rfb().sendCredentials).not.toHaveBeenCalled();
    expect(rfb()).toMatchObject({ focusOnClick: true, background: "transparent", showDotCursor: true, viewOnly: false });
  });

  it("starts read-only when asked and reports the connection once", async () => {
    const { cb } = await connected(options({ viewOnly: true }));
    expect(rfb().viewOnly).toBe(true);
    expect(cb.onConnected).toHaveBeenCalledTimes(1);
  });

  it("is built by the factory without connecting", async () => {
    const client = await createVncClient(stage, options(), callbacks());
    expect(client.protocol).toBe("vnc");
    expect(client.supportsResize).toBe(false);
    expect(mocks.openVncChannel).not.toHaveBeenCalled();
  });

  it("maps a relay failure to a connect-stage engine error carrying its message", async () => {
    mocks.openVncChannel.mockRejectedValue(new Error("relay refused"));
    const client = new VncClient(stage, options(), callbacks());
    await expect(client.connect()).rejects.toMatchObject({ failure: { protocol: "vnc", stage: "connect", message: "relay refused" } });
    expect(mocks.rfbs).toHaveLength(0);
  });

  it("uses a generic message when the failure is not an Error", async () => {
    mocks.openVncChannel.mockRejectedValue("nope");
    const client = new VncClient(stage, options(), callbacks());
    await expect(client.connect()).rejects.toMatchObject({ failure: { stage: "connect", message: expect.any(String) } });
  });

  it("closes the channel and reports a clean cancel when disconnect() came during the channel open", async () => {
    const client = new VncClient(stage, options(), callbacks());
    const pending = client.connect();
    client.disconnect();
    await expect(pending).rejects.toMatchObject({ failure: { stage: "closed", clean: true } });
    expect(mocks.channelClose).toHaveBeenCalled();
    expect(mocks.rfbs).toHaveLength(0);
  });

  it("aborts the channel open through the signal on disconnect", async () => {
    const client = new VncClient(stage, options(), callbacks());
    const pending = client.connect().catch(() => undefined);
    client.disconnect();
    await pending;
    const signal = mocks.openVncChannel.mock.calls[0][1] as AbortSignal;
    expect(signal.aborted).toBe(true);
  });

  it("rejects connect with a security failure and keeps the reason, falling back to a default", async () => {
    const first = new VncClient(stage, options(), callbacks());
    const pending = first.connect();
    await vi.waitFor(() => expect(mocks.rfbs).toHaveLength(1));
    rfb().emit("securityfailure", { reason: "Authentication failed" });
    await expect(pending).rejects.toMatchObject({ failure: { stage: "security", message: "Authentication failed" } });

    mocks.rfbs.length = 0;
    const second = new VncClient(stage, options(), callbacks());
    const pendingSecond = second.connect();
    await vi.waitFor(() => expect(mocks.rfbs).toHaveLength(1));
    rfb().emit("securityfailure");
    await expect(pendingSecond).rejects.toMatchObject({ failure: { stage: "security", message: expect.stringContaining("VNC") } });
  });

  it("rejects connect when the socket closes before the session came up, keeping the clean flag", async () => {
    const client = new VncClient(stage, options(), callbacks());
    const pending = client.connect();
    await vi.waitFor(() => expect(mocks.rfbs).toHaveLength(1));
    rfb().emit("disconnect", { clean: true });
    const error = (await pending.then(
      () => null,
      (reason: unknown) => reason,
    )) as { failure: EngineFailure };
    expect(error.failure).toMatchObject({ stage: "closed", clean: true, wasConnected: false });
  });
});

describe("VncClient session end", () => {
  it("reports a drop of an established session as not clean even when noVNC says clean", async () => {
    const { cb } = await connected();
    rfb().emit("disconnect", { clean: true });
    expect(cb.onDisconnected).toHaveBeenCalledTimes(1);
    expect(cb.onDisconnected).toHaveBeenCalledWith(expect.objectContaining({ clean: false, wasConnected: true, stage: "closed" }));
  });

  it("stays quiet when the client closed the session itself, and closes noVNC and the channel", async () => {
    const { client, cb } = await connected();
    client.disconnect();
    expect(rfb().disconnects).toBe(1);
    expect(mocks.channelClose).toHaveBeenCalled();
    rfb().emit("disconnect", { clean: true });
    expect(cb.onDisconnected).not.toHaveBeenCalled();
  });

  it("survives noVNC throwing while it is being disconnected", async () => {
    const { client } = await connected();
    (rfb() as unknown as { disconnect: () => void }).disconnect = () => {
      throw new Error("already closed");
    };
    expect(() => client.disconnect()).not.toThrow();
    expect(mocks.channelClose).toHaveBeenCalled();
  });

  it("stops acting on the session after a drop", async () => {
    const { client } = await connected();
    rfb().emit("disconnect", { clean: false });
    client.sendKey("KeyA", true);
    client.setViewOnly(true);
    client.focus();
    client.blur();
    expect(rfb().keys).toEqual([]);
    expect(await client.sendClipboard()).toEqual({ status: "refused" });
  });
});

describe("VncClient scale", () => {
  it("fits by scaling the viewport and never scrolls in fit mode", async () => {
    const { client } = await connected();
    expect(rfb()).toMatchObject({ scaleViewport: true, clipViewport: false, dragViewport: false });
    client.setScale(1);
    expect(rfb()).toMatchObject({ scaleViewport: false, clipViewport: false, dragViewport: false });
    client.setScale("fit");
    expect(rfb()).toMatchObject({ scaleViewport: true, clipViewport: false, dragViewport: false });
  });

  it("treats every fixed zoom as the picture's own size, scrolled by noVNC's screen, and leaves drags to the remote", async () => {
    const { client } = await connected();
    client.setScale(1.5);
    // dragViewport would turn every left-button drag into panning, so text could not be selected on the remote.
    expect(rfb()).toMatchObject({ scaleViewport: false, clipViewport: false, dragViewport: false });
  });

  it("applies a scale chosen before the session exists once it does", async () => {
    const client = new VncClient(stage, options(), callbacks());
    client.setScale(2);
    const pending = client.connect();
    await vi.waitFor(() => expect(mocks.rfbs).toHaveLength(1));
    expect(rfb()).toMatchObject({ scaleViewport: false, dragViewport: false });
    rfb().emit("connect");
    await pending;
  });

  it("ignores scale and resize requests safely before and after the session", () => {
    const client = new VncClient(stage, options(), callbacks());
    expect(() => {
      client.setScale(1);
      client.setViewOnly(true);
      client.resize();
      client.releaseAllKeys();
    }).not.toThrow();
  });
});

describe("VncClient keys", () => {
  it("sends mapped keys as X11 keysyms and drops unmapped ones", async () => {
    const { client } = await connected();
    client.sendKey("ControlLeft", true);
    client.sendKey("KeyL", false);
    client.sendKey("NoSuchKey", true);
    expect(rfb().keys).toEqual([
      [0xffe3, "ControlLeft", true],
      [0x6c, "KeyL", false],
    ]);
  });

  it("sends nothing in read-only mode and resumes when it is lifted", async () => {
    const { client } = await connected();
    client.setViewOnly(true);
    client.sendKey("KeyA", true);
    client.sendText("hi");
    await client.sendCombo(["ControlLeft", "AltLeft", "Delete"]);
    expect(rfb().keys).toEqual([]);
    expect(rfb().ctrlAltDel).toBe(0);
    client.setViewOnly(false);
    client.sendKey("KeyA", true);
    expect(rfb().keys).toHaveLength(1);
  });

  it("uses noVNC's Ctrl+Alt+Del for exactly that combo", async () => {
    const { client } = await connected();
    await client.sendCombo(["ControlLeft", "AltLeft", "Delete"]);
    expect(rfb().ctrlAltDel).toBe(1);
    expect(rfb().keys).toEqual([]);
  });

  it("presses a combo in order and releases it in reverse", async () => {
    const { client } = await connected();
    await client.sendCombo(["AltLeft", "Tab"]);
    expect(rfb().keys.map(([, code, down]) => `${code}:${down}`)).toEqual(["AltLeft:true", "Tab:true", "Tab:false", "AltLeft:false"]);
  });

  it("types text as press and release pairs, using Unicode keysyms beyond Latin-1", async () => {
    const { client } = await connected();
    client.sendText("aé中");
    expect(rfb().keys).toEqual([
      [0x61, null, true],
      [0x61, null, false],
      [0xe9, null, true],
      [0xe9, null, false],
      [0x01000000 + 0x4e2d, null, true],
      [0x01000000 + 0x4e2d, null, false],
    ]);
  });

  it("focuses without scrolling the page and blurs", async () => {
    const { client } = await connected();
    client.focus();
    client.blur();
    expect(rfb().focusOptions).toEqual([{ preventScroll: true }]);
    expect(rfb().blurs).toBe(1);
  });
});

describe("VncClient clipboard", () => {
  function stubClipboard(clipboard: Partial<Clipboard>) {
    vi.stubGlobal("navigator", { clipboard });
  }

  it("sends the local clipboard text to the remote", async () => {
    const readText = vi.fn(async () => "from local");
    stubClipboard({ readText });
    const { client } = await connected();
    expect(await client.sendClipboard()).toEqual({ status: "done" });
    expect(rfb().pasted).toEqual(["from local"]);

    const data = { files: [], types: ["text/plain"], getData: (type: string) => (type === "text/plain" ? "from event" : "") } as unknown as DataTransfer;
    expect(await client.sendClipboard(data)).toEqual({ status: "done" });
    expect(rfb().pasted).toEqual(["from local", "from event"]);
    expect(readText).toHaveBeenCalledTimes(1);
  });

  it("says when the clipboard is empty, refused, read-only, or turned off", async () => {
    const readText = vi.fn(async () => "");
    stubClipboard({ readText });
    const { client } = await connected();
    expect(await client.sendClipboard()).toEqual({ status: "empty" });
    readText.mockRejectedValueOnce(new DOMException("denied", "NotAllowedError"));
    expect(await client.sendClipboard()).toEqual({ status: "refused" });
    readText.mockResolvedValue("x");
    client.setViewOnly(true);
    expect(await client.sendClipboard()).toEqual({ status: "refused" });
    expect(rfb().pasted).toEqual([]);

    mocks.rfbs.length = 0;
    const off = new VncClient(stage, options({ clipboard: false }), callbacks());
    const pending = off.connect();
    await vi.waitFor(() => expect(mocks.rfbs).toHaveLength(1));
    rfb().emit("connect");
    await pending;
    expect(await off.sendClipboard()).toEqual({ status: "refused" });
    expect(await off.fetchClipboard()).toEqual({ status: "refused" });
  });

  it("sends nothing for an image or file paste, and says VNC carries text only", async () => {
    stubClipboard({});
    const { client } = await connected();
    const image = new File([new Uint8Array([1])], "a.png", { type: "image/png" });
    const data = { files: [image], types: ["Files"], getData: () => "" } as unknown as DataTransfer;
    const outcome = await client.sendClipboard(data);
    expect(outcome.status).toBe("unsupported");
    expect(outcome.note).toBeTruthy();
    expect(rfb().pasted).toEqual([]);
  });

  it("warns that non-Latin-1 text turns into question marks without the extended clipboard", async () => {
    stubClipboard({});
    const { client } = await connected();
    const data = { files: [], types: ["text/plain"], getData: () => "中文" } as unknown as DataTransfer;
    const classic = await client.sendClipboard(data);
    expect(classic.status).toBe("done");
    expect(classic.note).toBeTruthy();
    expect((await client.sendClipboard({ ...data, getData: () => "latin é" } as unknown as DataTransfer)).note).toBeUndefined();
    // A server that negotiated the extended clipboard (text format, notify action) carries UTF-8.
    Object.assign(rfb(), { _clipboardServerCapabilitiesFormats: { 1: true }, _clipboardServerCapabilitiesActions: { [1 << 27]: true } });
    expect((await client.sendClipboard(data)).note).toBeUndefined();
  });

  it("writes a remote copy to the local clipboard only while the tab is in front and the window has focus", async () => {
    const writeText = vi.fn(async () => undefined);
    stubClipboard({ writeText });
    const focus = vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const { client } = await connected();
    rfb().emit("clipboard", { text: "first" });
    expect(writeText).toHaveBeenCalledWith("first");
    client.setForeground(false);
    rfb().emit("clipboard", { text: "background" });
    client.setForeground(true);
    focus.mockReturnValue(false);
    rfb().emit("clipboard", { text: "unfocused" });
    expect(writeText).toHaveBeenCalledTimes(1);
    focus.mockRestore();
  });

  it("tells the pane when an automatic write of a remote copy is refused", async () => {
    stubClipboard({ writeText: vi.fn(async () => Promise.reject(new DOMException("denied", "NotAllowedError"))) });
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    const { cb } = await connected();
    rfb().emit("clipboard", { text: "t" });
    await vi.waitFor(() => expect(cb.onClipboardNotice).toHaveBeenCalledTimes(1));
  });

  it("keeps the text the remote copied and writes it to the local clipboard only when fetched with sync off", async () => {
    const writeText = vi.fn(async () => undefined);
    stubClipboard({ writeText });
    const { client } = await connected(options({ clipboardAutoSync: false }));
    expect(await client.fetchClipboard()).toEqual({ status: "empty" });
    rfb().emit("clipboard", { text: "from remote" });
    expect(writeText).not.toHaveBeenCalled();
    expect(await client.fetchClipboard()).toEqual({ status: "done" });
    expect(writeText).toHaveBeenCalledWith("from remote");
  });

  it("reports a refusal when the browser refuses to write the clipboard", async () => {
    stubClipboard({ writeText: vi.fn(async () => Promise.reject(new DOMException("denied", "NotAllowedError"))) });
    const { client } = await connected(options({ clipboardAutoSync: false }));
    rfb().emit("clipboard", { text: "t" });
    expect(await client.fetchClipboard()).toEqual({ status: "refused" });
  });
});

describe("VncClient picture", () => {
  it("reports the canvas size and tells the pane when the desktop size changes", async () => {
    const before = await connected();
    expect(before.client.desktopSize()).toEqual({ width: 0, height: 0 });
    expect(before.client.inputElement()).toBeNull();
    expect(before.client.thumbnail(100)).toBeNull();
    before.client.disconnect();
    mocks.rfbs.length = 0;

    // noVNC's canvas exists once the session is up and is resized to the framebuffer when the server changes it.
    const canvas = document.createElement("canvas");
    canvas.width = 1024;
    canvas.height = 768;
    stage.append(canvas);
    const { client, cb } = await connected();
    expect(client.inputElement()).toBe(canvas);
    expect(client.desktopSize()).toEqual({ width: 1024, height: 768 });
    expect(cb.onDesktopSize).toHaveBeenCalledWith(1024, 768);
    canvas.width = 1280;
    await vi.waitFor(() => expect(cb.onDesktopSize).toHaveBeenLastCalledWith(1280, 768));
  });
});

