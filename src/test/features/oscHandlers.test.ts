import { describe, expect, it, vi } from "vitest";
import {
  createOsc9Notifier,
  createOsc52Handler,
  decodeOsc52Payload,
} from "../../features/terminal/oscHandlers";

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
  const deniedDeps = () => {
    let on = false;
    let clock = 0;
    const writeText = vi.fn<(text: string) => Promise<void>>(() => Promise.resolve());
    const onDenied = vi.fn();
    const onError = vi.fn();
    const handler = createOsc52Handler({
      enabled: () => on,
      writeText,
      onDenied,
      onError,
      now: () => clock,
    });
    return {
      handler,
      writeText,
      onDenied,
      onError,
      enable: () => {
        on = true;
      },
      tick: (ms: number) => {
        clock += ms;
      },
    };
  };

  it("denies the write and notifies when the setting is off", () => {
    const d = deniedDeps();
    d.handler("c;aGVsbG8=");
    expect(d.writeText).not.toHaveBeenCalled();
    expect(d.onDenied).toHaveBeenCalledTimes(1);
    expect(d.onError).not.toHaveBeenCalled();
  });

  it("throttles repeated denials but reports again after the interval", () => {
    const d = deniedDeps();
    d.handler("c;YQ==");
    d.handler("c;Yg==");
    expect(d.onDenied).toHaveBeenCalledTimes(1);
    d.tick(4000);
    d.handler("c;Yw==");
    expect(d.onDenied).toHaveBeenCalledTimes(2);
    expect(d.writeText).not.toHaveBeenCalled();
  });

  it("writes the decoded text when the setting is on", () => {
    const d = deniedDeps();
    d.enable();
    d.handler("c;aGVsbG8=");
    expect(d.writeText).toHaveBeenCalledWith("hello");
    expect(d.onDenied).not.toHaveBeenCalled();
  });

  it("reports clipboard permission failures honestly", async () => {
    const d = deniedDeps();
    d.enable();
    const boom = new Error("clipboard permission denied");
    d.writeText.mockRejectedValueOnce(boom);
    d.handler("c;aGVsbG8=");
    await Promise.resolve();
    expect(d.onError).toHaveBeenCalledWith(boom);
    expect(d.onDenied).not.toHaveBeenCalled();
  });

  it("throttles repeated write failures", async () => {
    const d = deniedDeps();
    d.enable();
    d.writeText.mockRejectedValue(new Error("denied"));
    d.handler("c;YQ==");
    d.handler("c;Yg==");
    await Promise.resolve();
    await Promise.resolve();
    expect(d.onError).toHaveBeenCalledTimes(1);
    d.tick(4000);
    d.handler("c;Yw==");
    await Promise.resolve();
    expect(d.onError).toHaveBeenCalledTimes(2);
  });

  it("ignores queries and malformed payloads without any side effect", () => {
    const d = deniedDeps();
    d.enable();
    d.handler("c;");
    d.handler("c;!!!bad!!!");
    expect(d.writeText).not.toHaveBeenCalled();
    expect(d.onDenied).not.toHaveBeenCalled();
    expect(d.onError).not.toHaveBeenCalled();
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
