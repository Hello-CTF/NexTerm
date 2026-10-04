/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    digest: vi.fn(),
    linkGet: vi.fn(),
    linkSet: vi.fn(),
    remoteDigest: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: {
      digest: mocks.digest,
      linkGet: mocks.linkGet,
      linkSet: mocks.linkSet,
      remoteDigest: mocks.remoteDigest,
      push: vi.fn(),
      pull: vi.fn(),
      token: vi.fn(),
      rotateToken: vi.fn(),
    },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { SyncCard } from "../../features/settings/SyncCard";
import { useUi } from "../../app/store";

const LONG_HOST = "r120-sync-host-with-a-very-long-hostname.example-internal.company.com";
const LOCAL_DIGEST = {
  origin: "local",
  assets: [
    {
      id: "a1",
      name: "web-01",
      kind: "ssh",
      host: LONG_HOST,
      updatedAt: 2,
      deletedAt: null,
      hasCred: true,
    },
  ],
};
const SAVED_LINK = {
  url: "https://sync.example.com",
  tokenKind: "server",
  token: "saved-token",
  insecure: false,
  verifiedAt: 1,
  lastError: null as string | null,
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.digest.mockResolvedValue(LOCAL_DIGEST);
  mocks.linkGet.mockResolvedValue(null);
  mocks.linkSet.mockResolvedValue(SAVED_LINK);
  mocks.remoteDigest.mockResolvedValue({ origin: "remote", assets: [] });
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

describe("SyncCard 客户端变体窄屏结构", () => {
  it("部署位置行可换行、select 窄屏全宽 ≥560px 定宽", async () => {
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => mounted!.container.querySelector("#sync-token-kind") !== null);
    const select = mounted.container.querySelector("#sync-token-kind")!;
    expect(select.className).toContain("min-w-0");
    expect(select.className).toContain("min-[560px]:w-[250px]");
    expect(select.parentElement?.className).toContain("flex-wrap");
  });

  it("对照表长主机截断且 title 带完整值，带凭据行窄屏可换行收缩", async () => {
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => mounted!.container.querySelector("#sync-url") !== null);
    const urlInput = mounted.container.querySelector<HTMLInputElement>('input[id="sync-url"]')!;
    const tokenInput = mounted.container.querySelector<HTMLInputElement>('input[id="sync-token"]')!;
    setInputValue(urlInput, "https://sync.example.com");
    setInputValue(tokenInput, "fresh-token");
    await flushUntil(() => {
      const btn = [...mounted!.container.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "保存并测试连接",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(mounted.container, "保存并测试连接");
    await flushUntil(() => mocks.remoteDigest.mock.calls.length > 0);
    await flushUntil(() =>
      [...mounted!.container.querySelectorAll("span")].some((s) => s.textContent === LONG_HOST),
    );
    const hostSpan = [...mounted.container.querySelectorAll("span")].find(
      (s) => s.textContent === LONG_HOST,
    );
    expect(hostSpan?.className).toContain("truncate");
    expect(hostSpan?.className).toContain("shrink");
    expect(hostSpan?.getAttribute("title")).toBe(LONG_HOST);
    const row = hostSpan!.closest("label")!;
    expect(row.className).toContain("flex-wrap");
    expect(row.textContent).toContain("带密码");
    const badge = row.querySelector(".nx-badge");
    expect(badge?.className).toContain("shrink-0");
    expect(badge?.textContent).toBe("仅本机");
    const nameSpan = [...mounted.container.querySelectorAll("span")].find(
      (s) => s.textContent === "web-01",
    );
    expect(nameSpan?.className).toContain("truncate");
  });
});
