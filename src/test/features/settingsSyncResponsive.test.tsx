/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    linkGet: vi.fn(),
    linkSet: vi.fn(),
    status: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: {
      linkGet: mocks.linkGet,
      linkSet: mocks.linkSet,
      status: mocks.status,
    },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { SyncCard } from "../../features/settings/SyncCard";
import { useUi } from "../../app/store";

const SAVED_LINK = {
  url: "https://sync.example.com",
  username: "alice",
  insecure: false,
  hasPassword: true,
  verifiedAt: 1,
  lastError: null as string | null,
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.linkGet.mockResolvedValue(SAVED_LINK);
  mocks.status.mockResolvedValue({
    configured: true,
    loggedIn: true,
    username: "alice",
    seq: 7,
    verifiedAt: 1,
    lastError: "",
  });
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

describe("SyncCard 桌面端链接表单窄屏结构", () => {
  it("链接表单输入框可收缩(min-w-0 flex-1),标签固定宽", async () => {
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => mounted!.container.querySelector("#sync-url") !== null);
    for (const id of ["sync-url", "sync-user", "sync-pass"]) {
      const input = mounted.container.querySelector<HTMLInputElement>(`#${id}`)!;
      expect(input.className).toContain("min-w-0");
      expect(input.className).toContain("flex-1");
    }
    const label = mounted.container.querySelector('label[for="sync-url"]')!;
    expect(label.className).toContain("shrink-0");
  });

  it("操作按钮行可换行,长错误文本可断词", async () => {
    const longError = "同步头不一致:" + "很长的对端原因".repeat(20);
    mocks.status.mockResolvedValue({
      configured: true,
      loggedIn: false,
      seq: 0,
      verifiedAt: 0,
      lastError: longError,
    });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => mounted!.container.textContent?.includes(longError.slice(0, 20)));

    const buttonRow = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.className.includes("flex-wrap") && d.querySelector("button"),
    );
    expect(buttonRow).not.toBeNull();

    const errorSpan = [...mounted.container.querySelectorAll("span")].find(
      (s) => (s.textContent ?? "").includes(longError.slice(0, 20)),
    );
    expect(errorSpan?.className).toContain("text-amber-300");
  });
});
