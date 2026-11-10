import { afterEach, describe, expect, it, vi } from "vitest";
import type { TrzszOptions } from "trzsz";
import { createTrzszSession, currentTrzszRequestContext, reportTrzszDeclined, type TrzszFilterLike, type TrzszSessionOptions } from "../features/terminal/trzsz/trzszSession";
import { indexOfTrzszMagic, nextTrzszTail, trzszRequestIn } from "../features/terminal/trzsz/trzszDetect";

const encoder = new TextEncoder();
const bytes = (text: string) => encoder.encode(text);
const REQUEST = "::TRZSZ:TRANSFER:S:1.1.6:1234567890123\r\n";

// Stands in for trzsz.js: a request turns into a transfer, which collects
// output and input until it is stopped.
class FakeFilter implements TrzszFilterLike {
  transferring = false;
  received: Uint8Array[] = [];
  input: string[] = [];
  stopped = 0;
  constructor(readonly options: TrzszOptions) {}
  processServerOutput(output: string | ArrayBuffer | Uint8Array | Blob): void {
    if (this.transferring) {
      this.received.push(output as Uint8Array);
      return;
    }
    if (indexOfTrzszMagic(output as Uint8Array) >= 0) this.transferring = true;
    this.options.writeToTerminal?.(output);
  }
  processTerminalInput(input: string): void {
    this.input.push(input);
    if (input === "\u0003") this.stopTransferringFiles();
  }
  setTerminalColumns(): void {}
  isTransferringFiles(): boolean {
    return this.transferring;
  }
  stopTransferringFiles(): void {
    this.stopped += 1;
    this.transferring = false;
  }
}

function setup(overrides: Partial<TrzszSessionOptions> = {}) {
  const filters: FakeFilter[] = [];
  const local: string[] = [];
  const sent: Uint8Array[] = [];
  const activity: boolean[] = [];
  const loadFactory = vi.fn(async () => (options: TrzszOptions) => {
    const filter = new FakeFilter(options);
    filters.push(filter);
    return filter;
  });
  const session = createTrzszSession({
    enabled: () => true,
    inputOwner: () => true,
    windowsShell: () => false,
    columns: () => 80,
    host: () => "db-1",
    sendToServer: (data) => sent.push(data),
    writeLocal: (text) => local.push(text),
    onActiveChange: (active) => activity.push(active),
    loadFactory,
    pollMs: 10,
    ...overrides,
  });
  sessions.push(session);
  return { session, filters, local, sent, activity, loadFactory };
}

const sessions: ReturnType<typeof createTrzszSession>[] = [];

afterEach(() => {
  for (const session of sessions.splice(0)) session.dispose();
  vi.useRealTimers();
});

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("trzsz request detection", () => {
  it("finds the request prefix, also across a chunk boundary", () => {
    expect(indexOfTrzszMagic(bytes(`ls\r\n${REQUEST}`))).toBe(4);
    expect(indexOfTrzszMagic(bytes("a:b::TRZSZ:TRANS"))).toBe(-1);
    const first = bytes("output ::TRZSZ:TRA");
    const second = bytes("NSFER:S:1.1.6:42\r\n");
    expect(trzszRequestIn(new Uint8Array(0), first)).toBeNull();
    const tail = nextTrzszTail(new Uint8Array(0), first);
    const joined = trzszRequestIn(tail, second);
    expect(new TextDecoder().decode(joined!)).toBe("::TRZSZ:TRANSFER:S:1.1.6:42\r\n");
    // A request already seen whole in the previous chunk is not reported again.
    expect(trzszRequestIn(nextTrzszTail(new Uint8Array(0), bytes(REQUEST)), bytes("next"))).toBeNull();
  });
});

describe("trzsz session", () => {
  it("does not load trzsz.js or touch output without a request", () => {
    const { session, loadFactory } = setup();
    expect(session.handleOutput(bytes("plain output\r\n"), "live")).toBe(false);
    expect(session.handleInput("ls\r")).toBe(false);
    expect(loadFactory).not.toHaveBeenCalled();
  });

  it("starts only from live output, never from replayed or answered history", async () => {
    const { session, loadFactory } = setup();
    expect(session.handleOutput(bytes(REQUEST), "replay")).toBe(false);
    expect(session.handleOutput(bytes(REQUEST), "answered")).toBe(false);
    await flush();
    expect(loadFactory).not.toHaveBeenCalled();
    expect(session.active()).toBe(false);
  });

  it("does nothing while turned off", async () => {
    const { session, loadFactory } = setup({ enabled: () => false });
    expect(session.handleOutput(bytes(REQUEST), "live")).toBe(false);
    await flush();
    expect(loadFactory).not.toHaveBeenCalled();
  });

  it("hands a live request to trzsz.js and then consumes the transfer stream and input", async () => {
    const { session, filters, local, activity } = setup();
    // The request itself is still written to xterm by the caller.
    expect(session.handleOutput(bytes(REQUEST), "live")).toBe(false);
    await flush();
    expect(filters).toHaveLength(1);
    expect(session.active()).toBe(true);
    // trzsz.js echoing the request chunk does not write it a second time.
    expect(local).toEqual([]);
    expect(session.handleOutput(bytes("#DATA:abc\n"), "live")).toBe(true);
    // Output produced while this view was briefly detached belongs to the transfer too.
    expect(session.handleOutput(bytes("#DATA:def\n"), "answered")).toBe(true);
    expect(filters[0].received).toHaveLength(2);
    expect(session.handleInput("x")).toBe(true);
    expect(filters[0].input).toEqual(["x"]);
    await vi.waitFor(() => expect(activity).toEqual([true]));
  });

  it("cancels with Ctrl+C or the cancel action and returns output to xterm", async () => {
    const { session, filters, activity } = setup();
    session.handleOutput(bytes(REQUEST), "live");
    await flush();
    expect(session.handleInput("\u0003")).toBe(true);
    expect(filters[0].stopped).toBe(1);
    expect(session.handleOutput(bytes("prompt$ "), "live")).toBe(false);
    session.handleOutput(bytes(REQUEST.replace("123", "999")), "live");
    expect(session.active()).toBe(true);
    session.cancel();
    expect(filters[0].stopped).toBe(2);
    expect(session.active()).toBe(false);
    await vi.waitFor(() => expect(activity.at(-1)).toBe(false));
  });

  it("stops a transfer when replayed history arrives", async () => {
    const { session, filters } = setup();
    session.handleOutput(bytes(REQUEST), "live");
    await flush();
    expect(session.handleOutput(bytes("history"), "replay")).toBe(false);
    expect(filters[0].stopped).toBe(1);
  });

  it("leaves the transfer to the device that typed last", async () => {
    const { session, local, loadFactory } = setup({ inputOwner: () => false });
    expect(session.handleOutput(bytes(REQUEST), "live")).toBe(false);
    await flush();
    expect(loadFactory).not.toHaveBeenCalled();
    expect(local.join("")).toContain("[trzsz]");
  });

  it("names the host for a picker request only when one terminal is transferring", async () => {
    const one = setup({ host: () => "web-1" });
    one.session.handleOutput(bytes(REQUEST), "live");
    await flush();
    expect(currentTrzszRequestContext().host).toBe("web-1");
    const two = setup({ host: () => "web-2" });
    two.session.handleOutput(bytes(REQUEST), "live");
    await flush();
    expect(currentTrzszRequestContext().host).toBeNull();
  });

  describe("declined requests", () => {
    async function request(ctx: ReturnType<typeof setup>) {
      ctx.session.handleOutput(bytes(REQUEST), "live");
      await flush();
    }
    const decline = (ctx: ReturnType<typeof setup>) => {
      reportTrzszDeclined();
      ctx.session.cancel();
    };

    it("refuses requests for a while after a decline, then answers again", async () => {
      let now = 1000;
      const ctx = setup({ now: () => now, refuseMs: 30_000 });
      await request(ctx);
      expect(ctx.filters).toHaveLength(1);
      decline(ctx);
      now += 5000;
      ctx.local.length = 0;
      ctx.session.handleOutput(bytes(REQUEST), "live");
      await flush();
      expect(ctx.filters[0].transferring).toBe(false);
      expect(ctx.local.join("")).toContain("trzsz");
      now += 31_000;
      ctx.session.handleOutput(bytes(REQUEST), "live");
      await flush();
      expect(ctx.filters[0].transferring).toBe(true);
    });

    it("turns trzsz off for the terminal after three declines", async () => {
      let now = 1000;
      const ctx = setup({ now: () => now });
      for (let i = 0; i < 3; i += 1) {
        ctx.session.handleOutput(bytes(REQUEST), "live");
        await flush();
        expect(ctx.filters[0].transferring).toBe(true);
        ctx.local.length = 0;
        decline(ctx);
        now += 60_000;
      }
      expect(ctx.local.join("")).toContain("trzsz");
      ctx.session.handleOutput(bytes(REQUEST), "live");
      await flush();
      expect(ctx.filters[0].transferring).toBe(false);
    });
  });
});
