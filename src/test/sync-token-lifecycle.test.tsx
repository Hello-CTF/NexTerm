/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, flushUntil, mount, setInputValue, setSelectValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    digest: vi.fn(),
    token: vi.fn(),
    rotateToken: vi.fn(),
    tokenList: vi.fn(),
    tokenIssue: vi.fn(),
    tokenRevoke: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
  syncApi: {
    digest: mocks.digest,
    token: mocks.token,
    rotateToken: mocks.rotateToken,
    tokenList: mocks.tokenList,
    tokenIssue: mocks.tokenIssue,
    tokenRevoke: mocks.tokenRevoke,
  },
}));
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));

import { SyncCard } from "../features/settings/SyncCard";
import { useUi } from "../app/store";

const TOKENS = [
  { id: "admin", clientId: "admin", purpose: "sync", createdAt: 1_700_000_000_000, expiresAt: 0, revokedAt: null, lastUsedAt: 1_700_000_100_000 },
  { id: "t1", clientId: "macbook-pro", purpose: "sync", createdAt: 1_700_000_200_000, expiresAt: 0, revokedAt: null, lastUsedAt: null },
  { id: "t2", clientId: "old-phone", purpose: "sync", createdAt: 1_700_000_300_000, expiresAt: 1_700_000_400_000, revokedAt: null, lastUsedAt: null },
  { id: "t3", clientId: "lost-laptop", purpose: "metrics", createdAt: 1_700_000_500_000, expiresAt: 0, revokedAt: 1_700_000_600_000, lastUsedAt: null },
];

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.digest.mockResolvedValue({ origin: "local", assets: [] });
  mocks.token.mockResolvedValue("admin-secret-value");
  mocks.tokenList.mockResolvedValue(TOKENS);
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

function text(): string {
  return mounted?.container.textContent ?? "";
}

async function mountAndExpand() {
  mounted = withClient(createElement(SyncCard));
  await flushUntil(() => text().includes("这台是同步目标"));
  clickButton(mounted.container, "客户端令牌（按客户端分发 · 轮换 · 吊销）");
  await flushUntil(() => mocks.tokenList.mock.calls.length > 0);
  await flushUntil(() => text().includes("macbook-pro"));
}

function tokenRow(clientId: string): HTMLElement {
  const row = [...mounted!.container.querySelectorAll("div")].find(
    (d) =>
      d.className.includes("flex-wrap") &&
      [...d.querySelectorAll(":scope > span")].some((s) => s.textContent === clientId),
  );
  if (!row) throw new Error(`token row not found: ${clientId}`);
  return row;
}

function rowButton(row: HTMLElement, label: string): HTMLButtonElement {
  const btn = [...row.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
  if (!btn) throw new Error(`row button not found: ${label}`);
  return btn as HTMLButtonElement;
}

function oneTimeBox(): HTMLElement {
  const box = [...mounted!.container.querySelectorAll(".nx-alert-danger")].find((el) =>
    el.textContent?.includes("只显示这一次"),
  );
  if (!box) throw new Error("one-time secret box not found");
  return box as HTMLElement;
}

function clickInBox(box: HTMLElement, label: string): void {
  const btn = [...box.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
  if (!btn) throw new Error(`box button not found: ${label}`);
  click(btn);
}

describe("SyncCard 客户端令牌（服务端一面）", () => {
  it("面板展开才加载列表；列表只含元数据，不渲染任何令牌内容", async () => {
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => text().includes("这台是同步目标"));
    expect(mocks.tokenList).not.toHaveBeenCalled();

    clickButton(mounted.container, "客户端令牌（按客户端分发 · 轮换 · 吊销）");
    await flushUntil(() => text().includes("macbook-pro"));
    expect(mocks.tokenList).toHaveBeenCalledTimes(1);
    expect(text()).toContain("admin");
    expect(text()).toContain("管理员");
    expect(text()).toContain("metrics");
    expect(text()).toContain("不记录令牌本身");
    expect(text()).not.toContain("admin-secret-value");
  });

  it("状态与过期渲染：生效中 / 已过期 / 已吊销，永不过期与最近使用如实展示", async () => {
    await mountAndExpand();
    const row = (id: string) => tokenRow(id);
    expect(row("admin").textContent).toContain("生效中");
    expect(row("admin").textContent).toContain("永不过期");
    expect(row("macbook-pro").textContent).toContain("从未使用");
    expect(row("old-phone").textContent).toContain("已过期");
    expect(row("lost-laptop").textContent).toContain("已吊销");
  });

  it("签发到指定客户端/用途/有效期；新令牌默认遮蔽，显示后可复制，列表随之刷新", async () => {
    await mountAndExpand();
    mocks.tokenIssue.mockResolvedValue({
      token: { id: "t9", clientId: "macbook-pro", purpose: "sync", createdAt: Date.now(), expiresAt: 0, revokedAt: null, lastUsedAt: null },
      secret: "brand-new-secret",
    });
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-token-client")!, "macbook-pro");
    setSelectValue(mounted!.container.querySelector<HTMLSelectElement>("#sync-token-ttl")!, String(7 * 24 * 60 * 60 * 1000));
    clickButton(mounted!.container, "签发令牌");
    await flushUntil(() => mocks.tokenIssue.mock.calls.length > 0);

    expect(mocks.tokenIssue).toHaveBeenCalledWith("macbook-pro", "sync", 7 * 24 * 60 * 60 * 1000);
    await flushUntil(() => text().includes("只显示这一次"));
    expect(text()).not.toContain("brand-new-secret");
    clickInBox(oneTimeBox(), "显示");
    expect(text()).toContain("brand-new-secret");
    expect(text()).not.toContain("admin-secret-value");

    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
    clickInBox(oneTimeBox(), "复制");
    await flush();
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("brand-new-secret");

    expect(mocks.tokenList).toHaveBeenCalledTimes(2);
    clickInBox(oneTimeBox(), "完成");
    expect(text()).not.toContain("brand-new-secret");
  });

  it("用途或客户端标识不合法时内联报错，不发起签发", async () => {
    await mountAndExpand();
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-token-client")!, "macbook-pro");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-token-purpose")!, "Bad Purpose");
    clickButton(mounted!.container, "签发令牌");
    await flushUntil(() => text().includes("用途需以小写字母开头"));
    expect(mocks.tokenIssue).not.toHaveBeenCalled();

    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-token-purpose")!, "sync");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>("#sync-token-client")!, "  ");
    const issueBtn = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "签发令牌",
    ) as HTMLButtonElement;
    expect(issueBtn.disabled).toBe(true);
  });

  it("轮换需确认且提示旧令牌立即失效；确认后换发新令牌并只展示一次", async () => {
    await mountAndExpand();
    mocks.rotateToken.mockResolvedValue("rotated-secret-xyz");
    click(rowButton(tokenRow("macbook-pro"), "轮换"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("macbook-pro");
    expect(question).toContain("旧令牌立即失效");
    expect(mocks.rotateToken).toHaveBeenCalledWith("t1");

    await flushUntil(() => text().includes("只显示这一次"));
    clickInBox(oneTimeBox(), "显示");
    expect(text()).toContain("rotated-secret-xyz");
    expect(mocks.tokenList).toHaveBeenCalledTimes(2);
  });

  it("吊销需确认；确认后吊销该客户端令牌并刷新列表", async () => {
    await mountAndExpand();
    mocks.tokenRevoke.mockResolvedValue(undefined);
    click(rowButton(tokenRow("macbook-pro"), "吊销"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("吊销后该客户端立即无法同步");
    expect(mocks.tokenRevoke).toHaveBeenCalledWith("t1");
    await flushUntil(() => mocks.tokenList.mock.calls.length >= 2);
  });

  it("管理员令牌可轮换但不可吊销；已吊销令牌不能再轮换或吊销", async () => {
    await mountAndExpand();
    const adminRow = tokenRow("admin");
    expect(rowButton(adminRow, "吊销").disabled).toBe(true);
    expect(rowButton(adminRow, "吊销").getAttribute("title")).toContain("管理员令牌不可吊销");
    expect(rowButton(adminRow, "轮换").disabled).toBe(false);

    const revokedRow = tokenRow("lost-laptop");
    expect(rowButton(revokedRow, "轮换").disabled).toBe(true);
    expect(rowButton(revokedRow, "吊销").disabled).toBe(true);
  });

  it("面板轮换 admin 后主令牌框同步：旧主令牌不再显示/复制", async () => {
    await mountAndExpand();
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
    mocks.rotateToken.mockResolvedValue("admin-rotated-secret");
    click(rowButton(tokenRow("admin"), "轮换"));
    await flushUntil(() => mocks.rotateToken.mock.calls.length > 0);
    expect(mocks.rotateToken).toHaveBeenCalledWith("admin");

    const mainBox = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "重置令牌",
    )!.parentElement!;
    const revealBtn = [...mainBox.querySelectorAll("button")].find((b) => b.textContent?.trim() === "显示")!;
    click(revealBtn);
    await flushUntil(() => text().includes("admin-rotated-secret"));
    expect(text()).not.toContain("admin-secret-value");

    const copyBtn = [...mainBox.querySelectorAll("button")].find((b) => b.textContent?.trim() === "复制")!;
    click(copyBtn);
    await flush();
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("admin-rotated-secret");
    expect(navigator.clipboard.writeText).not.toHaveBeenCalledWith("admin-secret-value");
  });

  it("列表读取失败时内联报错并可重试", async () => {
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => text().includes("这台是同步目标"));
    mocks.tokenList.mockRejectedValueOnce(new Error("数据库只读"));
    clickButton(mounted.container, "客户端令牌（按客户端分发 · 轮换 · 吊销）");
    await flushUntil(() => text().includes("令牌列表读取失败"));
    expect(text()).toContain("数据库只读");

    clickButton(mounted.container, "重试");
    await flushUntil(() => text().includes("macbook-pro"));
    expect(mocks.tokenList).toHaveBeenCalledTimes(2);
  });

  it("令牌行窄屏结构：行可换行、客户端标识截断并带完整 title", async () => {
    await mountAndExpand();
    const row = tokenRow("macbook-pro");
    expect(row.className).toContain("flex-wrap");
    const name = [...row.querySelectorAll("span")].find((s) => s.textContent === "macbook-pro")!;
    expect(name.className).toContain("truncate");
    expect(name.className).toContain("min-w-0");
    expect(name.getAttribute("title")).toBe("macbook-pro");
    for (const label of ["轮换", "吊销"]) {
      expect(rowButton(row, label).className).toContain("shrink-0");
    }
  });
});
