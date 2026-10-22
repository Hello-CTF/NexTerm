import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  dockerApi,
  type DockerProductionConfirmation,
  type DockerPruneKind,
  type DockerPrunePreview,
  type DockerPruneResult,
} from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { describeDockerError } from "./dockerErrors";
import { IconRefresh, IconTrash } from "../../ui/icons";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { dockerConfirmation, getDockerConfirmationChallenge, type DockerConfirmationChallenge } from "./productionGuard";
import { formatDockerBytes } from "./diskFormat";

const CATEGORY_LABELS = {
  images: "镜像",
  containers: "容器",
  volumes: "卷",
  buildCache: "Build cache",
} as const;

const PRUNE_LABELS: Record<DockerPruneKind, string> = {
  containers: "停止的容器",
  images: "未使用的悬空镜像",
  volumes: "无容器使用的卷",
  networks: "空用户网络",
  buildCache: "可回收 build cache",
};

const DEFAULT_PRUNE: DockerPruneKind[] = ["containers", "images", "networks", "buildCache"];

type PendingProduction = {
  challenge: DockerConfirmationChallenge;
  preview: DockerPrunePreview;
};

export function DockerDiskPanel({ sessionId, visible }: { sessionId: string; visible: boolean }) {
  const qc = useQueryClient();
  const query = useQuery({
    queryKey: ["docker-disk", sessionId],
    queryFn: () => dockerApi.diskUsage(sessionId),
    enabled: visible,
    refetchInterval: false,
  });
  const [selected, setSelected] = useState<Set<DockerPruneKind>>(() => new Set(DEFAULT_PRUNE));
  const [preview, setPreview] = useState<DockerPrunePreview | null>(null);
  const [result, setResult] = useState<DockerPruneResult | null>(null);
  const [production, setProduction] = useState<PendingProduction | null>(null);
  const [busy, setBusy] = useState<"preview" | "prune" | null>(null);
  const [error, setError] = useState<string | null>(null);

  const toggle = (kind: DockerPruneKind) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(kind)) next.delete(kind);
      else next.add(kind);
      return next;
    });
    setPreview(null);
    setResult(null);
    setError(null);
  };

  const createPreview = async () => {
    if (busy || selected.size === 0) return;
    setBusy("preview");
    setError(null);
    setResult(null);
    try {
      setPreview(await dockerApi.prunePreview(sessionId, [...selected]));
    } catch (cause) {
      setPreview(null);
      setError(`prune 预览失败：${describeError(cause)}`);
    } finally {
      setBusy(null);
    }
  };

  const execute = async (target: DockerPrunePreview, confirmation?: DockerProductionConfirmation) => {
    if (busy) return;
    setBusy("prune");
    setError(null);
    try {
      const value = await dockerApi.prune(sessionId, target.token, target.digest, confirmation);
      setResult(value);
      setPreview(null);
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["docker-disk", sessionId] }),
        qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] }),
        qc.invalidateQueries({ queryKey: ["docker-images", sessionId] }),
        qc.invalidateQueries({ queryKey: ["docker-compose-stacks", sessionId] }),
      ]);
    } catch (cause) {
      const challenge = getDockerConfirmationChallenge(cause);
      if (challenge) setProduction({ challenge, preview: target });
      else setError(`prune 执行失败：${describeError(cause)}`);
    } finally {
      setBusy(null);
    }
  };

  const data = query.data;
  return (
    <div className="min-h-0 flex-1 overflow-auto p-3">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium text-neutral-200">Docker 磁盘与清理</span>
        <span className="nx-hint">df -v · 手动刷新</span>
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => {
          setPreview(null);
          setResult(null);
          void query.refetch();
        }}>
          <IconRefresh size={12} />刷新
        </button>
      </div>
      {query.isPending && <div className="nx-hint py-8 text-center">正在读取 docker system df -v…</div>}
      {query.isError && !data && (
        <div role="alert" className="rounded border border-red-900/60 bg-red-950/20 px-3 py-3 text-xs text-red-300">
          磁盘信息加载失败：{describeDockerError(query.error)}
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm ml-2" onClick={() => void query.refetch()}>重试</button>
        </div>
      )}
      {data && (
        <>
          <div className="mb-4 grid grid-cols-2 gap-2 lg:grid-cols-4">
            {data.categories.map((category) => (
              <div key={category.kind} className="rounded border border-neutral-800/70 bg-neutral-950/30 p-3">
                <div className="text-xs text-neutral-400">{CATEGORY_LABELS[category.kind]}</div>
                <div className="mt-1 text-lg text-neutral-100">{formatDockerBytes(category.sizeBytes)}</div>
                <div className="mt-1 text-[11px] text-neutral-500">{category.active} 活跃 / 共 {category.total}</div>
                <div className="mt-1 text-[11px] text-amber-300">可回收 {formatDockerBytes(category.reclaimableBytes)}</div>
              </div>
            ))}
          </div>

          <div className="grid gap-3 xl:grid-cols-2">
            <section className="min-w-0">
              <h3 className="mb-1.5 text-xs font-medium text-neutral-300">卷与容器使用关系</h3>
              <table className="nx-table nx-table-fixed">
                <thead><tr><th>卷</th><th style={{ width: 100 }}>大小</th><th style={{ width: 180 }}>容器</th></tr></thead>
                <tbody>
                  {data.volumes.length === 0 && <tr><td colSpan={3} className="nx-table-empty">没有卷</td></tr>}
                  {data.volumes.map((volume) => (
                    <tr key={volume.name}>
                      <td className="truncate font-mono" title={volume.name}>{volume.name}<span className="ml-1 text-neutral-500">{volume.driver}</span></td>
                      <td>{formatDockerBytes(volume.sizeBytes)}</td>
                      <td className="truncate text-[11px]" title={volume.containers.map((item) => item.name).join(", ")}>
                        {volume.containers.length > 0 ? volume.containers.map((item) => item.name).join(", ") : "未使用"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
            <section className="min-w-0">
              <h3 className="mb-1.5 text-xs font-medium text-neutral-300">网络与容器使用关系</h3>
              <table className="nx-table nx-table-fixed">
                <thead><tr><th>网络</th><th style={{ width: 100 }}>范围</th><th style={{ width: 180 }}>容器</th></tr></thead>
                <tbody>
                  {data.networks.length === 0 && <tr><td colSpan={3} className="nx-table-empty">没有网络</td></tr>}
                  {data.networks.map((network) => (
                    <tr key={network.id || network.name}>
                      <td className="truncate font-mono" title={network.name}>{network.name}{network.builtin && <span className="nx-badge ml-1">内置</span>}</td>
                      <td className="text-neutral-500">{network.scope}</td>
                      <td className="truncate text-[11px]" title={network.containers.map((item) => item.name).join(", ")}>
                        {network.containers.length > 0 ? network.containers.map((item) => item.name).join(", ") : "无容器"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
          </div>
        </>
      )}

      <section className="mt-4 border-t border-neutral-800/70 pt-3">
        <h3 className="mb-2 text-xs font-medium text-neutral-300">Prune 预览与确认</h3>
        <div className="flex flex-wrap gap-x-4 gap-y-2">
          {(Object.keys(PRUNE_LABELS) as DockerPruneKind[]).map((kind) => (
            <label key={kind} className="flex items-center gap-1.5 text-xs text-neutral-300">
              <input type="checkbox" className="nx-check" checked={selected.has(kind)} disabled={busy !== null} onChange={() => toggle(kind)} />
              {PRUNE_LABELS[kind]}
            </label>
          ))}
        </div>
        <p className="mt-2 text-[11px] text-neutral-500">悬空镜像和无容器卷仅从当前只读快照生成；执行时按预览 ID 逐项删除，不会扩大范围。卷默认不选中。</p>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm mt-2" disabled={busy !== null || selected.size === 0} onClick={() => void createPreview()}>
          {busy === "preview" ? <IconRefresh size={12} className="animate-spin" /> : <IconTrash size={12} />}
          生成精确预览
        </button>
        {error && <div role="alert" className="mt-2 text-xs text-red-300">{error}</div>}
        {preview && (
          <div className="mt-3 rounded border border-amber-900/60 bg-amber-950/10 p-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs font-medium text-amber-300">将删除 {preview.items.length} 项 · 释放 {formatDockerBytes(preview.totalBytes)}</span>
              <span className="nx-hint">预览过期时间 {new Date(preview.expiresAt).toLocaleTimeString()}</span>
              <div className="nx-spacer" />
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy !== null} onClick={() => setPreview(null)}>取消</button>
              <button type="button" className="nx-btn nx-btn-danger nx-btn-sm" disabled={busy !== null || preview.items.length === 0} onClick={() => void execute(preview)}>
                {busy === "prune" ? "执行中…" : "确认删除预览项"}
              </button>
            </div>
            {preview.items.length === 0 ? (
              <div className="mt-2 text-xs text-neutral-500">当前选择没有可删除项。</div>
            ) : (
              <ul className="mt-2 max-h-64 overflow-auto">
                {preview.items.map((item) => (
                  <li key={`${item.kind}:${item.id}`} className="flex items-center gap-2 border-b border-amber-900/20 py-1 font-mono text-[11px] last:border-b-0">
                    <span className="nx-badge shrink-0">{PRUNE_LABELS[item.kind]}</span>
                    <span className="min-w-0 flex-1 truncate" title={`${item.name} (${item.id})`}>{item.name}</span>
                    <span className="shrink-0 text-amber-300">{formatDockerBytes(item.bytes)}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        {result && (
          <div className="mt-3 rounded border border-neutral-800/70 p-3" role="status">
            <div className="text-xs text-green-300">已处理 {result.items.length} 项 · 成功项释放 {formatDockerBytes(result.freedBytes)}</div>
            <ul className="mt-2 max-h-56 overflow-auto">
              {result.items.map((entry) => (
                <li key={`${entry.item.kind}:${entry.item.id}`} className={`py-1 text-[11px] ${entry.error ? "text-red-300" : "text-neutral-400"}`}>
                  {entry.item.name} · {entry.error ?? "已删除"}
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>
      {production && (
        <ProductionConfirmationDialog
          challenge={production.challenge}
          contentHash={production.challenge.commandHash}
          command={`docker prune · ${production.preview.items.length} 项 · ${formatDockerBytes(production.preview.totalBytes)}`}
          onCancel={() => setProduction(null)}
          onConfirm={(environmentNames) => {
            const current = production;
            setProduction(null);
            void execute(current.preview, dockerConfirmation(current.challenge, environmentNames));
          }}
        />
      )}
    </div>
  );
}
