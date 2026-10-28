/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    digest: vi.fn(),
    linkGet: vi.fn(),
    linkSet: vi.fn(),
    remoteDigest: vi.fn(),
    push: vi.fn(),
    pull: vi.fn(),
    token: vi.fn(),
    rotateToken: vi.fn(),
    assetList: vi.fn(),
    groupList: vi.fn(),
    snippetList: vi.fn(),
    transcriptHosts: vi.fn(),
    transcriptList: vi.fn(),
    transcriptRead: vi.fn(),
    collectAssets: vi.fn(),
    collectTombstones: vi.fn(),
    collectCredentials: vi.fn(),
    kindOptInGet: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: {
    list: mocks.assetList,
    groupList: mocks.groupList,
    snippetList: mocks.snippetList,
  },
  transcriptApi: {
    hosts: mocks.transcriptHosts,
    list: mocks.transcriptList,
    read: mocks.transcriptRead,
  },
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
    collectAssets: mocks.collectAssets,
    collectTombstones: mocks.collectTombstones,
    collectCredentials: mocks.collectCredentials,
    kindOptInGet: mocks.kindOptInGet,
  },
}));
vi.mock("../ui/dialogs", () => ({ ask: vi.fn() }));

const syncV2Mocks = vi.hoisted(() => ({
  ids: vi.fn(),
  pull: vi.fn(),
  push: vi.fn(),
}));

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      status: vi.fn().mockResolvedValue({ initialized: true, registration_open: false, auth: "on" }),
      me: vi.fn(),
      logout: vi.fn().mockResolvedValue({ ok: true }),
      logoutAll: vi.fn().mockResolvedValue({ revoked: 1 }),
      devices: vi.fn().mockResolvedValue({ devices: [] }),
      deviceRevoke: vi.fn().mockResolvedValue({ ok: true }),
      totpStatus: vi.fn().mockResolvedValue({ enabled: false, pending: false, mfa_required: false, recovery_codes_left: 0 }),
      totpSetup: vi.fn(),
      totpConfirm: vi.fn(),
      totpDisable: vi.fn(),
      totpLogin: vi.fn(),
    },
    adminApi: {
      users: vi.fn().mockResolvedValue({ users: [] }),
      settingsGet: vi.fn().mockResolvedValue({ registration_open: false, mfa_required: false }),
      settingsPut: vi.fn(),
      createUser: vi.fn(),
      disableUser: vi.fn(),
      resetUser: vi.fn(),
    },
    syncV2Api: syncV2Mocks,
  };
});

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
  mocks.assetList.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.transcriptHosts.mockResolvedValue([]);
  mocks.transcriptList.mockResolvedValue([]);
  mocks.transcriptRead.mockResolvedValue({ chunks: [], nextSeq: 0, done: true, totalBytes: 0 });
  mocks.collectAssets.mockResolvedValue({ assets: [], hasMore: false });
  mocks.collectTombstones.mockResolvedValue({ tombstones: [], hasMore: false });
  mocks.collectCredentials.mockResolvedValue({ credentials: [], hasMore: false });
  mocks.kindOptInGet.mockResolvedValue({ knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false, stateRules: false, runbookTemplates: false, skills: false, memory: false, cron: false });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function view(props: { title: string; dir?: "push" | "pull"; data: ImportReport }): MountedView {
  return mount(createElement(ImportReportView, props));
}

describe("ImportReportView skipped-item details", () => {
  it("uses the remote side for revision labels in a push report (import happens remotely)", () => {
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

  it("uses the local side for revision labels in a pull report", () => {
    mounted = view({ title: "Pull results", dir: "pull", data: { ...REPORT_BASE, skippedNewerDetails: DETAILS } });
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("本机修订 500 较对端修订 300 新");
    expect(text).toContain("本机与对端修订号相同（400），按 Origin 字典序裁决");
  });

  it("labels the source side as the bundle in an asset bundle import report", () => {
    mounted = view({ title: "Import results", data: { ...REPORT_BASE, skippedNewerDetails: DETAILS } });
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("本机修订 500 较包内修订 300 新");
    expect(text).toContain("跳过（本机较新） 2");
  });

  it("requires accurate clocks when skips exist and omits that hint otherwise", () => {
    mounted = view({ title: "Pull results", dir: "pull", data: { ...REPORT_BASE, skippedNewer: 1 } });
    expect(mounted.container.textContent).toContain("请保持各设备时钟准确");
    mounted.unmount();

    mounted = view({ title: "Pull results", dir: "pull", data: { ...REPORT_BASE, skippedNewer: 0 } });
    const text = mounted.container.textContent ?? "";
    expect(text).not.toContain("请保持各设备时钟准确");
    expect(text).toContain("跳过（本机较新） 0");
  });

  it("shows credential deletion and snippet counts only when non-zero", () => {
    mounted = view({
      title: "Import results",
      data: { ...REPORT_BASE, credsDeleted: 2, snippetsCreated: 3, snippetsUpdated: 1 },
    });
    let text = mounted.container.textContent ?? "";
    expect(text).toContain("凭据：新建 0 / 更新 0 / 删除 2");
    expect(text).toContain("片段：新建 3 / 更新 1");
    mounted.unmount();

    mounted = view({ title: "Import results", data: { ...REPORT_BASE } });
    text = mounted.container.textContent ?? "";
    expect(text).not.toContain("删除 0");
    expect(text).not.toContain("片段");
  });

  it("falls back to the entry ID for credential tombstones and other details with empty names", () => {
    mounted = view({
      title: "Import results",
      data: {
        ...REPORT_BASE,
        skippedNewerDetails: [
          { kind: "credential", id: "cred-tombstone-1", name: "", localRevision: 200, remoteRevision: 100, equalRevision: false },
        ],
      },
    });
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("cred-tombstone-1");
    expect(text).toContain("（凭据）");
    expect(text).toContain("本机修订 200 较包内修订 100 新");
  });
});

describe("SyncCard comparison clock hint", () => {
  it("requires accurate clocks next to the sync status in the comparison console", async () => {
    const { useAuth } = await import("../features/auth/store");
    syncV2Mocks.ids.mockResolvedValue({
      protocol: 2,
      entries: [{ id: "a1", seq: 1, blob_hash: "h1" }],
      head: "head-1",
      max_seq: 1,
    });
    const { sealSyncObject, utf8Bytes, bytesToBase64 } = await import("../features/auth/crypto");
    const dek = new Uint8Array(32).fill(7);
    const blob = await sealSyncObject(
      dek,
      utf8Bytes(JSON.stringify({ id: "a1", groupId: null, kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 2 })),
      "a1",
      "asset",
    );
    syncV2Mocks.pull.mockResolvedValue({
      protocol: 2,
      objects: [{ id: "a1", seq: 1, blob: bytesToBase64(blob) }],
      head: "head-1",
      max_seq: 1,
      next_seq: 1,
      cursor_done: true,
    });
    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: { id: "u-1", username: "alice", display_name: "", role: "user", state: "active", must_change_password: false, mfa_enabled: false, created_at: 1, updated_at: 1, last_login_at: 1 },
      dek,
      gate: "ready",
      pendingRecoveryKey: null,
      error: null,
    });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("web-01"));
    await flushUntil(() => !(mounted!.container.textContent ?? "").includes("正在读取本机与服务端数据"));

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("请保持各设备时钟准确");
    expect(text).toContain("否则「较新」判定可能不符合预期");
    act(() => {
      useAuth.setState({ user: null, dek: null, gate: "ready", pendingRecoveryKey: null, error: null, status: null });
    });
  });
});
