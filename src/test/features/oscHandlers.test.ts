import { describe, expect, it, vi } from "vitest";
import {
  createOsc9Notifier,
  createOsc52Handler,
  decodeOsc52Payload,
} from "../../features/terminal/oscHandlers";
import { createOscStreamFilter } from "../../features/terminal/oscStream";

function b64(text: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)));
}

describe("decodeOsc52Payload", () => {
  it("decodes base64 payload with a selection prefix", () => {
    expect(decodeOsc52Payload("c;aGVsbG8=")).toBe("hello");
    expect(decodeOsc52Payload(`c;${b64("剪贴板内容")}`)).toBe("剪贴板内容");
  });

  it("decodes payload without a selection prefix", () => {
    expect(decodeOsc52Payload("aGVsbG8=")).toBe("hello");
  });

  it("refuses clipboard queries and empty payloads", () => {
    expect(decodeOsc52Payload("c;")).toBeNull();
    expect(decodeOsc52Payload("")).toBeNull();
  });

  it("returns null for malformed base64 instead of throwing", () => {
    expect(decodeOsc52Payload("c;!!!not-base64!!!")).toBeNull();
  });
});

describe("createOsc52Handler", () => {
  async function flushAuth(): Promise<void> {
    await Promise.resolve();
    await Promise.resolve();
  }

  function deps(choice: "once" | "session" | "deny" | null) {
    let on = false;
    const writeText = vi.fn<(text: string) => Promise<void>>(() => Promise.resolve());
    const authorize = vi.fn<() => Promise<"once" | "session" | "deny" | null>>(() =>
      Promise.resolve(choice),
    );
    const onError = vi.fn();
    const handler = createOsc52Handler({
      enabled: () => on,
      writeText,
      authorize,
      onError,
    });
    return {
      handler,
      writeText,
      authorize,
      onError,
      enable: () => {
        on = true;
      },
    };
  }

  it("writes directly without asking when the setting is on", () => {
    const d = deps("deny");
    d.enable();
    d.handler("c;aGVsbG8=");
    expect(d.writeText).toHaveBeenCalledWith("hello");
    expect(d.authorize).not.toHaveBeenCalled();
  });

  it("asks once and keeps writing for the rest of the session after 本会话允许", async () => {
    const d = deps("session");
    d.handler("c;aGVsbG8=");
    await flushAuth();
    expect(d.authorize).toHaveBeenCalledTimes(1);
    expect(d.writeText).toHaveBeenCalledWith("hello");
    d.handler("c;YQ==");
    expect(d.writeText).toHaveBeenCalledWith("a");
    expect(d.authorize).toHaveBeenCalledTimes(1);
    expect(d.onError).not.toHaveBeenCalled();
  });

  it("允许本次 writes only that payload and asks again on the next one", async () => {
    const d = deps("once");
    d.handler("c;aGVsbG8=");
    await flushAuth();
    expect(d.writeText).toHaveBeenCalledWith("hello");
    d.handler("c;YQ==");
    expect(d.authorize).toHaveBeenCalledTimes(2);
    await flushAuth();
    expect(d.writeText).toHaveBeenLastCalledWith("a");
    expect(d.writeText).toHaveBeenCalledTimes(2);
  });

  it("拒绝 drops the payload silently and never asks again for the session", async () => {
    const d = deps("deny");
    d.handler("c;aGVsbG8=");
    await flushAuth();
    expect(d.writeText).not.toHaveBeenCalled();
    d.handler("c;YQ==");
    d.handler("c;Yg==");
    expect(d.authorize).toHaveBeenCalledTimes(1);
    expect(d.writeText).not.toHaveBeenCalled();
  });

  it("a dismissed dialog drops that payload but asks again next time", async () => {
    const d = deps(null);
    d.handler("c;aGVsbG8=");
    await flushAuth();
    expect(d.writeText).not.toHaveBeenCalled();
    d.handler("c;YQ==");
    expect(d.authorize).toHaveBeenCalledTimes(2);
  });

  it("drops payloads that arrive while the authorization dialog is open", async () => {
    let resolveAuth!: (v: "once" | "session" | "deny" | null) => void;
    const writeText = vi.fn<(text: string) => Promise<void>>(() => Promise.resolve());
    const authorize = vi.fn<() => Promise<"once" | "session" | "deny" | null>>(
      () => new Promise((r) => (resolveAuth = r)),
    );
    const handler = createOsc52Handler({
      enabled: () => false,
      writeText,
      authorize,
      onError: vi.fn(),
    });
    handler("c;aGVsbG8=");
    handler("c;YQ==");
    expect(authorize).toHaveBeenCalledTimes(1);
    resolveAuth("session");
    await flushAuth();
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText).toHaveBeenCalledWith("hello");
  });

  it("reports clipboard permission failures honestly", async () => {
    const d = deps("session");
    d.enable();
    const boom = new Error("clipboard permission denied");
    d.writeText.mockRejectedValueOnce(boom);
    d.handler("c;aGVsbG8=");
    await Promise.resolve();
    expect(d.onError).toHaveBeenCalledWith(boom);
  });

  it("throttles repeated write failures", async () => {
    let clock = 0;
    const writeText = vi.fn<(text: string) => Promise<void>>(() =>
      Promise.reject(new Error("denied")),
    );
    const onError = vi.fn();
    const handler = createOsc52Handler({
      enabled: () => true,
      writeText,
      authorize: vi.fn(),
      onError,
      now: () => clock,
    });
    handler("c;YQ==");
    handler("c;Yg==");
    await Promise.resolve();
    await Promise.resolve();
    expect(onError).toHaveBeenCalledTimes(1);
    clock = 4000;
    handler("c;Yw==");
    await Promise.resolve();
    expect(onError).toHaveBeenCalledTimes(2);
  });

  it("ignores queries and malformed payloads without any side effect", async () => {
    const d = deps("session");
    d.handler("c;");
    d.handler("c;!!!bad!!!");
    await flushAuth();
    expect(d.writeText).not.toHaveBeenCalled();
    expect(d.authorize).not.toHaveBeenCalled();
    expect(d.onError).not.toHaveBeenCalled();
  });

  it("routes a synchronous writeText throw into onError instead of propagating", () => {
    const onError = vi.fn();
    const syncBoom = () => {
      throw new TypeError("Cannot read properties of undefined (reading 'writeText')");
    };
    const handler = createOsc52Handler({
      enabled: () => true,
      writeText: syncBoom,
      authorize: vi.fn(),
      onError,
    });
    expect(() => handler("c;aGVsbG8=")).not.toThrow();
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0][0]).toBeInstanceOf(TypeError);
  });

  it("throttles synchronous writeText throws like rejections", () => {
    let clock = 0;
    const onError = vi.fn();
    const handler = createOsc52Handler({
      enabled: () => true,
      writeText: () => {
        throw new Error("clipboard unavailable");
      },
      authorize: vi.fn(),
      onError,
      now: () => clock,
    });
    handler("c;YQ==");
    handler("c;Yg==");
    expect(onError).toHaveBeenCalledTimes(1);
    clock = 4000;
    handler("c;Yw==");
    expect(onError).toHaveBeenCalledTimes(2);
  });

  it("a missing navigator.clipboard never aborts the terminal chunk", () => {
    const onError = vi.fn();
    const handler = createOsc52Handler({
      enabled: () => true,
      writeText: (text) => navigator.clipboard.writeText(text),
      authorize: vi.fn(),
      onError,
    });
    const filter = createOscStreamFilter();
    const fed: string[] = [];
    for (const segment of filter.push(new TextEncoder().encode("A\x1b]52;c;aGVsbG8=\x07B"))) {
      if (segment.kind === "event") {
        if (segment.event.kind === "clipboard") handler(segment.event.payload);
      } else {
        fed.push(new TextDecoder().decode(segment.data));
      }
    }
    expect(fed.join("")).toBe("AB");
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0][0]).toBeInstanceOf(TypeError);
  });
});

describe("createOsc9Notifier", () => {
  it("trims, collapses whitespace and drops empty bodies", () => {
    const notify = vi.fn();
    const notifier = createOsc9Notifier({ notify, now: () => 0 });
    notifier("  build\ndone  ");
    expect(notify).toHaveBeenCalledWith("build done");
    notifier("   ");
    expect(notify).toHaveBeenCalledTimes(1);
  });

  it("caps very long bodies", () => {
    const notify = vi.fn();
    const notifier = createOsc9Notifier({ notify, now: () => 0 });
    notifier("x".repeat(500));
    expect(notify).toHaveBeenCalledWith("x".repeat(200));
  });

  it("throttles bursts but delivers after the interval", () => {
    const notify = vi.fn();
    let clock = 0;
    const notifier = createOsc9Notifier({ notify, now: () => clock, throttleMs: 1500 });
    notifier("one");
    notifier("two");
    expect(notify).toHaveBeenCalledTimes(1);
    clock = 1500;
    notifier("three");
    expect(notify).toHaveBeenCalledTimes(2);
    expect(notify).toHaveBeenLastCalledWith("three");
  });
});
