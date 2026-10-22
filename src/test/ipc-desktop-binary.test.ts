import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createWailsMock, type WailsMock } from "./wailsMock";

const DESKTOP_BINARY_TYPE = "nexterm.desktop-binary";
const FRAME_SIZE = 64 * 1024;

type EventHandler = (event: { data: unknown }) => void;

let runtime: WailsMock;

beforeEach(() => {
  vi.resetModules();
  vi.stubGlobal("window", undefined);
  runtime = createWailsMock();
  runtime.Events.On.mockImplementation(() => vi.fn());
  vi.doMock("@wailsio/runtime", () => runtime);
});

afterEach(() => {
  vi.doUnmock("@wailsio/runtime");
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
    const decode = vi.spyOn(globalThis, "atob");
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
    const decode = vi.spyOn(globalThis, "atob");
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
    const decode = vi.spyOn(globalThis, "atob");
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
});
