import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { assetApi, vaultApi, type Asset, type SessionInfo } from "../../ipc/commands";
import {
  createExecBatchId,
  execBatchApi,
  type ExecBatchResult,
} from "../../ipc/execBatchApi";
import { getProductionChallenge, type ExecBatchProductionChallenge } from "../production/productionChallenge";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { IconClose, IconEdit, IconKey, IconPlay, IconPlug, IconServer } from "../../ui/icons";
import { assetTagValues } from "../explorer/assetSearch";
import { ProductionConfirmationDialog } from "./ProductionConfirmationDialog";
import { TargetSetPicker, type TargetSetPickerTarget } from "./TargetSetPicker";
import {
  maintenanceApi,
  type AssetBulkEditIPCRequest,
  type AssetProbeResultDTO,
  type DeployPublicKeyRequest,
} from "./maintenanceApi";

const MAX_TARGETS = 64;
// Per-asset requests are issued by a small worker pool so a cancel can stop queueing between assets.
const LOGIN_CONCURRENCY = 8;
const LOGIN_TIMEOUT_MS = 10000;
const DATABASE_KINDS = new Set(["mysql", "postgres", "redis"]);

type MaintenanceResult = {
  assetId: string;
  label: string;
  ok: boolean;
  detail: string;
};

function assetEnvCount(asset: Asset): number {
  const env = asset.options.env;
  return env && typeof env === "object" && !Array.isArray(env) ? Object.keys(env).length : 0;
}

export function maintenanceTargets(assets: readonly Asset[]): TargetSetPickerTarget[] {
  return assets.filter((asset) => asset.kind === "ssh" && !asset.builtin).map((asset) => ({
    id: asset.id,
    label: asset.name,
    sessionName: asset.name,
    endpoint: asset.host ? `${asset.username ? `${asset.username}@` : ""}${asset.host}${asset.port ? `:${asset.port}` : ""}` : null,
    tags: assetTagValues(asset.tags),
    workspaceNames: [],
    envCount: assetEnvCount(asset),
  }));
}

// Connection-test targets: SSH hosts get a real handshake and login; database assets get a
// real login only for the kinds the database workspace can open.
export function loginTestTargets(assets: readonly Asset[]): TargetSetPickerTarget[] {
  return assets.filter((asset) => (asset.kind === "ssh" || DATABASE_KINDS.has(asset.kind)) && !asset.builtin).map((asset) => ({
    id: asset.id,
    label: asset.name,
    sessionName: asset.name,
    endpoint: asset.host ? `${asset.username ? `${asset.username}@` : ""}${asset.host}${asset.port ? `:${asset.port}` : ""}` : null,
    tags: [assetKindLabel(asset.kind), ...assetTagValues(asset.tags)],
    workspaceNames: [],
    envCount: assetEnvCount(asset),
  }));
}

function assetKindLabel(kind: string): string {
  switch (kind) {
    case "mysql": return "MySQL";
    case "postgres": return "PostgreSQL";
    case "redis": return "Redis";
    default: return kind.toUpperCase();
  }
}

function assignment(choice: string): string | undefined {
  if (choice === "keep") return undefined;
  if (choice === "clear") return "";
  return choice.startsWith("id:") ? choice.slice(3) : undefined;
}

function markerLines(result: ExecBatchResult): Set<string> {
  return new Set(result.stdout.split(/\r?\n/).map((line) => line.trim().split(/\s+/, 1)[0]));
}

type LoginTone = "ok" | "partial" | "warn" | "fail" | "canceled";

type LoginResult = {
  assetId: string;
  label: string;
  tone: LoginTone;
  status: string;
  detail: string;
  retryable: boolean;
};

// Status label, tone and retry eligibility per backend result kind. The backend message
// already carries the next step, so the label only names the outcome.
const LOGIN_FAILURE_KINDS: Record<string, { status: string; tone: LoginTone; retryable: boolean }> = {
  host_key_pending: { status: "需确认指纹", tone: "warn", retryable: true },
  host_key_changed: { status: "指纹变了", tone: "warn", retryable: true },
  auth: { status: "登录被拒绝", tone: "fail", retryable: true },
  credential: { status: "凭据不可用", tone: "fail", retryable: true },
  vault: { status: "密码库未解锁", tone: "fail", retryable: true },
  refused: { status: "端口不通", tone: "fail", retryable: true },
  timeout: { status: "连接超时", tone: "fail", retryable: true },
  dns: { status: "域名解析失败", tone: "fail", retryable: true },
  unreachable: { status: "网络不通", tone: "fail", retryable: true },
  network: { status: "网络不通", tone: "fail", retryable: true },
  proxy: { status: "代理连接失败", tone: "fail", retryable: true },
  handshake: { status: "协议握手失败", tone: "fail", retryable: true },
  config: { status: "配置有误", tone: "fail", retryable: false },
  tls: { status: "TLS 校验失败", tone: "fail", retryable: true },
  database: { status: "数据库不可用", tone: "fail", retryable: true },
  tunnel: { status: "跳板不可用", tone: "fail", retryable: true },
  canceled: { status: "已取消", tone: "canceled", retryable: false },
  not_found: { status: "资产不存在", tone: "fail", retryable: false },
  unsupported: { status: "不支持测试", tone: "fail", retryable: false },
  internal: { status: "测试出错", tone: "fail", retryable: true },
};

const LOGIN_TONE_CLASS: Record<LoginTone, string> = {
  ok: "text-green-300",
  partial: "text-amber-300",
  warn: "text-amber-300",
  fail: "text-red-300",
  canceled: "text-neutral-400",
};

type LoginRun = { canceled: boolean; settled: Set<string>; inFlight: Set<string> };

function canceledLoginResult(assetId: string, label: string, inFlight: boolean): LoginResult {
  return {
    assetId, label, tone: "canceled", status: "已取消", retryable: false,
    detail: inFlight
      ? "已取消。这台的检测请求已经发出，结果不采用，可以稍后单独重测"
      : "已取消，这台还没有开始测试",
  };
}

export function describeLoginResult(
  result: AssetProbeResultDTO,
  assetKind: string | undefined,
  label: string,
  hasCredential = true,
): LoginResult {
  const base = { assetId: result.assetId, label };
  if (result.reachable) {
    const database = assetKind !== undefined && DATABASE_KINDS.has(assetKind);
    if (result.kind === "port_only" || (assetKind !== "ssh" && assetKind !== "docker" && assetKind !== "local" && !database)) {
      return {
        ...base, tone: "partial", status: "仅端口通", retryable: false,
        detail: `${result.error || "仅确认端口可以连通，没有验证账号和密码"}（${result.durationMs} ms）`,
      };
    }
    if (assetKind === "local") {
      return { ...base, tone: "ok", status: "本机可用", retryable: false, detail: "本机终端无需登录" };
    }
    if (database) {
      return {
        ...base, tone: "ok", status: "已登录数据库", retryable: false,
        detail: hasCredential
          ? `已用资产绑定的凭据登录数据库并通过连通检查，没有执行任何 SQL（${result.durationMs} ms）`
          : `已连上并通过连通检查，但这条资产没有绑定凭据，是按无密码方式登录的，没有执行任何 SQL（${result.durationMs} ms）`,
      };
    }
    return {
      ...base, tone: "ok", status: "已验证登录", retryable: false,
      detail: `能连上，主机指纹和登录凭据都验证通过（${result.durationMs} ms）`,
    };
  }
  const known = result.kind ? LOGIN_FAILURE_KINDS[result.kind] : undefined;
  return {
    ...base,
    tone: known?.tone ?? "fail",
    status: known?.status ?? "连接失败",
    retryable: known?.retryable ?? true,
    detail: result.error || "没有拿到失败原因，请重试",
  };
}

export function summarizeLoginResults(results: readonly LoginResult[]): string {
  const count = (tone: LoginTone) => results.filter((result) => result.tone === tone).length;
  const parts = [`共 ${results.length} 个`, `已验证 ${count("ok")}`];
  if (count("partial") > 0) parts.push(`仅端口通 ${count("partial")}`);
  if (count("warn") > 0) parts.push(`需确认指纹 ${count("warn")}`);
  parts.push(`失败 ${count("fail")}`);
  if (count("canceled") > 0) parts.push(`已取消 ${count("canceled")}`);
  return parts.join(" · ");
}

function loginResultsText(results: readonly LoginResult[]): string {
  return results.map((result) => `${result.label}\t${result.status}\t${result.detail}`).join("\n");
}

function deployResult(result: ExecBatchResult): { ok: boolean; detail: string } {
  if (result.status !== "success") {
    return { ok: false, detail: result.error || result.stderr || `执行失败（${result.status}）` };
  }
  const markers = markerLines(result);
  if (markers.has("NEXTERM_PUBLIC_KEY_ALREADY_PRESENT")) return { ok: true, detail: "公钥已存在，未重复追加" };
  if (markers.has("NEXTERM_PUBLIC_KEY_ADDED")) {
    return {
      ok: true,
      detail: markers.has("NEXTERM_PUBLIC_KEY_BACKUP") ? "公钥已追加；原文件已创建只读备份" : "公钥已追加到新文件",
    };
  }
  return { ok: false, detail: "远端未返回部署完成标记，请检查 authorized_keys" };
}

function MaintenanceResults({ title, results }: { title: string; results: MaintenanceResult[] }) {
  return (
    <div className="mt-2 rounded border border-neutral-800/70 px-2 py-1.5" aria-label={title}>
      <div className="mb-1 text-[11px] font-medium text-neutral-300">{title}</div>
      {results.map((result) => (
        <div key={result.assetId} className="flex items-start gap-2 border-t border-neutral-800/50 py-1.5 text-[11px] first:border-t-0" data-asset-id={result.assetId}>
          <span className="w-36 shrink-0 truncate text-neutral-300" title={result.assetId}>{result.label}</span>
          <span className={result.ok ? "shrink-0 text-green-300" : "shrink-0 text-red-300"}>{result.ok ? "成功" : "失败"}</span>
          <span className={`min-w-0 flex-1 whitespace-pre-wrap break-all ${result.ok ? "text-neutral-400" : "text-red-300"}`}>{result.detail}</span>
        </div>
      ))}
    </div>
  );
}

function isRetryable(result: LoginResult): boolean {
  return result.tone !== "ok" && result.tone !== "partial" && result.tone !== "canceled" && result.retryable;
}

function LoginResults({
  results,
  busy,
  onRetry,
  onResume,
}: {
  results: LoginResult[];
  busy: boolean;
  onRetry: () => void;
  onResume: () => void;
}) {
  const [copied, setCopied] = useState<"ok" | "failed" | null>(null);
  const retryable = results.filter(isRetryable).length;
  const canceled = results.filter((result) => result.tone === "canceled").length;
  const copy = async () => {
    try {
      await copyText(loginResultsText(results));
      setCopied("ok");
    } catch {
      setCopied("failed");
    }
  };
  return (
    <div className="mt-2 rounded border border-neutral-800/70 px-2 py-1.5" aria-label="连接测试结果">
      <div className="mb-1 flex flex-wrap items-center gap-2 text-[11px]">
        <span className="font-medium text-neutral-300" role="status">{summarizeLoginResults(results)}</span>
        <span className="flex-1" />
        {retryable > 0 && (
          <button type="button" className="nx-btn nx-btn-sm" disabled={busy} onClick={onRetry}>重试未通过项（{retryable}）</button>
        )}
        {canceled > 0 && (
          <button type="button" className="nx-btn nx-btn-sm" disabled={busy} onClick={onResume}>测试已取消项（{canceled}）</button>
        )}
        <button type="button" className="nx-btn nx-btn-sm" onClick={() => void copy()}>
          {copied === "ok" ? "已复制" : copied === "failed" ? "复制失败" : "复制结果"}
        </button>
      </div>
      {results.map((result) => (
        <div key={result.assetId} className="flex items-start gap-2 border-t border-neutral-800/50 py-1.5 text-[11px] first:border-t-0" data-asset-id={result.assetId} data-tone={result.tone}>
          <span className="w-36 shrink-0 truncate text-neutral-300" title={result.assetId}>{result.label}</span>
          <span className={`w-24 shrink-0 ${LOGIN_TONE_CLASS[result.tone]}`}>{result.status}</span>
          <span className="min-w-0 flex-1 whitespace-pre-wrap break-all text-neutral-400">{result.detail}</span>
        </div>
      ))}
    </div>
  );
}

export function BulkMaintenancePanel({ sessions, onClose }: { sessions: SessionInfo[]; onClose?: () => void }) {
  const qc = useQueryClient();
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const groupsQuery = useQuery({ queryKey: ["groups"], queryFn: () => assetApi.groupList(), retry: false });
  const credentialsQuery = useQuery({ queryKey: ["credentials"], queryFn: () => vaultApi.listCredentials(), retry: false });
  const assets = useMemo(() => assetsQuery.data ?? [], [assetsQuery.data]);
  const targets = useMemo(() => maintenanceTargets(assets), [assets]);
  const loginTargets = useMemo(() => loginTestTargets(assets), [assets]);
  const [selected, setSelected] = useState<string[]>([]);
  const [busy, setBusy] = useState<"edit" | "deploy" | "login" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editResults, setEditResults] = useState<MaintenanceResult[]>([]);
  const [deployResults, setDeployResults] = useState<MaintenanceResult[]>([]);
  const [loginResults, setLoginResults] = useState<LoginResult[]>([]);
  const [loginSelected, setLoginSelected] = useState<string[]>([]);
  const [loginProgress, setLoginProgress] = useState<{ done: number; total: number } | null>(null);
  const loginRunRef = useRef<LoginRun | null>(null);
  const loginPendingIds = useRef<string[]>([]);
  const [groupChoice, setGroupChoice] = useState("keep");
  const [jumpChoice, setJumpChoice] = useState("keep");
  const [credentialChoice, setCredentialChoice] = useState("keep");
  const [tagsEnabled, setTagsEnabled] = useState(false);
  const [tags, setTags] = useState("");
  const [portEnabled, setPortEnabled] = useState(false);
  const [port, setPort] = useState("22");
  const [publicKey, setPublicKey] = useState("");
  const [authorizedKeysPath, setAuthorizedKeysPath] = useState("~/.ssh/authorized_keys");
  const [production, setProduction] = useState<{
    request: DeployPublicKeyRequest;
    challenge: ExecBatchProductionChallenge;
  } | null>(null);
  const activeDeployRef = useRef<string | null>(null);

  useEffect(() => {
    const available = new Set(targets.map((target) => target.id));
    setSelected((current) => current.filter((id) => available.has(id)));
  }, [targets]);

  useEffect(() => {
    const available = new Set(loginTargets.map((target) => target.id));
    setLoginSelected((current) => current.filter((id) => available.has(id)));
  }, [loginTargets]);

  useEffect(
    () => () => {
      const active = activeDeployRef.current;
      activeDeployRef.current = null;
      if (active) void execBatchApi.cancel(active).catch(() => undefined);
      if (loginRunRef.current) loginRunRef.current.canceled = true;
    },
    [],
  );

  const selectedAssets = targets.filter((target) => selected.includes(target.id))
    .map((target) => assets.find((asset) => asset.id === target.id))
    .filter((asset): asset is Asset => asset !== undefined);
  const sessionByAsset = useMemo(() => {
    const map = new Map<string, SessionInfo>();
    for (const session of sessions) {
      if (session.kind !== "ssh" || session.status !== "connected" || !session.assetId || map.has(session.assetId)) continue;
      map.set(session.assetId, session);
    }
    return map;
  }, [sessions]);
  const resultLabel = (assetId: string) => assets.find((asset) => asset.id === assetId)?.name ?? assetId;

  const applyBulkEdit = async () => {
    if (busy) return;
    setError(null);
    setEditResults([]);
    if (selectedAssets.length === 0) {
      setError("请至少选择一个 SSH 资产");
      return;
    }
    if (selectedAssets.length > MAX_TARGETS) {
      setError(`每次最多维护 ${MAX_TARGETS} 个资产`);
      return;
    }
    const request: AssetBulkEditIPCRequest = {
      targets: selectedAssets.map((asset) => ({ assetId: asset.id, expectedUpdatedAt: asset.updatedAt })),
    };
    const groupId = assignment(groupChoice);
    const jumpAssetId = assignment(jumpChoice);
    const credentialId = assignment(credentialChoice);
    if (groupId !== undefined) request.groupId = groupId;
    if (jumpAssetId !== undefined) request.jumpAssetId = jumpAssetId;
    if (credentialId !== undefined) request.credentialId = credentialId;
    if (tagsEnabled) request.tags = tags;
    if (portEnabled) {
      const value = Number(port);
      if (!Number.isInteger(value) || value < 1 || value > 65535) {
        setError("端口需在 1-65535 之间");
        return;
      }
      request.port = value;
    }
    if (request.groupId === undefined && request.jumpAssetId === undefined && request.credentialId === undefined
      && request.tags === undefined && request.port === undefined) {
      setError("请至少选择一个要修改的字段");
      return;
    }
    setBusy("edit");
    try {
      const response = await maintenanceApi.bulkEdit(request);
      setEditResults(response.results.map((result) => ({
        assetId: result.assetId,
        label: resultLabel(result.assetId),
        ok: !result.error,
        detail: result.error || `已更新（版本 ${result.updatedAt}）`,
      })));
      await qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["asset-search"] });
      void qc.invalidateQueries({ queryKey: ["credentials"] });
    } catch (cause) {
      setError(describeError(cause));
    } finally {
      setBusy(null);
    }
  };

  const runDeploy = async (request: DeployPublicKeyRequest) => {
    if (activeDeployRef.current) return;
    activeDeployRef.current = request.batchId;
    setBusy("deploy");
    setError(null);
    setDeployResults([]);
    setProduction(null);
    const sessionAssets = new Map(request.sessionIds.map((sessionId) => {
      const assetId = [...sessionByAsset.entries()].find(([, session]) => session.id === sessionId)?.[0] ?? sessionId;
      return [sessionId, assetId] as const;
    }));
    const offline = selectedAssets.filter((asset) => !sessionByAsset.has(asset.id)).map((asset) => ({
      assetId: asset.id, label: asset.name, ok: false, detail: "没有可复用的已连接 SSH 会话",
    }));
    try {
      const batch = await maintenanceApi.deployPublicKey(request);
      const remote = batch.results.map((result) => {
        const assetId = result.assetId || sessionAssets.get(result.sessionId) || result.sessionId;
        const outcome = deployResult(result);
        return { assetId, label: resultLabel(assetId), ...outcome };
      });
      setDeployResults([...offline, ...remote]);
    } catch (cause) {
      const challenge = getProductionChallenge(cause, "exec_batch_production_confirmation");
      if (challenge?.batchId === request.batchId) {
        const unconfirmed = { ...request };
        delete unconfirmed.productionConfirmation;
        setProduction({ request: unconfirmed, challenge });
      } else {
        const message = describeError(cause);
        setError(message);
        setDeployResults(selectedAssets.map((asset) => ({ assetId: asset.id, label: asset.name, ok: false, detail: message })));
      }
    } finally {
      if (activeDeployRef.current === request.batchId) {
        activeDeployRef.current = null;
        setBusy(null);
      }
    }
  };

  const deploy = () => {
    if (busy) return;
    setError(null);
    if (selectedAssets.length === 0) {
      setError("请至少选择一个 SSH 资产");
      return;
    }
    if (!publicKey.trim()) {
      setError("请粘贴要部署的 OpenSSH 公钥");
      return;
    }
    if (selectedAssets.length > MAX_TARGETS) {
      setError(`每次最多维护 ${MAX_TARGETS} 个资产`);
      return;
    }
    const sessionIds = selectedAssets.map((asset) => sessionByAsset.get(asset.id)?.id).filter((id): id is string => id !== undefined);
    if (sessionIds.length === 0) {
      setDeployResults(selectedAssets.map((asset) => ({ assetId: asset.id, label: asset.name, ok: false, detail: "没有可复用的已连接 SSH 会话" })));
      return;
    }
    void runDeploy({
      batchId: createExecBatchId(),
      sessionIds,
      publicKey: publicKey.trim(),
      path: authorizedKeysPath,
      timeoutMs: 30000,
      maxConcurrent: 4,
    });
  };

  const runLoginTest = async (ids: string[], merge: boolean) => {
    if (busy) return;
    setError(null);
    if (ids.length === 0) return;
    if (ids.length > MAX_TARGETS) {
      setError(`每次最多测试 ${MAX_TARGETS} 个资产`);
      return;
    }
    const run: LoginRun = { canceled: false, settled: new Set(), inFlight: new Set() };
    loginRunRef.current = run;
    const order = new Map(ids.map((id, index) => [id, index]));
    const settle = (fresh: LoginResult) => {
      run.settled.add(fresh.assetId);
      setLoginResults((current) => {
        const exists = current.some((result) => result.assetId === fresh.assetId);
        const next = exists
          ? current.map((result) => (result.assetId === fresh.assetId ? fresh : result))
          : [...current, fresh];
        return merge ? next : next.sort((x, y) => (order.get(x.assetId) ?? 0) - (order.get(y.assetId) ?? 0));
      });
      setLoginProgress({ done: run.settled.size, total: ids.length });
    };
    if (!merge) setLoginResults([]);
    setLoginProgress({ done: 0, total: ids.length });
    setBusy("login");
    let cursor = 0;
    const worker = async () => {
      while (!run.canceled && cursor < ids.length) {
        const id = ids[cursor++];
        run.inFlight.add(id);
        const target = assets.find((candidate) => candidate.id === id);
        try {
          const response = await maintenanceApi.testLogin([id], LOGIN_TIMEOUT_MS, 1);
          if (run.canceled) return;
          const result = response.results.find((candidate) => candidate.assetId === id) ?? response.results[0]
            ?? { assetId: id, reachable: false, kind: "internal", error: "", durationMs: 0 };
          settle(describeLoginResult(result, target?.kind, resultLabel(id), Boolean(target?.credId)));
        } catch (cause) {
          if (run.canceled) return;
          settle(describeLoginResult(
            { assetId: id, reachable: false, kind: "internal", error: describeError(cause), durationMs: 0 },
            target?.kind, resultLabel(id),
          ));
        } finally {
          run.inFlight.delete(id);
        }
      }
    };
    await Promise.all(Array.from({ length: Math.min(LOGIN_CONCURRENCY, ids.length) }, worker));
    if (loginRunRef.current === run && !run.canceled) {
      loginRunRef.current = null;
      setBusy(null);
      setLoginProgress(null);
    }
  };

  // Cancel stops queueing and releases the UI at once. Requests already sent cannot be
  // interrupted (each is bounded by the probe timeout), so their late results are dropped.
  const cancelLoginTest = () => {
    const run = loginRunRef.current;
    if (!run || run.canceled) return;
    run.canceled = true;
    loginRunRef.current = null;
    const pending = loginPendingIds.current.filter((id) => !run.settled.has(id));
    setLoginResults((current) => {
      const have = new Set(current.map((result) => result.assetId));
      const added = pending.filter((id) => !have.has(id) || current.find((result) => result.assetId === id)?.tone === "canceled")
        .map((id) => canceledLoginResult(id, resultLabel(id), run.inFlight.has(id)));
      const replaced = new Set(added.map((result) => result.assetId));
      return [...current.filter((result) => !replaced.has(result.assetId)), ...added];
    });
    setBusy(null);
    setLoginProgress(null);
  };

  const startLoginTest = (ids: string[], merge: boolean) => {
    loginPendingIds.current = ids;
    void runLoginTest(ids, merge);
  };

  const testLogin = () => {
    if (busy) return;
    if (loginSelected.length === 0) {
      setError("请至少选择一个要测试的资产");
      return;
    }
    startLoginTest(loginSelected, false);
  };

  const retryLogin = () => startLoginTest(loginResults.filter(isRetryable).map((result) => result.assetId), true);
  const resumeCanceled = () => startLoginTest(loginResults.filter((result) => result.tone === "canceled").map((result) => result.assetId), true);

  return (
    <section className="nx-pane" aria-label="批量主机维护" data-testid="bulk-maintenance-panel">
      <div className="nx-toolbar nx-wrap-toolbar flex-wrap">
        <IconServer size={14} className="text-neutral-500" />
        <h2 className="nx-toolbar-title">批量主机维护</h2>
        <span className="nx-hint">按 SSH 资产选择 · 逐目标返回结果</span>
        {onClose && (
          <>
            <span className="nx-spacer" />
            <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label="关闭批量维护" onClick={onClose}>
              <IconClose size={13} />
            </button>
          </>
        )}
      </div>
      <div className="px-3 pb-3">
        <TargetSetPicker
          targets={targets}
          selectedIds={selected}
          onChange={(ids) => {
            setSelected(ids);
            setProduction(null);
          }}
          disabled={busy !== null || production !== null}
          maxTargets={MAX_TARGETS}
          legend="SSH 资产"
          emptyMessage="没有可维护的 SSH 资产。请先在资产树中创建资产。"
          searchAriaLabel="搜索批量维护目标"
          metadataError={assetsQuery.isError ? describeError(assetsQuery.error) : null}
        />
        {error && <div role="alert" className="mb-3 text-[11px] text-red-300">{error}</div>}

        <details className="mb-2 rounded border border-neutral-800/70 p-2.5" open>
          <summary className="cursor-pointer text-[12px] font-medium text-neutral-200"><IconEdit size={12} className="mr-1 inline" />批量编辑</summary>
          <div className="mt-3 grid gap-2 md:grid-cols-2">
            <label className="flex flex-col gap-1 text-[11px] text-neutral-500">分组
              <select className="nx-select" aria-label="批量编辑分组" value={groupChoice} onChange={(event) => setGroupChoice(event.target.value)}>
                <option value="keep">不修改</option><option value="clear">移到未分组</option>
                {(groupsQuery.data ?? []).map((group) => <option key={group.id} value={`id:${group.id}`}>{group.name}</option>)}
              </select>
            </label>
            <label className="flex flex-col gap-1 text-[11px] text-neutral-500">跳板机
              <select className="nx-select" aria-label="批量编辑跳板机" value={jumpChoice} onChange={(event) => setJumpChoice(event.target.value)}>
                <option value="keep">不修改</option><option value="clear">不使用跳板机</option>
                {assets.filter((asset) => asset.kind === "ssh" || asset.kind === "docker").map((asset) => (
                  <option key={asset.id} value={`id:${asset.id}`}>{asset.name}（{asset.host ?? asset.id}）</option>
                ))}
              </select>
            </label>
            <label className="flex flex-col gap-1 text-[11px] text-neutral-500">凭据标识（不展示明文）
              <select className="nx-select" aria-label="批量编辑凭据" value={credentialChoice} onChange={(event) => setCredentialChoice(event.target.value)}>
                <option value="keep">不修改</option><option value="clear">清除凭据绑定</option>
                {(credentialsQuery.data ?? []).map((credential) => (
                  <option key={credential.id} value={`id:${credential.id}`}>{credential.name}（{credential.kind} · {credential.id}）</option>
                ))}
              </select>
            </label>
            <label className="flex flex-col gap-1 text-[11px] text-neutral-500">端口
              <span className="flex items-center gap-2">
                <input type="checkbox" aria-label="修改端口" checked={portEnabled} onChange={(event) => setPortEnabled(event.target.checked)} />
                <input className="nx-input" type="number" aria-label="批量编辑端口" min={1} max={65535} value={port} disabled={!portEnabled} onChange={(event) => setPort(event.target.value)} />
              </span>
            </label>
            <label className="flex flex-col gap-1 text-[11px] text-neutral-500 md:col-span-2">标签（逗号分隔，批量替换）
              <span className="flex items-center gap-2">
                <input type="checkbox" aria-label="修改标签" checked={tagsEnabled} onChange={(event) => setTagsEnabled(event.target.checked)} />
                <input className="nx-input" aria-label="批量编辑标签" value={tags} disabled={!tagsEnabled} onChange={(event) => setTags(event.target.value)} />
              </span>
            </label>
          </div>
          <div className="mt-2 text-[10px] text-neutral-500">仅显示凭据名称、类型和 ID，不返回或记录明文内容；CAS 冲突目标可刷新后单独重试。</div>
          <button type="button" className="nx-btn nx-btn-primary mt-3" disabled={busy !== null} onClick={() => void applyBulkEdit()}>
            {busy === "edit" ? "正在编辑…" : "应用批量编辑"}
          </button>
          {editResults.length > 0 && <MaintenanceResults title="批量编辑逐目标结果" results={editResults} />}
        </details>

        <details className="mb-2 rounded border border-neutral-800/70 p-2.5">
          <summary className="cursor-pointer text-[12px] font-medium text-neutral-200"><IconKey size={12} className="mr-1 inline" />部署公钥</summary>
          <div className="mt-3 flex flex-col gap-2">
            <label className="flex flex-col gap-1 text-[11px] text-neutral-500">OpenSSH 公钥（单行）
              <textarea className="nx-input min-h-20 font-mono text-xs" aria-label="部署公钥内容" value={publicKey} onChange={(event) => { setPublicKey(event.target.value); setProduction(null); }} />
            </label>
            <label className="flex flex-col gap-1 text-[11px] text-neutral-500">authorized_keys 路径
              <input className="nx-input font-mono text-xs" aria-label="authorized_keys 路径" value={authorizedKeysPath} onChange={(event) => { setAuthorizedKeysPath(event.target.value); setProduction(null); }} />
            </label>
            <div className="text-[10px] text-neutral-500">先读取并检查完整行；已存在则不修改。新增前备份原文件并去除备份写权限。每个资产复用一个现有会话，不会自动连接。</div>
            <button type="button" className="nx-btn nx-btn-primary self-start" disabled={busy !== null} onClick={deploy}>
              <IconPlay size={12} />{busy === "deploy" ? "正在部署…" : "部署公钥"}
            </button>
          </div>
          {deployResults.length > 0 && <MaintenanceResults title="公钥部署逐目标结果" results={deployResults} />}
        </details>

        <details className="rounded border border-neutral-800/70 p-2.5">
          <summary className="cursor-pointer text-[12px] font-medium text-neutral-200"><IconPlug size={12} className="mr-1 inline" />连接测试</summary>
          <div className="mt-2 text-[10px] text-neutral-500">
            SSH 主机：核对主机指纹和账号凭据，不打开终端、不执行命令。遇到没信任过的主机指纹时不会自动信任，请先到终端里核对并确认，再回来重新测试。
            MySQL、PostgreSQL、Redis：用资产绑定的凭据连接并做一次连通检查，不执行任何 SQL，也不保存连接；TLS 证书校验按资产设置进行，不会自动信任。
            其他类型只确认端口能连通，会标为“仅端口通”，说明账号密码还没有验证。
          </div>
          <div className="mt-2" data-testid="login-test-targets">
            <TargetSetPicker
              targets={loginTargets}
              selectedIds={loginSelected}
              onChange={setLoginSelected}
              disabled={busy !== null}
              maxTargets={MAX_TARGETS}
              legend="测试对象"
              emptyMessage="没有可测试的资产。"
              searchAriaLabel="搜索连接测试对象"
              metadataError={assetsQuery.isError ? describeError(assetsQuery.error) : null}
            />
            {selected.length > 0 && (
              <button
                type="button"
                className="nx-btn nx-btn-sm mb-2"
                disabled={busy !== null}
                onClick={() => setLoginSelected(selected)}
              >沿用上方已选的 {selected.length} 个 SSH 资产</button>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <button type="button" className="nx-btn" disabled={busy !== null} onClick={testLogin}>
              {busy === "login" ? `正在测试 ${loginProgress?.done ?? 0}/${loginProgress?.total ?? 0}…` : "开始连接测试"}
            </button>
            {busy === "login" && (
              <button type="button" className="nx-btn" onClick={cancelLoginTest}>取消测试</button>
            )}
            {busy === "login" && <span className="text-[10px] text-neutral-500">每台最多等待 10 秒；取消后已发出的请求结果不采用</span>}
          </div>
          {loginResults.length > 0 && (
            <LoginResults results={loginResults} busy={busy !== null} onRetry={retryLogin} onResume={resumeCanceled} />
          )}
        </details>
      </div>

      {production && (
        <ProductionConfirmationDialog
          challenge={production.challenge}
          contentHash={production.challenge.commandHash}
          command={`向 ${authorizedKeysPath} 追加公钥：\n${publicKey}`}
          onCancel={() => setProduction(null)}
          onConfirm={(environmentNames) => void runDeploy({
            ...production.request,
            productionConfirmation: {
              confirmed: true,
              targetSetHash: production.challenge.targetSetHash,
              commandHash: production.challenge.commandHash,
              ...(environmentNames ? { environmentNames } : {}),
            },
          })}
        />
      )}
    </section>
  );
}

export function BulkMaintenanceDialog({ sessions, onClose }: { sessions: SessionInfo[]; onClose: () => void }) {
  const modalRef = useRef<HTMLDivElement>(null);
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => modalRef.current });
  return createPortal(
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal flex max-h-[88vh] nx-modal-lg flex-col"
        role="dialog"
        aria-modal="true"
        aria-label="批量主机维护"
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          event.stopPropagation();
          if (!layer.isTopmost()) return;
          if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
            event.preventDefault();
            onClose();
            return;
          }
          trapOverlayTab(event, modalRef.current);
        }}
      >
        <div className="min-h-0 overflow-y-auto">
          <BulkMaintenancePanel sessions={sessions} onClose={onClose} />
        </div>
      </div>
    </div>,
    document.body,
  );
}
