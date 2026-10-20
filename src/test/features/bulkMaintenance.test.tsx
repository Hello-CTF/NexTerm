/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import type { Asset, SessionInfo } from "../../ipc/commands";
import type { ExecBatch, ExecBatchResult } from "../../ipc/execBatchApi";
import {
  click,
  clickButton,
  flushUntil,
  mountWithClient,
  setInputValue,
  setSelectValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  groupList: vi.fn(),
  credentials: vi.fn(),
  bulkEdit: vi.fn(),
  deploy: vi.fn(),
  testLogin: vi.fn(),
  cancel: vi.fn(),
  createId: vi.fn(),
}));

vi.mock("../../ipc/commands", async () => {
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    call: vi.fn(),
    assetApi: { list: mocks.assetList, groupList: mocks.groupList },
    vaultApi: { listCredentials: mocks.credentials },
  });
});

vi.mock("../../ipc/execBatchApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/execBatchApi")>();
  return { ...actual, execBatchApi: { cancel: mocks.cancel }, createExecBatchId: mocks.createId };
});

vi.mock("../../features/execbatch/maintenanceApi", () => ({
  maintenanceApi: {
    bulkEdit: mocks.bulkEdit,
    deployPublicKey: mocks.deploy,
    testLogin: mocks.testLogin,
  },
}));

import { BulkMaintenancePanel } from "../../features/execbatch/BulkMaintenancePanel";

function asset(id: string, name: string, updatedAt: number): Asset {
  return {
    id,
    groupId: null,
    kind: "ssh",
    name,
    host: `${id}.example.com`,
    port: 22,
    username: "root",
    authKind: "key",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt,
    deletedAt: null,
    builtin: false,
  };
}

const assets = [asset("a", "host-a", 10), asset("b", "host-b", 20)];
const sessions: SessionInfo[] = [{
  id: "s-a",
  assetId: "a",
  name: "session-a",
  kind: "ssh",
  status: "connected",
  tabs: [],
  createdAt: 1,
}];

function execResult(patch: Partial<ExecBatchResult>): ExecBatchResult {
  return {
    sessionId: "s-a",
    sessionName: "session-a",
    status: "success",
    stdout: "",
    stderr: "",
    exitCode: 0,
    durationMs: 1,
    truncated: false,
    ...patch,
  };
}

function batch(results: ExecBatchResult[]): ExecBatch {
  return { batchId: "maintenance-batch", command: "deploy", startedAt: 1, finishedAt: 2, results };
}

let mounted: MountedView | undefined;

async function mountPanel() {
  mounted = mountWithClient(createElement(BulkMaintenancePanel, { sessions }));
  await flushUntil(() => mounted?.container.querySelector('input[type="checkbox"][value="a"]') !== null
    && mounted?.container.querySelector('input[type="checkbox"][value="b"]') !== null);
  return mounted;
}

function target(id: string): HTMLInputElement {
  const input = mounted?.container.querySelector<HTMLInputElement>(`input[type="checkbox"][value="${id}"]`);
  if (!input) throw new Error(`target ${id} not found`);
  return input;
}

function inputByLabel<T extends HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(label: string): T {
  const input = mounted?.container.querySelector<T>(`[aria-label="${label}"]`);
  if (!input) throw new Error(`input ${label} not found`);
  return input;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue(assets.map((item) => ({ ...item })));
  mocks.groupList.mockResolvedValue([{ id: "g1", parentId: null, name: "fleet", sort: 0, createdAt: 1, updatedAt: 1 }]);
  mocks.credentials.mockResolvedValue([{
    id: "cred-1",
    name: "deploy-credential",
    kind: "private_key",
    createdAt: 1,
    updatedAt: 1,
    usedBy: [],
    source: "inline",
    refPath: null,
    hasPassphrase: false,
    secret: "credential-plaintext-must-not-render",
  }]);
  mocks.createId.mockReturnValue("maintenance-batch");
  mocks.cancel.mockResolvedValue({ batchId: "maintenance-batch", canceled: true });
  mocks.bulkEdit.mockResolvedValue({ results: [] });
  mocks.deploy.mockResolvedValue(batch([]));
  mocks.testLogin.mockResolvedValue({ results: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("BulkMaintenancePanel", () => {
  it("sends CAS versions and field-only edits with per-target results and no credential plaintext", async () => {
    mocks.bulkEdit.mockResolvedValue({
      results: [
        { assetId: "a", updatedAt: 11 },
        { assetId: "b", conflict: true, error: "资产已被其他修改更新，请刷新后重试" },
      ],
    });
    await mountPanel();
    click(target("a"));
    click(target("b"));
    await flushUntil(() => inputByLabel<HTMLSelectElement>("批量编辑分组").options.length > 2
      && inputByLabel<HTMLSelectElement>("批量编辑凭据").options.length > 2);
    setSelectValue(inputByLabel("批量编辑分组"), "id:g1");
    setSelectValue(inputByLabel("批量编辑跳板机"), "clear");
    setSelectValue(inputByLabel("批量编辑凭据"), "id:cred-1");
    click(inputByLabel("修改标签"));
    setInputValue(inputByLabel("批量编辑标签"), "web, env:prod");
    click(inputByLabel("修改端口"));
    setInputValue(inputByLabel("批量编辑端口"), "2222");
    clickButton(mounted!.container, "应用批量编辑");
    await flushUntil(() => mocks.bulkEdit.mock.calls.length === 1
      && (mounted?.container.textContent.includes("版本 11") ?? false));

    expect(mocks.bulkEdit).toHaveBeenCalledWith({
      targets: [
        { assetId: "a", expectedUpdatedAt: 10 },
        { assetId: "b", expectedUpdatedAt: 20 },
      ],
      groupId: "g1",
      jumpAssetId: "",
      credentialId: "cred-1",
      tags: "web, env:prod",
      port: 2222,
    });
    expect(mounted!.container.textContent).toContain("资产已被其他修改更新");
    expect(mounted!.container.textContent).toContain("deploy-credential");
    expect(mounted!.container.textContent).toContain("cred-1");
    expect(mounted!.container.textContent).not.toContain("credential-plaintext-must-not-render");
  });

  it("attributes test-login results and deploys through one connected session per asset with offline failures", async () => {
    mocks.deploy.mockResolvedValue(batch([
      execResult({ stdout: "NEXTERM_PUBLIC_KEY_ALREADY_PRESENT\n" }),
    ]));
    mocks.testLogin.mockResolvedValue({
      results: [
        { assetId: "a", reachable: true, durationMs: 12 },
        { assetId: "b", reachable: false, kind: "refused", error: "connection refused", durationMs: 13 },
      ],
    });
    await mountPanel();
    click(target("a"));
    click(target("b"));
    setInputValue(inputByLabel("部署公钥内容"), "ssh-ed25519 AAAATEST operator");
    clickButton(mounted!.container, "部署公钥");
    await flushUntil(() => mocks.deploy.mock.calls.length === 1
      && (mounted?.container.textContent.includes("公钥已存在，未重复追加") ?? false));
    expect(mocks.deploy).toHaveBeenCalledWith({
      batchId: "maintenance-batch",
      sessionIds: ["s-a"],
      publicKey: "ssh-ed25519 AAAATEST operator",
      path: "~/.ssh/authorized_keys",
      timeoutMs: 30000,
      maxConcurrent: 4,
    });
    const offline = mounted!.container.querySelector('[data-asset-id="b"]');
    expect(offline?.textContent).toContain("没有可复用的已连接 SSH 会话");

    clickButton(mounted!.container, "test login / 连接测试");
    await flushUntil(() => mocks.testLogin.mock.calls.length === 1
      && (mounted?.container.textContent.includes("SSH 握手成功（12 ms）") ?? false));
    expect(mocks.testLogin).toHaveBeenCalledWith(["a", "b"]);
    const loginResults = mounted!.container.querySelector('[aria-label="test login 逐目标结果"]');
    expect(loginResults?.querySelector('[data-asset-id="a"]')?.textContent).toContain("SSH 握手成功（12 ms）");
    expect(loginResults?.querySelector('[data-asset-id="b"]')?.textContent).toContain("连接被拒绝：connection refused");
  });

  it("presents each test-login failure class accurately instead of raw kinds", async () => {
    const more = [asset("c", "host-c", 30), asset("d", "host-d", 40), asset("e", "host-e", 50), asset("f", "host-f", 60)];
    mocks.assetList.mockResolvedValue([...assets, ...more].map((item) => ({ ...item })));
    mocks.testLogin.mockResolvedValue({
      results: [
        { assetId: "a", reachable: false, kind: "network", error: "dial tcp: no route to host", durationMs: 5 },
        { assetId: "b", reachable: false, kind: "host_key_changed", error: "host key mismatch", durationMs: 6 },
        { assetId: "c", reachable: false, kind: "auth", error: "permission denied", durationMs: 7 },
        { assetId: "d", reachable: false, kind: "vault", error: "凭据库已锁定", durationMs: 8 },
        { assetId: "e", reachable: false, kind: "config", error: "asset host is required", durationMs: 9 },
        {
          assetId: "f",
          reachable: false,
          kind: "host_key_pending",
          error: "首次连接 f.example.com:22 需要确认主机指纹（SHA256:pending），请与服务器管理员核对后确认是否信任该主机",
          durationMs: 10,
        },
      ],
    });
    await mountPanel();
    for (const id of ["a", "b", "c", "d", "e", "f"]) click(target(id));
    clickButton(mounted!.container, "test login / 连接测试");
    await flushUntil(() => mocks.testLogin.mock.calls.length === 1
      && (mounted?.container.textContent.includes("凭据库不可用：凭据库已锁定") ?? false));
    const loginResults = mounted!.container.querySelector('[aria-label="test login 逐目标结果"]');
    expect(loginResults?.querySelector('[data-asset-id="a"]')?.textContent).toContain("网络不可达：dial tcp: no route to host");
    expect(loginResults?.querySelector('[data-asset-id="b"]')?.textContent).toContain("主机密钥已变更：host key mismatch");
    expect(loginResults?.querySelector('[data-asset-id="c"]')?.textContent).toContain("认证失败：permission denied");
    expect(loginResults?.querySelector('[data-asset-id="d"]')?.textContent).toContain("凭据库不可用：凭据库已锁定");
    expect(loginResults?.querySelector('[data-asset-id="e"]')?.textContent).toContain("配置无效：asset host is required");
    // A pending host key keeps its own class with the actionable backend guidance; the
    // panel never auto-accepts it (the result row offers no trust action).
    const pendingRow = loginResults?.querySelector('[data-asset-id="f"]');
    expect(pendingRow?.textContent).toContain("主机密钥待确认：首次连接 f.example.com:22 需要确认主机指纹（SHA256:pending），请与服务器管理员核对后确认是否信任该主机");
    expect(pendingRow?.querySelector("button")).toBeNull();
    for (const raw of ["network：", "host_key_changed：", "host_key_pending：", "auth：", "vault：", "config："]) {
      expect(mounted!.container.textContent).not.toContain(raw);
    }
    // SSH assets get a real handshake, not a "TCP reachable / unauthenticated" label.
    expect(mounted!.container.textContent).toContain("真实 host-key+凭据握手");
    expect(mounted!.container.textContent).not.toContain("不代表凭据已完成认证");
  });

  it("requires production confirmation before retrying deploy with bound hashes", async () => {
    const targetSetHash = "a".repeat(64);
    const commandHash = "b".repeat(64);
    mocks.deploy
      .mockRejectedValueOnce({
        code: "needs_confirm",
        message: "生产环境批量执行需要显式确认",
        detail: {
          kind: "exec_batch_production_confirmation",
          batchId: "maintenance-batch",
          targetSetHash,
          commandHash,
          productionTargets: [{ sessionId: "s-a", sessionName: "session-a", environment: "production" }],
        },
      })
      .mockResolvedValueOnce(batch([
        execResult({ stdout: "NEXTERM_PUBLIC_KEY_BACKUP /home/a/.ssh/authorized_keys.bak\nNEXTERM_PUBLIC_KEY_ADDED\n" }),
      ]));
    await mountPanel();
    click(target("a"));
    setInputValue(inputByLabel("部署公钥内容"), "ssh-ed25519 AAAATEST production");
    clickButton(mounted!.container, "部署公钥");
    await flushUntil(() => document.body.querySelector('[role="dialog"][aria-label="确认生产环境执行"]') !== null);
    expect(mocks.deploy).toHaveBeenCalledTimes(1);

    const dialog = document.body.querySelector<HTMLElement>('[role="dialog"][aria-label="确认生产环境执行"]');
    if (!dialog) throw new Error("production dialog not found");
    click(dialog.querySelector('input[aria-label="确认生产环境风险"]')!);
    clickButton(dialog, "确认执行（1）");
    await flushUntil(() => mocks.deploy.mock.calls.length === 2
      && (mounted?.container.textContent.includes("原文件已创建只读备份") ?? false));
    expect(mocks.deploy.mock.calls[1][0].productionConfirmation).toEqual({
      confirmed: true,
      targetSetHash,
      commandHash,
    });
  });
});
