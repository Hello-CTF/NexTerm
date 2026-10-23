import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { assetApi } from "../../ipc/commands";
import {
  inventoryApi,
  type InventoryDiscoveryDTO,
  type InventorySource,
  type InventorySyncDTO,
} from "../../ipc/inventoryApi";
import { WEB } from "../../ipc/env";
import { useUi } from "../../app/store";
import { useAuth } from "../auth/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconAlert, IconLoader, IconServer, IconXCircle } from "../../ui/icons";
import { inventoryExecTarget, inventorySourceLabel, inventoryTargetText } from "./inventory";
import { InventoryTargetActions, targetExecutable, type InventoryTargetSession } from "./InventoryTargetActions";

const SOURCE_ORDER: InventorySource[] = ["ssh-config", "docker-context", "kubernetes"];
const SOURCE_LABEL: Record<InventorySource, string> = {
  "ssh-config": "SSH config",
  "docker-context": "Docker contexts",
  kubernetes: "Kubernetes",
};
const DEFAULT_GROUP: Record<InventorySource, string> = {
  "ssh-config": "ssh-config",
  "docker-context": "docker-contexts",
  kubernetes: "kubernetes",
};

type Busy = "discover" | "sync" | "watch" | null;

export function InventoryDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => assetApi.groupList(), refetchOnWindowFocus: false });
  const watchQuery = useQuery({
    queryKey: ["inventory-ssh-watch"],
    queryFn: () => inventoryApi.sshWatchStatus(),
    refetchOnWindowFocus: false,
  });
  const [source, setSource] = useState<InventorySource>("ssh-config");
  const [path, setPath] = useState("");
  const [groupId, setGroupId] = useState("");
  const [discovery, setDiscovery] = useState<InventoryDiscoveryDTO | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [result, setResult] = useState<InventorySyncDTO | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<Busy>(null);
  const [execKey, setExecKey] = useState<string | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const pathRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const pathId = useId();
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => pathRef.current });

  // The exec target actions bind every operation to the account that
  // launched it: the owner key matches the IPC owner derivation (the
  // account's user id, or the shared desktop default when ownerless), and
  // the session select offers the account's own Docker-host sessions.
  const user = useAuth((state) => state.user);
  const ownerKey = user?.id ?? "default";
  const sessions = useUi((state) => state.sessions);

  // Async completions must not outlive the account that started them: after every await, shared
  // side effects (query-cache writes, invalidations, refetches) require the dialog to still be
  // mounted on the same account epoch. A stale completion does nothing at all — the account-epoch
  // remount in the asset tree replaces the whole dialog on a switch, so the old instance's state
  // is irrelevant and only the new account's cache may be written.
  const accountEpoch = useUi((state) => state.accountEpoch);
  const mountedRef = useRef(false);
  const epochRef = useRef(accountEpoch);
  useEffect(() => {
    mountedRef.current = true;
    epochRef.current = accountEpoch;
    return () => {
      mountedRef.current = false;
    };
  }, [accountEpoch]);
  const isStale = (epoch: number) => !mountedRef.current || epochRef.current !== epoch;

  const refreshAssets = () => {
    void qc.invalidateQueries({ queryKey: ["assets"] });
    void qc.invalidateQueries({ queryKey: ["groups"] });
    void qc.invalidateQueries({ queryKey: ["asset-search"] });
  };

  const runDiscover = async () => {
    const epoch = accountEpoch;
    setBusy("discover");
    setError(null);
    setResult(null);
    try {
      const response = await inventoryApi.discover(source, source === "ssh-config" ? path : undefined);
      if (isStale(epoch)) return;
      const normalized = { ...response, items: response.items ?? [], warnings: response.warnings ?? [] };
      setDiscovery(normalized);
      setSelected(new Set(normalized.items.map((item) => item.key)));
    } catch (cause) {
      if (isStale(epoch)) return;
      setDiscovery(null);
      setSelected(new Set());
      setError(describeError(cause));
    } finally {
      if (!isStale(epoch)) setBusy(null);
    }
  };

  const runSync = async () => {
    // The epoch is captured at entry: a confirmation that resolves after an account switch must
    // never reach inventoryApi.sync — empty keys would soft-delete the new account's assets, and
    // no post-hoc staleness check can undo that call.
    const epoch = accountEpoch;
    if (source !== "ssh-config" && selected.size === 0) {
      const confirmed = await ask(
        "本次没有勾选任何主机。继续会移除此来源在当前账号下导入的全部资产；手动添加的资产和其他来源的资产不受影响。",
        { title: "同步为空清单", kind: "warning" },
      );
      if (!confirmed || isStale(epoch)) return;
    }
    setBusy("sync");
    setError(null);
    try {
      const response = await inventoryApi.sync({
        source,
        ...(source === "ssh-config" ? { path } : {}),
        ...(groupId ? { groupId } : {}),
        ...(source === "ssh-config" ? {} : { keys: [...selected] }),
      });
      if (isStale(epoch)) return;
      setResult({ ...response, warnings: response.warnings ?? [] });
      refreshAssets();
    } catch (cause) {
      if (isStale(epoch)) return;
      setError(describeError(cause));
    } finally {
      if (!isStale(epoch)) setBusy(null);
    }
  };

  const setWatch = async (enabled: boolean) => {
    const epoch = accountEpoch;
    setBusy("watch");
    setError(null);
    try {
      const status = await inventoryApi.setSshWatch({
        enabled,
        ...(enabled && path.trim() ? { path: path.trim() } : {}),
        ...(enabled && groupId ? { groupId } : {}),
      });
      if (isStale(epoch)) return;
      qc.setQueryData(["inventory-ssh-watch"], status);
      if (status.lastResult) setResult({ ...status.lastResult, warnings: status.lastResult.warnings ?? [] });
      refreshAssets();
    } catch (cause) {
      if (isStale(epoch)) return;
      setError(describeError(cause));
      void watchQuery.refetch();
    } finally {
      if (!isStale(epoch)) setBusy(null);
    }
  };

  const watching = watchQuery.data?.watching === true;
  const watchedPath = watchQuery.data?.path;
  const watchedGroupId = watchQuery.data?.groupId;
  useEffect(() => {
    if (!watching) return;
    setPath(watchedPath ?? "");
    setGroupId(watchedGroupId ?? "");
  }, [watching, watchedPath, watchedGroupId]);
  const items = discovery?.items ?? [];
  const selectedCount = source === "ssh-config" ? items.length : selected.size;
  const dockerSessions: InventoryTargetSession[] = sessions
    .filter((session) => session.kind === "docker")
    .map((session) => ({ id: session.id, name: session.name, assetId: session.assetId }));

  return (
    <div className="nx-overlay" onClick={busy ? undefined : onClose}>
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[680px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          event.stopPropagation();
          if (!layer.isTopmost()) return;
          if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event) && !busy) {
            event.preventDefault();
            onClose();
            return;
          }
          trapOverlayTab(event, modalRef.current);
        }}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">动态清单源</span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          <div className="nx-segment mb-3" aria-label="清单来源">
            {SOURCE_ORDER.map((item) => (
              <button
                key={item}
                type="button"
                className={`nx-segment-item ${source === item ? "is-active" : ""}`}
                disabled={busy !== null}
                onClick={() => {
                  setSource(item);
                  setDiscovery(null);
                  setSelected(new Set());
                  setResult(null);
                  setError(null);
                  setExecKey(null);
                }}
              >
                {SOURCE_LABEL[item]}
              </button>
            ))}
          </div>

          <div className="nx-form-row">
            <label className="nx-label" htmlFor="inventory-group">同步到 managed group</label>
            <select id="inventory-group" className="nx-select" value={groupId} disabled={busy !== null || (source === "ssh-config" && watching)} onChange={(event) => setGroupId(event.target.value)}>
              <option value="">默认（{DEFAULT_GROUP[source]}）</option>
              {(groups.data ?? []).map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}
            </select>
          </div>

          {source === "ssh-config" && (
            <div className="nx-form-row">
              <label className="nx-label" htmlFor={pathId}>SSH 配置文件</label>
              <input
                id={pathId}
                ref={pathRef}
                className="nx-input font-mono text-[12px]"
                value={path}
                disabled={busy !== null || watching}
                onChange={(event) => setPath(event.target.value)}
                placeholder="~/.ssh/config"
                autoComplete="off"
              />
              <div className="nx-hint mt-1.5">
                复用 OpenSSH Include 解析；watch 每 2 秒重读。解析失败保留最后快照，源文件删除按空清单软删除 managed 资产。
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={busy !== null} onClick={() => void setWatch(!watching)}>
                  {busy === "watch" ? <IconLoader size={12} className="animate-spin" /> : null}
                  {watching ? "停止 watch" : "启用 watch 并同步"}
                </button>
                {watching && watchQuery.data?.path && <span className="nx-badge">watching: {watchQuery.data.path}</span>}
              </div>
              {watchQuery.data?.lastError && <div className="mt-2 text-[12px] text-red-300" role="alert">后台同步失败：{watchQuery.data.lastError}</div>}
            </div>
          )}

          {source === "docker-context" && (
            <div className="nx-hint mb-3">只读执行 docker context ls。导入为 Docker context 清单记录，不修改 context，也不假定已有终端传输。</div>
          )}
          {source === "kubernetes" && (
            <div className="nx-hint mb-3">只读发现 contexts 与当前 kubectl context 的 pods。context 仅作清单与导航记录；pod 支持一次性 exec，不创建交互式终端。</div>
          )}

          {error && (
            <div className="mb-3 flex items-start gap-2 rounded-md border border-red-500/25 bg-red-500/10 px-3 py-2 text-[12px] text-red-300" role="alert">
              <IconXCircle size={14} className="mt-0.5 shrink-0" /><span>{error}</span>
            </div>
          )}

          {busy === "discover" && <div className="nx-empty" role="status"><IconLoader size={16} className="animate-spin" />正在只读发现…</div>}

          {discovery && busy !== "discover" && (
            <section aria-label={`${inventorySourceLabel(source)} 候选`}>
              <div className="mb-2 flex flex-wrap items-center gap-2 text-[12px] text-neutral-400">
                <span>{items.length} 个候选 · 本次同步 {selectedCount} 个</span>
                {source !== "ssh-config" && items.length > 0 && (
                  <>
                    <button type="button" className="nx-link" onClick={() => setSelected(new Set(items.map((item) => item.key)))}>全选</button>
                    <button type="button" className="nx-link" onClick={() => setSelected(new Set())}>全不选</button>
                  </>
                )}
              </div>
              {items.length === 0 ? (
                <div className="nx-empty"><IconServer size={18} /><span className="text-[12px] text-neutral-400">来源当前没有列出候选；同步空清单可移除该来源已管理的资产。</span></div>
              ) : (
                <div className="flex max-h-[320px] flex-col gap-1.5 overflow-y-auto">
                  {items.map((item) => {
                    const target = inventoryTargetText(item.options);
                    const execTarget = inventoryExecTarget(item.options);
                    const executable = execTarget !== null && targetExecutable(item.kind, execTarget);
                    return (
                      <div key={item.key} className="rounded-md border border-neutral-800/70 bg-neutral-950/40">
                        <div className="flex items-start gap-2 px-3 py-2 text-[12px]">
                          <label className="flex min-w-0 flex-1 items-start gap-2">
                            {source !== "ssh-config" ? (
                              <input
                                type="checkbox"
                                className="mt-0.5 shrink-0"
                                aria-label={`选择清单项 ${item.name}`}
                                checked={selected.has(item.key)}
                                onChange={(event) => {
                                  setSelected((current) => {
                                    const next = new Set(current);
                                    if (event.target.checked) next.add(item.key);
                                    else next.delete(item.key);
                                    return next;
                                  });
                                }}
                              />
                            ) : <span className="w-3 shrink-0" />}
                            <span className="min-w-0 flex-1">
                              <span className="break-all font-mono text-neutral-100">{item.name}</span>
                              <span className="ml-2 text-neutral-500">{item.kind}</span>
                              <div className="mt-0.5 break-all text-[11px] text-neutral-500">
                                {item.host || target || "仅清单记录"}
                              </div>
                            </span>
                          </label>
                          {executable && (
                            <button
                              type="button"
                              className="nx-link shrink-0"
                              aria-expanded={execKey === item.key}
                              onClick={() => setExecKey((current) => (current === item.key ? null : item.key))}
                            >
                              {execKey === item.key ? "收起" : "执行命令"}
                            </button>
                          )}
                        </div>
                        {executable && execKey === item.key && (
                          <div className="border-t border-neutral-800/70 px-3 py-2">
                            <InventoryTargetActions
                              kind={item.kind}
                              target={execTarget}
                              ownerKey={ownerKey}
                              sessions={dockerSessions}
                              requireSession={WEB && item.kind === "docker-context"}
                            />
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
              )}
              {source !== "ssh-config" && <div className="nx-hint mt-2">同步仅保留勾选的 source-managed 项；取消勾选会软删除该来源管理项，不影响手工资产或其他来源。</div>}
            </section>
          )}

          {result && (
            <div className="mt-3" role="status">
              <div className="flex flex-wrap gap-1.5">
                <span className="nx-badge nx-badge-green">新增 {result.created}</span>
                <span className="nx-badge">更新 {result.updated}</span>
                <span className="nx-badge nx-badge-amber">移除 {result.removed}</span>
                <span className="nx-badge">未变化 {result.unchanged}</span>
              </div>
            </div>
          )}
          {(result?.warnings.length || discovery?.warnings.length) ? (
            <div className="mt-3 rounded-md border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-[11.5px] text-amber-300">
              <div className="mb-1 flex items-center gap-1"><IconAlert size={12} />同步提示</div>
              {(result?.warnings.length ? result.warnings : discovery?.warnings ?? []).map((warning, index) => <div key={index}>{warning}</div>)}
            </div>
          ) : null}
        </div>
        <div className="nx-modal-footer shrink-0">
          <button type="button" className="nx-btn nx-btn-ghost" disabled={busy !== null} onClick={onClose}>关闭</button>
          <div className="nx-spacer" />
          <button type="button" className="nx-btn nx-btn-outline" disabled={busy !== null} onClick={() => void runDiscover()}>
            {busy === "discover" ? "发现中…" : discovery ? "重新发现" : "发现"}
          </button>
          <button type="button" className="nx-btn nx-btn-primary" disabled={busy !== null || !discovery} onClick={() => void runSync()}>
            {busy === "sync" ? "同步中…" : source === "ssh-config" ? "同步全部" : "同步选中项"}
          </button>
        </div>
      </div>
    </div>
  );
}
