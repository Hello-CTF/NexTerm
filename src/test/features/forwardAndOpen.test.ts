/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  createTemporary: vi.fn(),
  openTerminalLink: vi.fn(),
  web: { value: false },
  pushToast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, forwardApi: { ...actual.forwardApi, list: mocks.list, createTemporary: mocks.createTemporary } };
});
vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return {
    ...actual,
    get WEB() {
      return mocks.web.value;
    },
  };
});
vi.mock("../../features/terminal/terminalLinks", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/terminal/terminalLinks")>();
  return { ...actual, openTerminalLink: mocks.openTerminalLink };
});

import { ensureTemporaryForward, forwardAndOpen, targetHostFor } from "../../features/preview/forwardAndOpen";
import { useUi } from "../../app/store";

const spec = (extra: Record<string, unknown> = {}) => ({
  id: "f1",
  sessionId: "s1",
  status: "active",
  listenPort: 41000,
  targetHost: "127.0.0.1",
  targetPort: 3000,
  kind: "local",
  createdAt: 1,
  temporary: true,
  ...extra,
});

afterEach(() => vi.unstubAllGlobals());

beforeEach(() => {
  vi.clearAllMocks();
  mocks.web.value = false;
  mocks.list.mockResolvedValue([]);
  mocks.createTemporary.mockResolvedValue(spec());
  useUi.setState({ pushToast: mocks.pushToast } as never);
});

describe("targetHostFor", () => {
  it("maps wildcard and loopback binds to 127.0.0.1", () => {
    for (const address of ["0.0.0.0", "*", "127.0.0.1", "127.1.2.3"]) {
      expect(targetHostFor({ address, port: 80 })).toBe("127.0.0.1");
    }
  });
  it("maps IPv6 wildcard and loopback to ::1 unless the port also listens on IPv4", () => {
    expect(targetHostFor({ address: "::", port: 80 })).toBe("::1");
    expect(targetHostFor({ address: "::1", port: 80 })).toBe("::1");
    expect(targetHostFor({ address: "::", port: 80 }, [{ address: "0.0.0.0", port: 80 }])).toBe("127.0.0.1");
    expect(targetHostFor({ address: "::", port: 80 }, [{ address: "0.0.0.0", port: 81 }])).toBe("::1");
  });
  it("keeps a specific address", () => {
    expect(targetHostFor({ address: "10.0.0.5", port: 80 })).toBe("10.0.0.5");
  });
});

describe("ensureTemporaryForward", () => {
  it("reuses an active temporary forward to the same target", async () => {
    mocks.list.mockResolvedValue([spec()]);
    const out = await ensureTemporaryForward("s1", "127.0.0.1", 3000);
    expect(out.reused).toBe(true);
    expect(mocks.createTemporary).not.toHaveBeenCalled();
  });
  it("does not reuse a permanent, failed or other-session forward", async () => {
    mocks.list.mockResolvedValue([spec({ temporary: false }), spec({ status: "failed" }), spec({ sessionId: "s2" })]);
    await ensureTemporaryForward("s1", "127.0.0.1", 3000);
    expect(mocks.createTemporary).toHaveBeenCalledWith("s1", "127.0.0.1", 3000);
  });
});

describe("forwardAndOpen", () => {
  it("opens the allocated local port", async () => {
    await forwardAndOpen({ sessionId: "s1", host: "127.0.0.1", port: 3000 });
    expect(mocks.openTerminalLink).toHaveBeenCalledWith("http://127.0.0.1:41000/", expect.anything());
  });
  it("does not open in browser mode when the page is not served from loopback, and says so", async () => {
    mocks.web.value = true;
    vi.stubGlobal("location", { hostname: "nexterm.example.com" });
    await forwardAndOpen({ sessionId: "s1", host: "127.0.0.1", port: 3000 });
    expect(mocks.openTerminalLink).not.toHaveBeenCalled();
    const message = mocks.pushToast.mock.calls.map((c) => String(c[1])).join("\n");
    expect(message).toContain("127.0.0.1:41000");
  });
  it("opens in browser mode when the page is served from loopback", async () => {
    mocks.web.value = true;
    vi.stubGlobal("location", { hostname: "localhost" });
    await forwardAndOpen({ sessionId: "s1", host: "127.0.0.1", port: 3000 });
    expect(mocks.openTerminalLink).toHaveBeenCalledWith("http://127.0.0.1:41000/", expect.anything());
  });
  it("reports a failure to create the forward", async () => {
    mocks.createTemporary.mockRejectedValue(new Error("nope"));
    await forwardAndOpen({ sessionId: "s1", host: "127.0.0.1", port: 3000 });
    expect(mocks.openTerminalLink).not.toHaveBeenCalled();
    expect(mocks.pushToast).toHaveBeenCalledWith("error", expect.any(String));
  });
});
