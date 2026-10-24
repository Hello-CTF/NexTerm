import { useId, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useUi } from "../../app/store";
import { forwardRuleApi, type ForwardRule, type ForwardRuleInput } from "../../ipc/forwardRuleApi";
import { ask } from "../../ui/dialogs";
import { ContextMenu, type ContextMenuState } from "../../ui/ContextMenu";
import { useCompactWidth } from "../docker/useCompactWidth";
import { describeError } from "../../ui/errorText";
import {
  IconChevronDown,
  IconChevronRight,
  IconEdit,
  IconPlay,
  IconRefresh,
  IconSave,
  IconStop,
  IconTrash,
} from "../../ui/icons";

interface ForwardRuleDraft {
  listenPort: string;
  targetHost: string;
  targetPort: string;
}

export function ForwardRules({
  sessionId,
  draft,
  canSave,
  visible,
}: {
  sessionId?: string;
  draft: ForwardRuleDraft;
  canSave: boolean;
  visible: boolean;
}) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const sessions = useUi((s) => s.sessions);
  const [expanded, setExpanded] = useState(false);
  const [editing, setEditing] = useState<ForwardRule | null>(null);
  const [name, setName] = useState("");
  const [listenPort, setListenPort] = useState("");
  const [targetHost, setTargetHost] = useState("");
  const [targetPort, setTargetPort] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const panelId = useId();
  const [rootRef, compact] = useCompactWidth(760);
  const [menu, setMenu] = useState<ContextMenuState | null>(null);

  const rules = useQuery({
    queryKey: ["forwardRules"],
    queryFn: () => forwardRuleApi.list(),
    enabled: expanded && visible,
    refetchInterval: expanded && visible ? 5000 : false,
  });

  const refreshRules = () => void qc.invalidateQueries({ queryKey: ["forwardRules"] });
  const refreshAll = () => {
    refreshRules();
    void qc.invalidateQueries({ queryKey: ["forwards"] });
  };

  const parsePorts = (listen: string, target: string): { listenPort: number; targetPort: number } | null => {
    const listenPort = Number(listen.trim());
    const targetPort = Number(target.trim());
    if (!Number.isInteger(listenPort) || listenPort < 1 || listenPort > 65535) return null;
    if (!Number.isInteger(targetPort) || targetPort < 1 || targetPort > 65535) return null;
    return { listenPort, targetPort };
  };

  const save = async (input: ForwardRuleInput) => {
    setBusy("save");
    try {
      await forwardRuleApi.save(input);
      setEditing(null);
      refreshRules();
      pushToast("success", input.id ? "规则已保存，运行配置未自动更改" : "规则已保存，点击启动后才会监听");
    } catch (error) {
      pushToast("error", `保存规则失败：${describeError(error)}`);
    } finally {
      setBusy(null);
    }
  };

  const saveDraft = async () => {
    const assetId = sessions.find((session) => session.id === sessionId)?.assetId;
    const ports = parsePorts(draft.listenPort, draft.targetPort);
    if (!canSave || !assetId || !draft.targetHost.trim() || !ports) {
      pushToast("info", "请先选择 SSH 会话，并填写有效的固定端口和目标主机");
      return;
    }
    await save({
      assetId,
      name: "",
      listenPort: ports.listenPort,
      targetHost: draft.targetHost.trim(),
      targetPort: ports.targetPort,
    });
  };

  const beginEdit = (rule: ForwardRule) => {
    setEditing(rule);
    setName(rule.name);
    setListenPort(String(rule.listenPort));
    setTargetHost(rule.targetHost);
    setTargetPort(String(rule.targetPort));
  };

  const saveEdit = async () => {
    if (!editing) return;
    const ports = parsePorts(listenPort, targetPort);
    if (!targetHost.trim() || !ports) {
      pushToast("info", "目标主机不能为空，端口必须是 1–65535 之间的整数");
      return;
    }
    await save({
      id: editing.id,
      assetId: editing.assetId,
      name: name.trim(),
      listenPort: ports.listenPort,
      targetHost: targetHost.trim(),
      targetPort: ports.targetPort,
    });
  };

  const start = async (rule: ForwardRule) => {
    const candidates = sessions.filter(
      (session) => session.status === "connected" && session.kind === "ssh" && session.assetId === rule.assetId,
    );
    const current = candidates.find((session) => session.id === sessionId);
    const selected = current ?? (candidates.length === 1 ? candidates[0] : undefined);
    if (!selected) {
      pushToast(
        "info",
        candidates.length > 1
          ? "同一资产有多个连接，请从所属主机工作区启动规则"
          : "规则所属 SSH 资产尚未连接，请先手动连接",
      );
      return;
    }
    setBusy(rule.id);
    try {
      await forwardRuleApi.start(rule.id, selected.id);
      refreshAll();
      pushToast("success", `规则已启动 → 127.0.0.1:${rule.listenPort}`);
    } catch (error) {
      refreshAll();
      pushToast("error", `启动规则失败：${describeError(error)}`);
    } finally {
      setBusy(null);
    }
  };

  const stop = async (rule: ForwardRule) => {
    if (
      rule.status === "active" &&
      !(await ask(`停止规则「${rule.name}」？\n\n已经建立的连接会被断开，规则定义会保留。`, { kind: "warning" }))
    ) {
      return;
    }
    setBusy(rule.id);
    try {
      await forwardRuleApi.stop(rule.id);
      refreshAll();
      pushToast("success", "规则已停止，定义已保留");
    } catch (error) {
      refreshAll();
      pushToast("error", `停止规则失败：${describeError(error)}`);
    } finally {
      setBusy(null);
    }
  };

  const remove = async (rule: ForwardRule) => {
    const running = rule.status !== "stopped";
    if (
      !(await ask(
        running
          ? `删除规则「${rule.name}」？\n\n会先停止运行时并断开连接；停止失败时规则定义会保留。`
          : `确定删除已保存的规则「${rule.name}」？`,
        { kind: "warning" },
      ))
    ) {
      return;
    }
    setBusy(rule.id);
    try {
      await forwardRuleApi.delete(rule.id);
      if (editing?.id === rule.id) setEditing(null);
      refreshAll();
      pushToast("success", "规则已删除");
    } catch (error) {
      refreshAll();
      pushToast("error", `删除规则失败：${describeError(error)}`);
    } finally {
      setBusy(null);
    }
  };

  return (
    <section ref={rootRef} className="shrink-0 border-t border-neutral-800/60 bg-neutral-950/20">
      <ContextMenu state={menu} onClose={() => setMenu(null)} />
      <button
        className="nx-btn nx-btn-ghost nx-btn-sm m-2"
        aria-expanded={expanded}
        aria-controls={panelId}
        onClick={() => setExpanded((value) => !value)}
      >
        {expanded ? <IconChevronDown size={13} /> : <IconChevronRight size={13} />}
        已保存规则
      </button>
      {expanded && (
        <div id={panelId} className="max-h-72 overflow-auto border-t border-neutral-800/60 px-3 py-2">
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <span className="text-[11px] text-neutral-500">仅 local 固定端口；只监听 127.0.0.1，不自动启动。</span>
            <div className="nx-spacer" />
            <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={refreshRules} disabled={busy !== null}>
              <IconRefresh size={11} />
              刷新
            </button>
            <button className="nx-btn nx-btn-primary nx-btn-xs" onClick={() => void saveDraft()} disabled={!canSave || busy !== null}>
              <IconSave size={11} />
              保存当前为规则
            </button>
          </div>
          {rules.isPending ? (
            <div className="py-3 text-[11px] text-neutral-500">规则加载中…</div>
          ) : rules.isError && !rules.data ? (
            <div className="flex flex-wrap items-center gap-2 py-2 text-[11px] text-red-300" role="alert">
              <span>规则加载失败 · {describeError(rules.error)}</span>
              <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void rules.refetch()}>重试</button>
            </div>
          ) : (
            <div className="flex flex-col gap-1.5">
              {rules.isError && (
                <div className="text-[11px] text-amber-300" role="status">
                  规则刷新失败 · {describeError(rules.error)}；已保留上次结果。
                </div>
              )}
              {(rules.data ?? []).length === 0 ? (
                <div className="py-2 text-[11px] text-neutral-500">还没有保存的转发规则</div>
              ) : (
                (rules.data ?? []).map((rule) => (
                  <div key={rule.id} className="flex items-center gap-2 rounded border border-neutral-800/70 px-2 py-1.5 text-[11px]">
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium text-neutral-200" title={rule.name}>{rule.name}</div>
                      <div className="nx-mono truncate text-neutral-500" title={`${rule.listenHost}:${rule.listenPort} → ${rule.targetHost}:${rule.targetPort}`}>
                        {rule.listenHost}:{rule.listenPort} → {rule.targetHost}:{rule.targetPort}
                      </div>
                      <div className={rule.status === "failed" ? "text-red-300" : rule.status === "active" ? "text-green-300" : "text-neutral-500"}>
                        {rule.status === "active" ? "运行中" : rule.status === "failed" ? "失败" : rule.status === "reconnecting" ? "启动中" : "未运行"}
                        {rule.error && <span className="ml-1 break-words text-red-300">{rule.error}</span>}
                      </div>
                    </div>
                    {compact ? (
                      <div className="flex shrink-0 items-center gap-1">
                        {rule.status === "stopped" || rule.status === "failed" ? (
                          <button className="nx-btn nx-btn-ghost nx-btn-xs pointer-coarse:h-9" disabled={busy !== null} onClick={() => void start(rule)}>
                            <IconPlay size={11} />
                            启动
                          </button>
                        ) : (
                          <button className="nx-btn nx-btn-ghost nx-btn-xs pointer-coarse:h-9" disabled={busy !== null} onClick={() => void stop(rule)}>
                            <IconStop size={11} />
                            停止
                          </button>
                        )}
                        <button
                          className="nx-btn nx-btn-ghost nx-btn-xs pointer-coarse:h-9 pointer-coarse:w-9"
                          aria-label={`${rule.name} 更多操作`}
                          aria-haspopup="menu"
                          disabled={busy !== null}
                          onClick={(e) => {
                            const rect = e.currentTarget.getBoundingClientRect();
                            setMenu({
                              x: rect.right,
                              y: rect.bottom,
                              title: rule.name,
                              items: [
                                ...(rule.status === "failed"
                                  ? [{ kind: "item" as const, label: "清除状态", onSelect: () => void stop(rule) }]
                                  : []),
                                { kind: "item", label: "编辑", icon: <IconEdit size={13} />, disabled: rule.status !== "stopped", hint: rule.status !== "stopped" ? "请先停止并清除运行状态再编辑" : undefined, onSelect: () => beginEdit(rule) },
                                { kind: "item", label: "删除", icon: <IconTrash size={13} />, danger: true, onSelect: () => void remove(rule) },
                              ],
                            });
                          }}
                        >
                          <span aria-hidden="true">⋯</span>
                        </button>
                      </div>
                    ) : (
                      <div className="flex shrink-0 items-center gap-1">
                        {rule.status === "stopped" || rule.status === "failed" ? (
                          <button className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy !== null} onClick={() => void start(rule)}>
                            <IconPlay size={11} />
                            启动
                          </button>
                        ) : (
                          <button className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy !== null} onClick={() => void stop(rule)}>
                            <IconStop size={11} />
                            停止
                          </button>
                        )}
                        {rule.status === "failed" && (
                          <button className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy !== null} onClick={() => void stop(rule)}>
                            清除状态
                          </button>
                        )}
                        <button
                          className="nx-btn nx-btn-ghost nx-btn-xs"
                          disabled={busy !== null || rule.status !== "stopped"}
                          title={rule.status !== "stopped" ? "请先停止并清除运行状态再编辑" : "编辑规则"}
                          onClick={() => beginEdit(rule)}
                        >
                          <IconEdit size={11} />
                          编辑
                        </button>
                        <button className="nx-btn nx-btn-danger nx-btn-xs" disabled={busy !== null} onClick={() => void remove(rule)}>
                          <IconTrash size={11} />
                          删除
                        </button>
                      </div>
                    )}
                  </div>
                ))
              )}
            </div>
          )}
          {editing && (
            <div className="mt-2 flex flex-wrap items-center gap-2 border-t border-neutral-800/60 pt-2">
              <input className="nx-input nx-input-sm min-w-36 flex-1" value={name} onChange={(event) => setName(event.target.value)} aria-label="规则名称" placeholder="规则名称（可选）" />
              <input className="nx-input nx-input-sm w-24 font-mono" value={listenPort} onChange={(event) => setListenPort(event.target.value)} aria-label="规则监听端口" inputMode="numeric" />
              <span className="text-neutral-500">→</span>
              <input className="nx-input nx-input-sm min-w-40 flex-1 font-mono" value={targetHost} onChange={(event) => setTargetHost(event.target.value)} aria-label="规则目标主机" />
              <input className="nx-input nx-input-sm w-24 font-mono" value={targetPort} onChange={(event) => setTargetPort(event.target.value)} aria-label="规则目标端口" inputMode="numeric" />
              <button className="nx-btn nx-btn-primary nx-btn-xs" disabled={busy !== null} onClick={() => void saveEdit()}>保存编辑</button>
              <button className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy !== null} onClick={() => setEditing(null)}>取消</button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
