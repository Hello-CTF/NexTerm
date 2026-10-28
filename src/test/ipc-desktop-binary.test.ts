import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createWailsMock, type WailsMock } from "./wailsMock";

const DESKTOP_BINARY_TYPE = "nexterm.desktop-binary";
const FRAME_SIZE = 64 * 1024;

type EventHandler = (event: { data: unknown }) => void;

let runtime: WailsMock;
const nativeBase64Descriptor = Object.getOwnPropertyDescriptor(Uint8Array, "fromBase64");
const base64Decoder = Uint8Array as unknown as { fromBase64?: (text: string) => Uint8Array };

function spyActiveDecoder() {
  return typeof base64Decoder.fromBase64 === "function"
    ? vi.spyOn(base64Decoder, "fromBase64")
    : vi.spyOn(globalThis, "atob");
}

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", undefined);
  runtime = createWailsMock();
  runtime.Events.On.mockImplementation(() => vi.fn());
  vi.doMock("@wailsio/runtime", () => runtime);
});

afterEach(() => {
  vi.doUnmock("@wailsio/runtime");
  if (nativeBase64Descriptor) Object.defineProperty(Uint8Array, "fromBase64", nativeBase64Descriptor);
  else delete base64Decoder.fromBase64;
});

function base64Of(bytes: Uint8Array): string {
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

function sourceBytes(length: number): Uint8Array {
  const bytes = new Uint8Array(length);
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = (index * 31 + Math.floor(index / FRAME_SIZE)) & 0xff;
  }
  return bytes;
}

async function ready(channel: unknown): Promise<void> {
  await (channel as unknown as { ready: Promise<void> }).ready;
}

describe("desktop binary channel wire compatibility", () => {
  it("decodes a typed base64 Wails payload exactly once", async () => {
    const decode = spyActiveDecoder();
    const { createBinaryChannel } = await import("../ipc/events");
    const received: Uint8Array[] = [];
    const channel = createBinaryChannel((bytes) => received.push(bytes));
    await ready(channel);
    const push = runtime.Events.On.mock.calls[0][1] as EventHandler;
    const expected = sourceBytes(FRAME_SIZE);

    push({ data: { type: DESKTOP_BINARY_TYPE, data: base64Of(expected) } });

    expect(decode).toHaveBeenCalledTimes(1);
    expect(received).toHaveLength(1);
    expect(received[0]).toEqual(expected);
  });

  it("replays 4 MiB without loss, corruption, or reordering", async () => {
    const decode = spyActiveDecoder();
    const { createBinaryChannel } = await import("../ipc/events");
    const received: Uint8Array[] = [];
    const channel = createBinaryChannel((bytes) => received.push(bytes));
    await ready(channel);
    const push = runtime.Events.On.mock.calls[0][1] as EventHandler;
    const source = sourceBytes(4 * 1024 * 1024);
    const frameCount = source.length / FRAME_SIZE;

    for (let index = 0; index < frameCount; index += 1) {
      const frame = source.subarray(index * FRAME_SIZE, (index + 1) * FRAME_SIZE);
      push({ data: { type: DESKTOP_BINARY_TYPE, data: base64Of(frame) } });
    }

    expect(decode).toHaveBeenCalledTimes(frameCount);
    expect(received).toHaveLength(frameCount);
    let replayedBytes = 0;
    let mismatch = -1;
    for (let index = 0; index < frameCount && mismatch < 0; index += 1) {
      const frame = received[index];
      for (let offset = 0; offset < FRAME_SIZE; offset += 1) {
        const sourceIndex = index * FRAME_SIZE + offset;
        if (frame[offset] !== source[sourceIndex]) {
          mismatch = sourceIndex;
          break;
        }
      }
      replayedBytes += frame.length;
    }
    expect(mismatch).toBe(-1);
    expect(replayedBytes).toBe(source.length);
  });

  it("unsubscribes once and does not decode typed payloads after disposal", async () => {
    const off = vi.fn();
    runtime.Events.On.mockReturnValue(off);
    const decode = spyActiveDecoder();
    const { createBinaryChannel, disposeChannel } = await import("../ipc/events");
    const received: Uint8Array[] = [];
    const channel = createBinaryChannel((bytes) => received.push(bytes));
    await ready(channel);
    const push = runtime.Events.On.mock.calls[0][1] as EventHandler;

    disposeChannel(channel);
    disposeChannel(channel);
    push({ data: { type: DESKTOP_BINARY_TYPE, data: base64Of(sourceBytes(16)) } });

    expect(off).toHaveBeenCalledTimes(1);
    expect(decode).not.toHaveBeenCalled();
    expect(received).toEqual([]);
  });

  describe("base64 decoding branches", () => {
    it("uses Uint8Array.fromBase64 when the runtime provides it", async () => {
      const expected = sourceBytes(1024);
      const native = vi.fn((_text: string) => expected.slice());
      (Uint8Array as unknown as { fromBase64?: unknown }).fromBase64 = native;
      const decode = vi.spyOn(globalThis, "atob");
      const { createBinaryChannel } = await import("../ipc/events");
      const received: Uint8Array[] = [];
      const channel = createBinaryChannel((bytes) => received.push(bytes));
      await ready(channel);
      const push = runtime.Events.On.mock.calls[0][1] as EventHandler;

      push({ data: { type: DESKTOP_BINARY_TYPE, data: base64Of(expected) } });

      expect(native).toHaveBeenCalledTimes(1);
      expect(decode).not.toHaveBeenCalled();
      expect(received[0]).toEqual(expected);
    });

    it("falls back to atob when fromBase64 is absent, including padding lengths", async () => {
      delete base64Decoder.fromBase64;
      const { createBinaryChannel } = await import("../ipc/events");
      const received: Uint8Array[] = [];
      const channel = createBinaryChannel((bytes) => received.push(bytes));
      await ready(channel);
      const push = runtime.Events.On.mock.calls[0][1] as EventHandler;

      for (const length of [0, 1, 2, 3, 4, 255]) {
        push({ data: { type: DESKTOP_BINARY_TYPE, data: base64Of(sourceBytes(length)) } });
      }

      expect(received.map((bytes) => bytes.length)).toEqual([0, 1, 2, 3, 4, 255]);
      expect(received[5]).toEqual(sourceBytes(255));
    });
  });

  describe("channel ready gate", () => {
    async function pendingChannel() {
      const { newWailsChannel } = await import("../ipc/wails");
      const channel = newWailsChannel();
      await channel.ready;
      let release!: () => void;
      const gate = new Promise<void>((resolve) => {
        release = resolve;
      });
      Object.defineProperty(channel, "ready", { value: gate });
      return { channel, release };
    }

    it("does not make unrelated calls wait for another channel's subscription", async () => {
      runtime.Call.ByName.mockResolvedValue({ ok: true, data: "pong" });
      const { callDesktop } = await import("../ipc/wails");
      await pendingChannel();

      await expect(callDesktop("ping")).resolves.toBe("pong");
    });

    it("holds a call that hands over a channel until that channel is subscribed", async () => {
      runtime.Call.ByName.mockResolvedValue({ ok: true, data: "attached" });
      const { callDesktop } = await import("../ipc/wails");
      const { channel, release } = await pendingChannel();

      const attach = callDesktop("terminal_attach", { tabId: "t1", channel });
      await new Promise((resolve) => setTimeout(resolve, 20));
      expect(runtime.Call.ByName).not.toHaveBeenCalled();

      release();
      await expect(attach).resolves.toBe("attached");
      const sent = runtime.Call.ByName.mock.calls[0][1] as { channel?: unknown };
      expect(JSON.parse(JSON.stringify(sent.channel))).toBe(channel.id);
    });
  });
});
