import { describe, expect, it, vi } from "vitest";

vi.mock("../ipc/env", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/env")>()),
  WEB: false,
}));
vi.mock("../ipc/wails", () => ({
  disposeWailsChannel: vi.fn(),
  listenWailsEvent: vi.fn(),
  newWailsChannel: vi.fn(),
}));

const text = (bytes: Uint8Array) => new TextDecoder().decode(bytes);

describe("desktop terminal input", () => {
  it("keeps one write in flight per tab and merges input that arrives meanwhile, in order", async () => {
    const { sendChannelInput } = await import("../ipc/events");
    const calls: string[] = [];
    const pending: Array<() => void> = [];
    const http = (data: Uint8Array) => {
      calls.push(text(data));
      return new Promise<void>((resolve) => pending.push(resolve));
    };
    const encode = (value: string) => new TextEncoder().encode(value);

    sendChannelInput(null, "tab-1", encode("a"), http);
    sendChannelInput(null, "tab-1", encode("b"), http);
    sendChannelInput(null, "tab-1", encode("c"), http);
    sendChannelInput(null, "tab-2", encode("x"), http);
    expect(calls).toEqual(["a", "x"]);

    pending.shift()!();
    await vi.waitFor(() => expect(calls).toEqual(["a", "x", "bc"]));
    sendChannelInput(null, "tab-1", encode("d"), http);
    expect(calls).toEqual(["a", "x", "bc"]);

    pending.splice(0).forEach((resolve) => resolve());
    await vi.waitFor(() => expect(calls).toEqual(["a", "x", "bc", "d"]));
  });

  it("continues with queued input after a failed write", async () => {
    const { sendChannelInput } = await import("../ipc/events");
    const calls: string[] = [];
    let reject: ((error: Error) => void) | null = null;
    const http = (data: Uint8Array) => {
      calls.push(text(data));
      if (calls.length === 1) return new Promise<void>((_, fail) => (reject = fail));
      return Promise.resolve();
    };
    const encode = (value: string) => new TextEncoder().encode(value);

    sendChannelInput(null, "tab-3", encode("1"), http);
    sendChannelInput(null, "tab-3", encode("2"), http);
    reject!(new Error("write failed"));
    await vi.waitFor(() => expect(calls).toEqual(["1", "2"]));
  });

  it("reports dropped input when the backlog exceeds the cap and keeps the queued bytes", async () => {
    const { sendChannelInput } = await import("../ipc/events");
    const { setInputFailureHandler } = await import("../ipc/inputFailure");
    const failures: Array<{ error: unknown; connectionLost: boolean }> = [];
    setInputFailureHandler((failure) => failures.push(failure));
    const calls: Uint8Array[] = [];
    const pending: Array<() => void> = [];
    const http = (data: Uint8Array) => {
      calls.push(data);
      return new Promise<void>((resolve) => pending.push(resolve));
    };
    const cap = 4 * 1024 * 1024;
    const fill = (byte: number, size: number) => new Uint8Array(size).fill(byte);

    try {
      sendChannelInput(null, "tab-ovf", fill(1, 1), http);
      sendChannelInput(null, "tab-ovf", fill(2, cap - 10), http);
      sendChannelInput(null, "tab-ovf", fill(3, 100), http);
      expect(failures).toHaveLength(1);
      expect(failures[0].connectionLost).toBe(false);
      expect(failures[0].error).toMatchObject({ code: "input_overflow" });

      pending.shift()!();
      await vi.waitFor(() => expect(calls).toHaveLength(2));
      expect(calls[1].length).toBe(cap - 10);
      expect(calls[1].every((b) => b === 2)).toBe(true);

      // Once the backlog drained the counter is clear, so a large write queues again.
      sendChannelInput(null, "tab-ovf", fill(4, cap - 10), http);
      expect(failures).toHaveLength(1);
      pending.splice(0).forEach((resolve) => resolve());
      await vi.waitFor(() => expect(calls).toHaveLength(3));
      expect(calls[2].length).toBe(cap - 10);
      pending.splice(0).forEach((resolve) => resolve());
    } finally {
      setInputFailureHandler(null);
    }
  });
});
