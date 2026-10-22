import { useState } from "react";
import {
  IconAlert,
  IconCheck,
  IconChevronRight,
  IconLoader,
  IconShield,
  IconXCircle,
} from "../../ui/icons";
import {
  autoWriteAllowed,
  formatEvidence,
  handleAtHostCwd,
  presenceView,
  runStateView,
  type AgentStateTone,
} from "./agentState";
import type { AgentApprovalDecision, AgentToolCall } from "./agentTypes";

const TONE_BADGE: Record<AgentStateTone, string> = {
  ok: "nx-badge nx-badge-green",
  warn: "nx-badge nx-badge-amber",
  error: "nx-badge nx-badge-red",
  muted: "nx-count",
  accent: "nx-badge nx-badge-blue",
};

const APPROVAL_LABEL: Record<AgentApprovalDecision, string> = {
  approve_once: "已批准一次",
  approve_session: "已本会话批准",
  approve_persistent: "已永久批准",
  deny: "已拒绝",
};

export function AgentToolCard({
  call,
  onApproval,
  approvalBusy = false,
}: {
  call: AgentToolCall;
  onApproval?: (requestId: string, decision: AgentApprovalDecision) => void;
  approvalBusy?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const running = call.state === "running";
  const presence = presenceView(call.presence, null);
  const run = call.run ? runStateView(call.run.state, call.run.evidence) : null;
  const expandable = call.output.length > call.summary.length;
  const body = open ? call.output : call.summary;
  const approval = call.approval;

  return (
    <div
      className="rounded-lg border border-neutral-800 bg-neutral-900/70 px-2.5 py-2 text-[11.5px]"
      aria-busy={running}
      data-call-id={call.callId}
    >
      <div className="flex items-center gap-1.5">
        <span className="nx-badge nx-badge-purple font-mono">{call.tool}</span>
        <span
          className="min-w-0 flex-1 truncate font-mono text-neutral-400"
          title={handleAtHostCwd(call.handle)}
        >
          {handleAtHostCwd(call.handle)}
        </span>
        <span className="sr-only">
          {running
            ? "工具执行中"
            : call.state === "unknown"
              ? "工具状态未知"
              : call.state === "ok"
                ? "工具执行成功"
                : "工具执行失败"}
        </span>
        {running ? (
          <IconLoader size={11} className="animate-spin text-amber-300" />
        ) : call.state === "ok" ? (
          <IconCheck size={12} className="text-green-300" />
        ) : call.state === "error" ? (
          <IconXCircle size={12} className="text-red-300" />
        ) : (
          <IconAlert size={12} className="text-neutral-400" />
        )}
        {!running && call.exitCode !== null && (
          <span className="font-mono text-[10px] text-neutral-500">{call.exitCode}</span>
        )}
      </div>

      {(run || call.presence !== "attached" || call.production || !call.isMine || !call.persistent) && (
        <div className="mt-1 flex flex-wrap items-center gap-1 text-[10.5px]">
          {run && (
            <span
              className={TONE_BADGE[run.tone]}
              title={run.evidence ? formatEvidence(run.evidence) : "无证据"}
            >
              {run.label}
            </span>
          )}
          {call.presence !== "attached" && (
            <span className={TONE_BADGE[presence.tone]} title={presence.evidence ? formatEvidence(presence.evidence) : "无证据"}>
              {presence.label}
            </span>
          )}
          {call.production && <span className="nx-badge nx-badge-red">生产环境</span>}
          {!call.isMine && (
            <span className="nx-badge nx-badge-amber" title="仅所有者可审批">
              所有者：{call.ownerName}
            </span>
          )}
          {call.persistent === false && <span className="nx-count">非持久目标</span>}
          {!autoWriteAllowed(call.presence) && (
            <span className="text-[var(--nx-fg-warning)]">自动写入已禁止</span>
          )}
        </div>
      )}

      {run?.evidence && (
        <div className="mt-1 text-[10.5px] text-neutral-500">
          证据：{formatEvidence(run.evidence)}
        </div>
      )}

      {approval &&
        (approval.decision === null ? (
          <div className="mt-1.5 flex flex-wrap items-center gap-1 border-t border-neutral-800 pt-1.5">
            <IconShield size={11} className="shrink-0 text-amber-300" />
            <span className="text-[10.5px] text-neutral-400">
              等待审批 · 请求 {approval.requestId} · 第 {approval.attempt} 次
            </span>
            <span className="nx-spacer" />
            <button
              className="nx-btn nx-btn-outline nx-btn-xs"
              disabled={approvalBusy || !call.isMine}
              title={call.isMine ? "仅批准这一次调用" : "仅所有者可审批"}
              onClick={() => onApproval?.(approval.requestId, "approve_once")}
            >
              批准一次
            </button>
            <button
              className="nx-btn nx-btn-outline nx-btn-xs"
              disabled={approvalBusy || !call.isMine}
              title={call.isMine ? "本会话内同类调用不再询问" : "仅所有者可审批"}
              onClick={() => onApproval?.(approval.requestId, "approve_session")}
            >
              本会话批准
            </button>
            <button
              className="nx-btn nx-btn-outline nx-btn-xs"
              disabled={approvalBusy || !call.isMine}
              title={call.isMine ? "拒绝此次调用" : "仅所有者可审批"}
              onClick={() => onApproval?.(approval.requestId, "deny")}
            >
              拒绝
            </button>
          </div>
        ) : (
          <div className="mt-1.5 border-t border-neutral-800 pt-1.5 text-[10.5px] text-neutral-500">
            <IconShield size={10} className="mr-1 inline shrink-0" />
            {APPROVAL_LABEL[approval.decision]} · 请求 {approval.requestId}
          </div>
        ))}

      {body && (
        <pre
          className={`mt-1.5 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] ${
            open ? "max-h-[26rem] text-neutral-300" : "max-h-32 text-neutral-500"
          }`}
        >
          {body}
        </pre>
      )}
      {expandable && (
        <button
          className="mt-1.5 flex items-center gap-1 text-[10.5px] text-neutral-500 hover:text-neutral-300"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          <IconChevronRight size={10} className={open ? "rotate-90" : undefined} />
          {open ? "收起" : `展开完整输出（${call.output.length} 字）`}
        </button>
      )}
    </div>
  );
}
