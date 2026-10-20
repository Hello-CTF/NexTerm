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
import { describeError } from "../../ui/errorText";
import { IconClose, IconEdit, IconKey, IconPlay, IconPlug, IconServer } from "../../ui/icons";
import { assetTagValues } from "../explorer/assetSearch";
import { ProductionConfirmationDialog } from "./ProductionConfirmationDialog";
import { TargetSetPicker, type TargetSetPickerTarget } from "./TargetSetPicker";
import {
  maintenanceApi,
  type AssetBulkEditIPCRequest,
  type DeployPublicKeyRequest,
} from "./maintenanceApi";

const MAX_TARGETS = 64;

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

function assignment(choice: string): string | undefined {
  if (choice === "keep") return undefined;
  if (choice === "clear") return "";
  return choice.startsWith("id:") ? choice.slice(3) : undefined;
}

function markerLines(result: ExecBatchResult): Set<string> {
  return new Set(result.stdout.split(/\r?\n/).map((line) => line.trim().split(/\s+/, 1)[0]));
}

// Accurate per-class labels for the real handshake probe (network/host-key/auth/vault/config);
// anything unmapped stays visible as the raw kind instead of being mislabeled.
const PROBE_KIND_LABELS: Record<string, string> = {
  network: "网络不可达",
  unreachable: "主机不可达",
  refused: "连接被拒绝",
  dns: "DNS 解析失败",
  timeout: "连接超时",
  host_key_pending: "主机密钥待确认",
  host_key_changed: "主机密钥已变更",
  auth: "认证失败",
  vault: "凭据库不可用",
  config: "配置无效",
  proxy: "跳板/代理连接失败",
  handshake: "握手失败",
  canceled: "已取消",
  not_found: "资产不存在",
  unsupported: "不支持探测",
  internal: "内部错误",
};

function probeFailureLabel(kind: string | undefined): string {
  if (!kind) return "连接失败";
  return PROBE_KIND_LABELS[kind] ?? `连接失败（${kind}）`;
}

// SSH and Docker assets get a real host-key+credential handshake (M13); other kinds keep the
// TCP reachability probe, which says nothing about authentication.
function probeSuccessLabel(assetKind: string | undefined): string {
  if (assetKind === "ssh" || assetKind === "docker") return "SSH 握手成功";
  if (assetKind === "local") return "本地可用";
  return "TCP 连接可达";
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

export function BulkMaintenancePanel({ sessions, onClose }: { sessions: SessionInfo[]; onClose?: () => void }) {
  const qc = useQueryClient();
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const groupsQuery = useQuery({ queryKey: ["groups"], queryFn: () => assetApi.groupList(), retry: false });
  const credentialsQuery = useQuery({ queryKey: ["credentials"], queryFn: () => vaultApi.listCredentials(), retry: false });
  const assets = useMemo(() => assetsQuery.data ?? [], [assetsQuery.data]);
  const targets = useMemo(() => maintenanceTargets(assets), [assets]);
  const [selected, setSelected] = useState<string[]>([]);
  const [busy, setBusy] = useState<"edit" | "deploy" | "login" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editResults, setEditResults] = useState<MaintenanceResult[]>([]);
  const [deployResults, setDeployResults] = useState<MaintenanceResult[]>([]);
  const [loginResults, setLoginResults] = useState<MaintenanceResult[]>([]);
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

  useEffect(
    () => () => {
      const active = activeDeployRef.current;
      activeDeployRef.current = null;
      if (active) void execBatchApi.cancel(active).catch(() => undefined);
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

  const testLogin = async () => {
    if (busy) return;
    setError(null);
    setLoginResults([]);
    if (selectedAssets.length === 0) {
      setError("请至少选择一个 SSH 资产");
      return;
    }
    if (selectedAssets.length > MAX_TARGETS) {
      setError(`每次最多维护 ${MAX_TARGETS} 个资产`);
      return;
    }
    setBusy("login");
    try {
      const response = await maintenanceApi.testLogin(selectedAssets.map((asset) => asset.id));
      setLoginResults(response.results.map((result) => {
        const asset = assets.find((candidate) => candidate.id === result.assetId);
        return {
          assetId: result.assetId,
          label: resultLabel(result.assetId),
          ok: result.reachable,
          detail: result.reachable
            ? `${probeSuccessLabel(asset?.kind)}（${result.durationMs} ms）`
            : `${probeFailureLabel(result.kind)}：${result.error || "不可达"}`,
        };
      }));
    } catch (cause) {
      setError(describeError(cause));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section className="nx-pane" aria-label="批量主机维护" data-testid="bulk-maintenance-panel">
      <div className="nx-toolbar flex-wrap">
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
          <summary className="cursor-pointer text-[12px] font-medium text-neutral-200"><IconPlug size={12} className="mr-1 inline" />test login</summary>
          <div className="mt-2 text-[10px] text-neutral-500">SSH 与 Docker 资产执行真实 host-key+凭据握手（不注册会话、不自动接受未知主机密钥）；其他资产仅 TCP 可达性探测，不代表凭据已认证。</div>
          <button type="button" className="nx-btn mt-3" disabled={busy !== null} onClick={() => void testLogin()}>
            {busy === "login" ? "正在测试…" : "test login / 连接测试"}
          </button>
          {loginResults.length > 0 && <MaintenanceResults title="test login 逐目标结果" results={loginResults} />}
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
        className="nx-modal flex max-h-[88vh] max-w-[900px] flex-col"
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
