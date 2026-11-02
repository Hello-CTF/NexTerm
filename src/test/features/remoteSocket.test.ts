/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  PRIVATE_RDP_PROXY,
  PreambleChannel,
  REMOTE_DESKTOP_STREAM,
  TICKET_PREAMBLE,
  installRdpSocketShim,
  resetRemoteSocketForTest,
  setStreamFactory,
  type SocketLike,
} from "../../features/remote/remoteSocket";

// The property check noVNC's Websock.attach runs on a raw channel (core/websock.js).
const NOVNC_CHANNEL_PROPS = ["send", "close", "binaryType", "onerror", "onmessage", "onopen", "protocol", "readyState"];
function passesNoVncAttachCheck(channel: object): boolean {
  const props = [...Object.keys(channel), ...Object.getOwnPropertyNames(Object.getPrototypeOf(channel))];
  return NOVNC_CHANNEL_PROPS.every((name) => props.includes(name));
}

class FakeSocket extends EventTarget implements SocketLike {
  readyState = 0;
  binaryType = "blob";
  protocol = "";
  sent: Array<string | ArrayBufferLike | ArrayBufferView> = [];
  closed = false;
  send(data: string | ArrayBufferLike | ArrayBufferView): void {
    if (this.readyState === 0) throw new DOMException("Still in CONNECTING state.", "InvalidStateError");
    this.sent.push(data);
  }
  close(): void {
    this.closed = true;
    this.readyState = 3;
    this.dispatchEvent(new Event("close"));
  }
  open(): void {
    this.readyState = 1;
    this.dispatchEvent(new Event("open"));
  }
  receive(data: unknown): void {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }
}

afterEach(() => {
  resetRemoteSocketForTest();
  vi.restoreAllMocks();
});

describe("PreambleChannel", () => {
  it("passes the property check noVNC runs on a raw channel", () => {
    expect(passesNoVncAttachCheck(new PreambleChannel(new FakeSocket(), null))).toBe(true);
  });

  it("sends the ticket frame once the socket opens, before it reports open", async () => {
    const socket = new FakeSocket();
    const channel = new PreambleChannel(socket, `${TICKET_PREAMBLE}abc`);
    expect(channel.readyState).toBe(0);
    const ready = channel.whenReady();
    socket.open();
    await ready;
    expect(socket.sent).toEqual(["NXRD1:abc"]);
    expect(channel.readyState).toBe(1);
    expect(socket.binaryType).toBe("arraybuffer");
  });

  it("does not report open when the ticket could not be sent", () => {
    const socket = new FakeSocket();
    vi.spyOn(socket, "send").mockImplementation(() => {
      throw new Error("boom");
    });
    const channel = new PreambleChannel(socket, "NXRD1:abc");
    socket.open();
    expect(socket.closed).toBe(true);
    expect(channel.readyState).toBe(3);
  });

  it("passes everything through when the ticket travels in the URL", async () => {
    const socket = new FakeSocket();
    const channel = new PreambleChannel(socket, null);
    socket.open();
    await channel.whenReady();
    expect(socket.sent).toEqual([]);
    channel.send("hello");
    expect(socket.sent).toEqual(["hello"]);
  });

  it("keeps frames that arrive before the consumer attaches and replays them after its set-up", async () => {
    const socket = new FakeSocket();
    const channel = new PreambleChannel(socket, "NXRD1:abc");
    socket.open();
    socket.receive("RFB 003.008\n");
    socket.receive("second");
    const seen: unknown[] = [];
    channel.onmessage = (event) => seen.push((event as MessageEvent).data);
    // Not inside the assignment: the consumer is still setting itself up.
    expect(seen).toEqual([]);
    await Promise.resolve();
    expect(seen).toEqual(["RFB 003.008\n", "second"]);
    socket.receive("third");
    expect(seen).toHaveLength(3);
  });

  it("delivers a close that happened before the consumer attached", async () => {
    const socket = new FakeSocket();
    const channel = new PreambleChannel(socket, null);
    socket.open();
    socket.close();
    const onclose = vi.fn();
    channel.onclose = onclose;
    await Promise.resolve();
    expect(onclose).toHaveBeenCalledTimes(1);
  });

  it("rejects whenReady when the socket closes first", async () => {
    const socket = new FakeSocket();
    const channel = new PreambleChannel(socket, null);
    const ready = channel.whenReady();
    socket.close();
    await expect(ready).rejects.toThrow();
  });
});

describe("RDP WebSocket shim", () => {
  function fakeWindow() {
    const created: Array<{ url: string }> = [];
    class NativeWebSocket {
      static readonly OPEN = 1;
      constructor(public url: string) {
        created.push({ url });
      }
    }
    return { target: { WebSocket: NativeWebSocket as unknown as typeof WebSocket }, created, Native: NativeWebSocket };
  }

  it("sends only the private scheme to the stream and every other address to the original constructor", () => {
    const { target, created, Native } = fakeWindow();
    const stream = { fake: true };
    const factory = vi.fn(() => stream);
    setStreamFactory(factory);
    installRdpSocketShim(target, true);

    const proxied = new target.WebSocket(PRIVATE_RDP_PROXY);
    expect(proxied).toBe(stream);
    expect(factory).toHaveBeenCalledWith(REMOTE_DESKTOP_STREAM);
    expect(created).toHaveLength(0);

    const other = new target.WebSocket("wss://example.test/ws/events");
    expect(other).toBeInstanceOf(Native);
    expect(created).toEqual([{ url: "wss://example.test/ws/events" }]);
    expect(factory).toHaveBeenCalledTimes(1);
  });

  it("keeps the original statics and instanceof working", () => {
    const { target } = fakeWindow();
    setStreamFactory(() => ({}));
    installRdpSocketShim(target, true);
    expect((target.WebSocket as unknown as { OPEN: number }).OPEN).toBe(1);
    expect(new target.WebSocket("ws://x") instanceof target.WebSocket).toBe(true);
  });

  it("installs once", () => {
    const { target } = fakeWindow();
    installRdpSocketShim(target, true);
    const first = target.WebSocket;
    installRdpSocketShim(target, true);
    expect(target.WebSocket).toBe(first);
  });

  it("is not installed outside the desktop and mobile apps", () => {
    const { target } = fakeWindow();
    const original = target.WebSocket;
    installRdpSocketShim(target);
    expect(target.WebSocket).toBe(original);
  });

  it("fails loudly when the stream is not ready instead of dialing a bogus address", () => {
    const { target } = fakeWindow();
    installRdpSocketShim(target, true);
    expect(() => new target.WebSocket(PRIVATE_RDP_PROXY)).toThrow();
  });
});
