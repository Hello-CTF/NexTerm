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

function loginTarget(id: string): HTMLInputElement {
  const input = mounted?.container.querySelector<HTMLInputElement>(`[data-testid="login-test-targets"] input[type="checkbox"][value="${id}"]`);
  if (!input) throw new Error(`login test target ${id} not found`);
  return input;
}

function loginRows(): Element | null {
  return mounted?.container.querySelector('[aria-label="连接测试结果"]') ?? null;
}

function loginRow(id: string): Element | null {
  return loginRows()?.querySelector(`[data-asset-id="${id}"]`) ?? null;
}

function resultFor(id: string, patch: Record<string, unknown> = {}) {
  return { assetId: id, reachable: true, durationMs: 5, ...patch };
}

// Answers each per-asset request from a table keyed by asset id.
function answerTestLogin(table: Record<string, Record<string, unknown>>) {
  mocks.testLogin.mockImplementation((ids: string[]) => Promise.resolve({
    results: ids.map((id) => resultFor(id, table[id] ?? {})),
  }));
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
    answerTestLogin({ b: { reachable: false, kind: "refused", error: "connection refused" } });
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

    click(loginTarget("a"));
    click(loginTarget("b"));
    clickButton(mounted!.container, "开始连接测试");
    await flushUntil(() => mocks.testLogin.mock.calls.length === 2 && (loginRow("b")?.textContent.includes("端口不通") ?? false));
    expect(mocks.testLogin).toHaveBeenCalledWith(["a"], 10000, 1);
    expect(mocks.testLogin).toHaveBeenCalledWith(["b"], 10000, 1);
    expect(loginRow("a")?.textContent).toContain("已验证登录");
    expect(loginRow("b")?.textContent).toContain("端口不通");
  });

  it("classifies every test-login outcome with a plain status and keeps the backend next step", async () => {
    const more = [asset("c", "host-c", 30), asset("d", "host-d", 40), asset("e", "host-e", 50), asset("f", "host-f", 60)];
    mocks.assetList.mockResolvedValue([...assets, ...more].map((item) => ({ ...item })));
    answerTestLogin({
      a: { reachable: false, kind: "network", error: "无法连到 a:22，请检查网络和防火墙" },
      b: { reachable: false, kind: "host_key_changed", error: "指纹和上次记录的不一样" },
      c: { reachable: false, kind: "auth", error: "账号或凭据被拒绝" },
      d: { reachable: false, kind: "vault", error: "请先解锁密码库" },
      e: { reachable: false, kind: "config", error: "资产配置不完整或无效" },
      f: { reachable: false, kind: "host_key_pending", error: "主机指纹还没有被信任（指纹 SHA256:pending）" },
    });
    await mountPanel();
    for (const id of ["a", "b", "c", "d", "e", "f"]) click(loginTarget(id));
    clickButton(mounted!.container, "开始连接测试");
    await flushUntil(() => mocks.testLogin.mock.calls.length === 6
      && (loginRows()?.textContent.includes("共 6 个") ?? false));
    const loginResults = loginRows();
    const row = (id: string) => loginRow(id);
    expect(row("a")?.textContent).toContain("网络不通");
    expect(row("b")?.textContent).toContain("指纹变了");
    expect(row("b")?.getAttribute("data-tone")).toBe("warn");
    expect(row("c")?.textContent).toContain("登录被拒绝");
    expect(row("d")?.textContent).toContain("密码库未解锁");
    expect(row("d")?.textContent).toContain("请先解锁密码库");
    expect(row("e")?.textContent).toContain("配置有误");
    expect(row("f")?.textContent).toContain("需确认指纹");
    expect(row("f")?.textContent).toContain("SHA256:pending");
    // The panel never offers to trust a host key from a test result.
    expect(row("f")?.querySelector("button")).toBeNull();
    expect(loginResults?.textContent).toContain("共 6 个 · 已验证 0 · 需确认指纹 2 · 失败 4");
    for (const raw of ["network", "host_key_changed", "host_key_pending", "vault"]) {
      expect(loginResults?.textContent).not.toContain(raw);
    }
  });

  it("never shows a port-only result as a verified login", async () => {
    const database = { ...asset("c", "db-c", 30), kind: "mysql" } as Asset;
    mocks.assetList.mockResolvedValue([...assets, database].map((item) => ({ ...item })));
    const { describeLoginResult } = await import("../../features/execbatch/BulkMaintenancePanel");
    const portOnly = describeLoginResult(
      { assetId: "c", reachable: true, kind: "port_only", error: "仅确认端口可以连通，没有验证账号和密码", durationMs: 3 },
      "mysql",
      "db-c",
    );
    expect(portOnly.tone).toBe("partial");
    expect(portOnly.status).toBe("仅端口通");
    expect(portOnly.detail).toContain("没有验证账号和密码");
    // Even without the backend marker, a reachable kind with no real login path is not a verified login.
    expect(describeLoginResult({ assetId: "c", reachable: true, durationMs: 3 }, "winrm", "w").tone).toBe("partial");
    expect(describeLoginResult({ assetId: "a", reachable: true, durationMs: 3 }, "ssh", "a").tone).toBe("ok");
    const verified = describeLoginResult({ assetId: "c", reachable: true, durationMs: 3 }, "redis", "r", true);
    expect(verified.tone).toBe("ok");
    expect(verified.status).toBe("已登录数据库");
    expect(verified.detail).toContain("已用资产绑定的凭据登录数据库");
    const anonymous = describeLoginResult({ assetId: "c", reachable: true, durationMs: 3 }, "redis", "r", false);
    expect(anonymous.detail).toContain("没有绑定凭据");
    expect(anonymous.detail).not.toContain("已用资产绑定的凭据");
  });

  it("retries only unresolved failures and merges the new results in place", async () => {
    let bAttempts = 0;
    mocks.testLogin.mockImplementation((ids: string[]) => {
      if (ids[0] === "b") {
        bAttempts += 1;
        return Promise.resolve({ results: [bAttempts === 1
          ? resultFor("b", { reachable: false, kind: "timeout", error: "连接 b:22 超时，请检查网络和防火墙", durationMs: 10000 })
          : resultFor("b")] });
      }
      return Promise.resolve({ results: [resultFor(ids[0])] });
    });
    await mountPanel();
    click(loginTarget("a"));
    click(loginTarget("b"));
    clickButton(mounted!.container, "开始连接测试");
    await flushUntil(() => mounted?.container.textContent.includes("重试未通过项（1）") ?? false);
    clickButton(mounted!.container, "重试未通过项（1）");
    await flushUntil(() => mocks.testLogin.mock.calls.length === 3
      && (mounted?.container.textContent.includes("已验证 2") ?? false));
    expect(mocks.testLogin).toHaveBeenLastCalledWith(["b"], 10000, 1);
    expect(loginRow("a")?.textContent).toContain("已验证登录");
    expect(loginRow("b")?.textContent).toContain("已验证登录");
    expect(mounted!.container.textContent).not.toContain("重试未通过项");
  });

  it("offers database assets for testing and reports what was actually verified", async () => {
    const dbAsset = (id: string, kind: string, credId: string | null): Asset => ({ ...asset(id, `db-${id}`, 30), kind, credId } as Asset);
    mocks.assetList.mockResolvedValue([
      ...assets, dbAsset("m", "mysql", "cred-1"), dbAsset("r", "redis", null), dbAsset("w", "winrm", null),
      dbAsset("x", "mongodb", null),
    ].map((item) => ({ ...item })));
    answerTestLogin({
      m: { reachable: false, kind: "tls", error: "MySQL 127.0.0.1 的 TLS 校验没有通过" },
      w: { kind: "port_only", error: "仅确认端口可以连通，没有验证账号和密码" },
    });
    await mountPanel();
    // SSH-only batch actions never list database assets; the test picker lists the DB kinds with a real login path.
    expect(target("a")).toBeTruthy();
    expect(mounted!.container.querySelector('input[type="checkbox"][value="m"]:not([data-testid="login-test-targets"] *)')).toBeNull();
    expect(loginTarget("m")).toBeTruthy();
    expect(() => loginTarget("w")).toThrow();
    expect(() => loginTarget("x")).toThrow();
    click(loginTarget("m"));
    click(loginTarget("r"));
    clickButton(mounted!.container, "开始连接测试");
    await flushUntil(() => mocks.testLogin.mock.calls.length === 2 && (loginRows()?.textContent.includes("共 2 个") ?? false));
    expect(loginRow("m")?.textContent).toContain("TLS 校验失败");
    expect(loginRow("r")?.textContent).toContain("已登录数据库");
    expect(loginRow("r")?.textContent).toContain("没有绑定凭据");
    expect(loginRow("r")?.textContent).not.toContain("仅端口通");
  });

  it("cancels a running batch, marks the rest as canceled and keeps them out of retry-failed", async () => {
    const pending = new Map<string, (value: unknown) => void>();
    mocks.testLogin.mockImplementation((ids: string[]) => new Promise((resolve) => { pending.set(ids[0], resolve); }));
    const ids = ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j"];
    mocks.assetList.mockResolvedValue(ids.map((id, index) => asset(id, `host-${id}`, index)).map((item) => ({ ...item })));
    await mountPanel();
    for (const id of ids) click(loginTarget(id));
    clickButton(mounted!.container, "开始连接测试");
    await flushUntil(() => pending.size === 8);
    // One finishes with a real failure before the cancel.
    pending.get("a")!({ results: [resultFor("a", { reachable: false, kind: "auth", error: "账号或凭据被拒绝" })] });
    await flushUntil(() => loginRow("a") !== null);
    clickButton(mounted!.container, "取消测试");
    await flushUntil(() => mounted!.container.textContent.includes("已取消 "));
    // No further asset was queued after the cancel, and the UI is free again.
    expect(mocks.testLogin.mock.calls.length).toBeLessThanOrEqual(9);
    const calledBeforeLate = mocks.testLogin.mock.calls.length;
    pending.get("b")!({ results: [resultFor("b")] });
    await flushUntil(() => true);
    expect(mocks.testLogin.mock.calls.length).toBe(calledBeforeLate);
    expect(loginRow("a")?.textContent).toContain("登录被拒绝");
    expect(loginRow("b")?.getAttribute("data-tone")).toBe("canceled");
    expect(loginRow("b")?.textContent).toContain("结果不采用");
    expect(loginRow("j")?.getAttribute("data-tone")).toBe("canceled");
    expect(loginRow("j")?.textContent).toContain("还没有开始测试");
    expect(loginRows()?.textContent).toContain("失败 1 · 已取消 9");
    // Canceled items are not failures: retry-failed counts only the real failure.
    expect(mounted!.container.textContent).toContain("重试未通过项（1）");
    expect(mounted!.container.textContent).toContain("测试已取消项（9）");

    mocks.testLogin.mockImplementation((requested: string[]) => Promise.resolve({ results: [resultFor(requested[0])] }));
    mocks.testLogin.mockClear();
    clickButton(mounted!.container, "测试已取消项（9）");
    await flushUntil(() => mocks.testLogin.mock.calls.length === 9 && (loginRows()?.textContent.includes("已验证 9") ?? false));
    expect(loginRow("a")?.textContent).toContain("登录被拒绝");
    expect(loginRows()?.textContent).not.toContain("已取消");
  });

  it("shows a request that fails outright as a per-asset result instead of a panel error", async () => {
    mocks.testLogin.mockRejectedValue(new Error("ipc unavailable"));
    await mountPanel();
    click(loginTarget("a"));
    clickButton(mounted!.container, "开始连接测试");
    await flushUntil(() => loginRow("a") !== null);
    expect(loginRow("a")?.textContent).toContain("测试出错");
    expect(mounted!.container.querySelector('[role="alert"]')).toBeNull();
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
          requiredEnvironmentNames: ["production"],
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
    const environmentInput = dialog.querySelector<HTMLInputElement>('input[aria-label="输入环境名 session-a"]');
    if (!environmentInput) throw new Error("typed environment input not found");
    setInputValue(environmentInput, "production");
    clickButton(dialog, "确认执行（1）");
    await flushUntil(() => mocks.deploy.mock.calls.length === 2
      && (mounted?.container.textContent.includes("原文件已创建只读备份") ?? false));
    expect(mocks.deploy.mock.calls[1][0].productionConfirmation).toEqual({
      confirmed: true,
      targetSetHash,
      commandHash,
      environmentNames: ["production"],
    });
  });
});
