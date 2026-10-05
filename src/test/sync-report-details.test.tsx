/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    digest: vi.fn(),
    linkGet: vi.fn(),
    linkSet: vi.fn(),
    remoteDigest: vi.fn(),
    push: vi.fn(),
    pull: vi.fn(),
    token: vi.fn(),
    rotateToken: vi.fn(),
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
    linkGet: mocks.linkGet,
    linkSet: mocks.linkSet,
    remoteDigest: mocks.remoteDigest,
    push: mocks.push,
    pull: mocks.pull,
    token: mocks.token,
    rotateToken: mocks.rotateToken,
  },
}));
vi.mock("../ui/dialogs", () => ({ ask: vi.fn() }));

import { SyncCard } from "../features/settings/SyncCard";
import { ImportReportView } from "../features/settings/SyncCardReport";
import { useUi } from "../app/store";
import type { ImportReport } from "../ipc/types";

const REPORT_BASE: ImportReport = {
  groupsCreated: 0,
  groupsUpdated: 0,
  assetsCreated: 1,
  assetsUpdated: 0,
  credsCreated: 0,
  credsUpdated: 0,
  credsDeleted: 0,
  snippetsCreated: 0,
  snippetsUpdated: 0,
  skippedNewer: 2,
  refused: 0,
  warnings: [],
};

const DETAILS = [
  { kind: "asset", id: "a1", name: "web-01", localRevision: 500, remoteRevision: 300, equalRevision: false },
  { kind: "credential", id: "c1", name: "生产口令", localRevision: 400, remoteRevision: 400, equalRevision: true },
];

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function view(props: { title: string; dir?: "push" | "pull"; data: ImportReport }): MountedView {
  return mount(createElement(ImportReportView, props));
}

describe("ImportReportView 跳过明细", () => {
  it("推送报告：修订号标签以对端为本（导入发生在对端）", () => {
    mounted = view({ title: "推送结果", dir: "push", data: { ...REPORT_BASE, skippedNewerDetails: DETAILS } });
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("推送结果");
    expect(text).toContain("web-01");
    expect(text).toContain("（资产）");
    expect(text).toContain("对端修订 500 较本机修订 300 新");
    expect(text).toContain("生产口令");
    expect(text).toContain("（凭据）");
    expect(text).toContain("对端与本机修订号相同（400），按 Origin 字典序裁决");
  });

  it("拉取报告：修订号标签以本机为本", () => {
    mounted = view({ title: "拉取结果", dir: "pull", data: { ...REPORT_BASE, skippedNewerDetails: DETAILS } });
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("本机修订 500 较对端修订 300 新");
    expect(text).toContain("本机与对端修订号相同（400），按 Origin 字典序裁决");
  });

  it("资产包导入报告：来源侧标注为「包内」", () => {
    mounted = view({ title: "导入结果", data: { ...REPORT_BASE, skippedNewerDetails: DETAILS } });
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("本机修订 500 较包内修订 300 新");
    expect(text).toContain("跳过（本机较新） 2");
  });

  it("存在跳过时给出 NTP 时钟要求；无跳过时不出该提示", () => {
    mounted = view({ title: "拉取结果", dir: "pull", data: { ...REPORT_BASE, skippedNewer: 1 } });
    expect(mounted.container.textContent).toContain("NTP 同步");
    mounted.unmount();

    mounted = view({ title: "拉取结果", dir: "pull", data: { ...REPORT_BASE, skippedNewer: 0 } });
    const text = mounted.container.textContent ?? "";
    expect(text).not.toContain("NTP 同步");
    expect(text).toContain("跳过（本机较新） 0");
  });

  it("报告计数补充凭据删除与片段：仅在非零时出现", () => {
    mounted = view({
      title: "导入结果",
      data: { ...REPORT_BASE, credsDeleted: 2, snippetsCreated: 3, snippetsUpdated: 1 },
    });
    let text = mounted.container.textContent ?? "";
    expect(text).toContain("凭据 新建 0 / 更新 0 / 删除 2");
    expect(text).toContain("片段 新建 3 / 更新 1");
    mounted.unmount();

    mounted = view({ title: "导入结果", data: { ...REPORT_BASE } });
    text = mounted.container.textContent ?? "";
    expect(text).not.toContain("删除 0");
    expect(text).not.toContain("片段");
  });
});

describe("SyncCard 对照区 NTP 提示", () => {
  it("对照表在展示同步状态处给出 NTP 时钟要求", async () => {
    mocks.digest.mockResolvedValue({
      origin: "local",
      assets: [{ id: "a1", name: "web-01", kind: "ssh", host: "10.0.0.1", updatedAt: 2, deletedAt: null, hasCred: false, groupId: null }],
    });
    mocks.linkGet.mockResolvedValue({ url: "https://sync.example.com", tokenKind: "server", token: "", insecure: false, verifiedAt: 1, lastError: null });
    mocks.linkSet.mockImplementation(async (l: unknown) => l);
    mocks.remoteDigest.mockResolvedValue({
      origin: "remote",
      assets: [{ id: "a1", name: "web-01", kind: "ssh", host: "10.0.0.1", updatedAt: 1, deletedAt: null, hasCred: false, groupId: null }],
    });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
    await flushUntil(() => mounted!.container.querySelector("#sync-url") !== null);
    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[id="sync-url"]')!, "https://sync.example.com");
    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[id="sync-token"]')!, "fresh-token");
    await flushUntil(() => {
      const btn = [...mounted!.container.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "保存并测试连接",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(mounted.container, "保存并测试连接");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("资产对照"));

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("NTP 同步");
    expect(text).toContain("不检测也不校正时钟偏移");
  });
});
