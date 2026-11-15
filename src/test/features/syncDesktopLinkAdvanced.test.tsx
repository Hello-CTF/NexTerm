/** @vitest-environment jsdom */
// Desktop account login form: an address without a scheme is completed as HTTPS, and the collapsed advanced
// options carry the manual proxy (HTTP / SOCKS4 / SOCKS5 with optional credentials) next to certificate skipping.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { chooseSelectOption, click, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    linkGet: vi.fn(),
    status: vi.fn(),
    kindOptInGet: vi.fn(),
    linkSet: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: { ...actual.syncApi, linkGet: mocks.linkGet, status: mocks.status, kindOptInGet: mocks.kindOptInGet },
  };
});
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return { ...actual, remoteLinkApi: { linkSet: mocks.linkSet, mfa: vi.fn(), resetPassword: vi.fn() } };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: vi.fn(async () => () => undefined) };
});
vi.mock("../../features/settings/AuthCard", () => ({ AuthCard: () => null }));
vi.mock("../../features/settings/AccountCard", () => ({ AccountCard: () => null }));

import { SyncCard } from "../../features/settings/SyncCard";
import { useUi } from "../../app/store";

const EMPTY_LINK = { url: "", username: "", insecure: false, hasPassword: false, verifiedAt: 0, lastError: "" };
let mounted: MountedView | undefined;

function input(id: string): HTMLInputElement {
  const element = document.querySelector<HTMLInputElement>(`#${id}`);
  if (!element) throw new Error(`input #${id} not found`);
  return element;
}

function loginButton(): HTMLButtonElement {
  return [...document.querySelectorAll<HTMLButtonElement>("button[type=submit]")].find((b) => b.textContent?.includes("登录"))!;
}

function advancedToggle(): HTMLButtonElement {
  return [...document.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) => b.textContent?.includes("高级选项"))!;
}

async function mountForm(link: Record<string, unknown>) {
  mocks.linkGet.mockResolvedValue(link);
  mocks.status.mockResolvedValue({ configured: false, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" });
  mocks.kindOptInGet.mockResolvedValue({});
  mocks.linkSet.mockImplementation((patch: Record<string, unknown>) => Promise.resolve({ ...EMPTY_LINK, ...patch, connect: "" }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
  await flushUntil(() => document.querySelector("#sync-url") !== null);
}

// submit logs in and waits until the card has reloaded the saved link, so no update lands after the test.
async function submit() {
  const toasts = mocks.toast.mock.calls.length;
  click(loginButton());
  await flushUntil(() => mocks.toast.mock.calls.length > toasts && document.querySelector("#sync-url") !== null);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("Desktop login address", () => {
  it("completes a bare host name as HTTPS on blur and on submit, and keeps an explicit http://", async () => {
    await mountForm(EMPTY_LINK);
    const url = input("sync-url");
    setInputValue(url, "nexterm.example.com");
    act(() => {
      url.focus();
      url.blur();
    });
    await flushUntil(() => input("sync-url").value === "https://nexterm.example.com");

    setInputValue(input("sync-url"), "nexterm.example.com:8443");
    setInputValue(input("sync-user"), "alice");
    setInputValue(input("sync-pass"), "alice-pw-123");
    await submit();
    expect(mocks.linkSet.mock.calls[0][0]).toMatchObject({
      url: "https://nexterm.example.com:8443",
      proxy: { type: "", host: "", port: 0, username: "" },
    });

    mocks.linkSet.mockClear();
    setInputValue(input("sync-url"), "http://192.168.1.10:8080");
    setInputValue(input("sync-pass"), "alice-pw-123");
    await submit();
    expect(mocks.linkSet.mock.calls[0][0]).toMatchObject({ url: "http://192.168.1.10:8080" });
  });
});

describe("Desktop login proxy", () => {
  it("is collapsed by default and sends a SOCKS5 proxy with its credentials", async () => {
    await mountForm(EMPTY_LINK);
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("false");
    expect(document.querySelector("#sync-proxy-host")).toBeNull();

    setInputValue(input("sync-url"), "https://nexterm.example.com");
    setInputValue(input("sync-user"), "alice");
    setInputValue(input("sync-pass"), "alice-pw-123");
    click(advancedToggle());
    chooseSelectOption(document, "代理类型", "SOCKS5");
    await flushUntil(() => document.querySelector("#sync-proxy-host") !== null);
    expect(loginButton().disabled).toBe(true);

    setInputValue(input("sync-proxy-host"), "10.0.0.2");
    setInputValue(input("sync-proxy-port"), "70000");
    await flushUntil(() => document.body.textContent?.includes("代理端口须在 1-65535 之间") === true);
    expect(loginButton().disabled).toBe(true);

    setInputValue(input("sync-proxy-port"), "1080");
    expect(input("sync-proxy-pass").disabled).toBe(true);
    setInputValue(input("sync-proxy-user"), "proxy-user");
    setInputValue(input("sync-proxy-pass"), "proxy-pw");
    await submit();
    expect(mocks.linkSet.mock.calls[0][0]).toMatchObject({
      url: "https://nexterm.example.com",
      proxy: { type: "socks5", host: "10.0.0.2", port: 1080, username: "proxy-user", password: "proxy-pw" },
    });
  });

  it("opens with a saved proxy, keeps its password unless retyped, and sends no password for SOCKS4", async () => {
    await mountForm({
      url: "https://nexterm.example.com", username: "alice", insecure: false, hasPassword: true, verifiedAt: 0, lastError: "",
      proxy: { type: "http", host: "proxy.internal", port: 3128, username: "u", hasPassword: true },
    });
    await flushUntil(() => document.querySelector("#sync-proxy-host") !== null);
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("true");
    expect(input("sync-proxy-host").value).toBe("proxy.internal");
    expect(input("sync-proxy-port").value).toBe("3128");
    expect(input("sync-proxy-pass").placeholder).toBe("留空则不修改已保存的密码");

    await submit();
    const kept = mocks.linkSet.mock.calls[0][0] as { proxy: Record<string, unknown> };
    expect(kept.proxy).toEqual({ type: "http", host: "proxy.internal", port: 3128, username: "u" });

    mocks.linkSet.mockClear();
    chooseSelectOption(document, "代理类型", "SOCKS4");
    await flushUntil(() => input("sync-proxy-pass").disabled);
    expect(input("sync-proxy-pass").placeholder).toBe("SOCKS4 不支持密码");
    await submit();
    expect(mocks.linkSet.mock.calls[0][0]).toMatchObject({ proxy: { type: "socks4", host: "proxy.internal", port: 3128, username: "u" } });
    expect((mocks.linkSet.mock.calls[0][0] as { proxy: Record<string, unknown> }).proxy).not.toHaveProperty("password");
  });
});
