import { memo, useState, useMemo, useEffect } from "react";
import { type ChatItem, type ConfirmItem, type QuestionItem, type SubagentTimeline } from "../conversation";
import type { AgentToolCall, AgentApprovalDecision } from "../agentTypes";
import { IconEdit, IconImage, IconAlert, IconRefresh, IconShield, IconLoader, IconCheck, IconXCircle, IconChevronRight, IconBot, IconList, IconPlay } from "../../../ui/icons";
import { Markdown } from "../Markdown";
import { AgentToolCard } from "../AgentToolCard";
import { type EnvironmentConfirmationChallenge, evaluateEnvironmentConfirmation, isManagedDeviceEnvironment, MANAGED_DEVICE_ENVIRONMENT_HINT, targetKey, environmentDisplayLabel } from "../../production/environmentConfirmation";
import { simpleDiff, hasVisibleChange, diffLineText } from "../diff";
import { toolLabel, extractUrls, splitUserRefs } from "./aiSidebarShared";
import { resolutionLabel, TOOL_USER_DENIED } from "./aiConfirmLabels";
import { t } from "../../../i18n";


export const ChatBubble = memo(function ChatBubble({
  item,
  streaming,
  planApproved,
  onApprovePlan,
  onRetry,
  onEdit,
  agentToolCall,
  onAgentApproval,
  agentApprovalBusy,
}: {
  item: ChatItem;
  streaming: boolean;
  planApproved: boolean;
  onApprovePlan: () => void;
  onRetry: (item: ChatItem) => void;
  onEdit: (item: ChatItem) => void;
  agentToolCall: AgentToolCall | null;
  onAgentApproval: (requestId: string, decision: AgentApprovalDecision) => void;
  agentApprovalBusy: boolean;
}) {
  if (item.role === "user") {
    const { refs, body } = splitUserRefs(item.text);
    return (
      <div className="group/user relative ml-10 rounded-[10px] rounded-br-[3px] border border-blue-500/25 bg-blue-500/20 px-3 py-2 text-[12.3px] leading-relaxed text-[var(--nx-fg-on-tint)]">
        {item.messageId ? (
          <button
            type="button"
            className="absolute right-1.5 top-1.5 rounded p-0.5 text-[var(--nx-fg-soft)] opacity-0 transition-opacity hover:text-neutral-100 focus:opacity-100 group-hover/user:opacity-100 pointer-coarse:opacity-100"
            title={t("编辑并重新发送")}
            aria-label={t("编辑并重新发送")}
            onClick={() => onEdit(item)}
          >
            <IconEdit size={11} />
          </button>
        ) : null}
        {item.imageCount ? (
          <div className="mb-1 flex items-center gap-1 text-[11px] text-[var(--nx-fg-soft)]">
            <IconImage size={11} />
            {t("{imageCount} 张图片", { imageCount: item.imageCount })}
          </div>
        ) : null}
        {refs.length > 0 ? (
          <ul className="mb-1 flex flex-wrap gap-1" aria-label={t("引用")}>
            {refs.map((ref, index) => (
              <li
                key={`${index}:${ref}`}
                className="max-w-full truncate rounded border border-blue-400/30 bg-blue-500/15 px-1.5 py-px text-[11px] text-[var(--nx-fg-soft)]"
                title={ref}
              >
                @{ref}
              </li>
            ))}
          </ul>
        ) : null}
        <pre className="font-sans break-words whitespace-pre-wrap">{body}</pre>
        {item.steer ? (
          <div className="mt-1 text-[10.5px] text-[var(--nx-fg-soft)]">
            {item.steer === "pending"
              ? t("补充指令 · 等待送达…")
              : item.steer === "delivered"
                ? t("补充指令 · 已送达")
                : t("补充指令 · 未送达")}
          </div>
        ) : null}
      </div>
    );
  }
  if (item.role === "assistant") {
    return (
      <div className="mr-2 rounded-[10px] rounded-bl-[3px] border border-neutral-800 bg-neutral-800 px-3 py-2 text-[12.3px] leading-relaxed text-neutral-200">
        <Markdown text={item.text} streaming={streaming} />
      </div>
    );
  }
  if (item.role === "reasoning") {
    return (
      <details
        className="ml-1 border-l-2 border-neutral-700 pl-2.5 text-[11.5px] leading-relaxed text-neutral-500"
        open={streaming}
      >
        <summary className="cursor-pointer select-none text-[10.5px] text-[var(--nx-fg-tertiary)] hover:text-neutral-400">
          {t("思考过程（{length} 字）{value}", { length: item.text.length, value: streaming ? t(" · 进行中") : "" })}
        </summary>
        <div className="mt-1 whitespace-pre-wrap italic">{item.text}</div>
      </details>
    );
  }
  if (item.role === "confirm" || item.role === "question") {
    return <InteractionRecord item={item} />;
  }
  if (item.role === "compact") {
    return (
      <details className="rounded-lg border border-dashed border-neutral-800 px-2.5 py-1.5 text-[11px] text-neutral-500">
        <summary className="cursor-pointer select-none">
          {t("已压缩上下文{value}，之后的回答基于下方摘要", { value: item.messages > 0 ? t("（{messages} 条消息）", { messages: item.messages }) : "" })}
        </summary>
        <div className="mt-1.5 whitespace-pre-wrap text-[11.5px] leading-relaxed text-neutral-400">{item.text}</div>
      </details>
    );
  }
  if (item.role === "outcome") {
    if (item.outcome === "error") {
      return (
        <div
          role="alert"
          className="flex items-start gap-1.5 rounded-lg border border-red-500/30 bg-red-500/[0.08] px-2.5 py-2 text-[11.5px] leading-relaxed text-red-200"
        >
          <IconAlert size={12} className="mt-0.5 shrink-0 text-red-300" />
          {item.maxIterations ? (
            <span className="nx-badge nx-badge-amber shrink-0">{t("已达到最大步数")}</span>
          ) : null}
          <span className="whitespace-pre-wrap">{t("本次回答失败：{text}", { text: item.text })}</span>
          {item.retryable ? (
            <button
              className="nx-btn nx-btn-outline nx-btn-xs ml-auto shrink-0"
              title={t("使用同一条消息重新回答")}
              onClick={() => onRetry(item)}
            >
              <IconRefresh size={11} />
              {t("重试")}
            </button>
          ) : null}
        </div>
      );
    }
    return (
      <div
        role="status"
        className={`flex items-center justify-center gap-1.5 py-0.5 text-[10.5px] ${
          item.outcome === "canceled" ? "text-[var(--nx-fg-warning)]" : "text-[var(--nx-fg-tertiary)]"
        }`}
      >
        {item.text}
      </div>
    );
  }
  if (item.role === "diff") return <DiffBubble item={item} />;
  if (item.role === "plan") {
    return <PlanBubble item={item} approved={planApproved} onApprove={onApprovePlan} />;
  }
  if (item.role === "tool") {
    // A tool call with a verified roster handle renders the reviewed agent
    // tool card (roster-joined identity, run evidence and approval actions);
    // anything else keeps the plain bubble and claims no roster identity.
    if (agentToolCall) {
      return (
        <AgentToolCard
          call={agentToolCall}
          onApproval={onAgentApproval}
          approvalBusy={agentApprovalBusy}
        />
      );
    }
    return <ToolBubble item={item} />;
  }
  return null;
});

function InteractionRecord({ item }: { item: ConfirmItem | QuestionItem }) {
  const pending = item.resolution === undefined;
  const text =
    item.role === "confirm"
      ? pending
        ? t("等待确认：{value}", { value: toolLabel(item.tool) })
        : `${toolLabel(item.tool)} · ${resolutionLabel(item.resolution ?? "")}`
      : pending
        ? t("等待回答：{question}", { question: item.question })
        : t("提问 · {resolution}", { resolution: item.resolution });
  return (
    <div
      className={`flex items-center gap-1.5 text-[10.5px] ${
        pending ? "text-[var(--nx-fg-warning)]" : "text-[var(--nx-fg-tertiary)]"
      }`}
      title={text}
    >
      {item.role === "confirm" ? <IconShield size={10} className="shrink-0" /> : <IconAlert size={10} className="shrink-0" />}
      <span className="truncate">{text}</span>
    </div>
  );
}

const POLICY_DENIED_PREFIX = t("权限策略已拒绝: ");
const USER_DENIED_TEXT = t("用户拒绝了此操作");

// blockedOutcome recognizes the two refusals the backend reports as tool
// failures, so the card shows them as interceptions rather than errors.
function blockedOutcome(text: string): { by: "policy" | "user"; reason: string } | null {
  const trimmed = text.trim();
  if (trimmed.startsWith(POLICY_DENIED_PREFIX)) return { by: "policy", reason: trimmed.slice(POLICY_DENIED_PREFIX.length) };
  if (trimmed === USER_DENIED_TEXT) return { by: "user", reason: "" };
  return null;
}

function firstLine(text: string): string {
  return text.trim().split(/\r?\n/, 1)[0] ?? "";
}

// The bubble is one line until opened: the tool, its input, the outcome. Opening it shows the whole input and
// output. A failure or refusal keeps its first line visible while closed, so a collapsed card never hides an error.
function ToolBubble({ item }: { item: Extract<ChatItem, { role: "tool" }> }) {
  const [open, setOpen] = useState(false);
  const running = item.summary === undefined;
  const full = item.text ?? "";
  const preview = item.summary ?? "";
  const output = full || preview;
  const blocked = !running && !item.ok && !item.panic ? blockedOutcome(output) : null;
  const body = blocked ? (blocked.by === "policy" ? t("拦截原因：{reason}", { reason: blocked.reason }) : TOOL_USER_DENIED.body) : output;
  const headline = !running && !item.ok ? firstLine(body) : "";
  return (
    <div
      className="rounded-lg border border-neutral-800 bg-neutral-900/70 px-2.5 py-2 text-[11.5px]"
      aria-busy={running}
    >
      <div className="flex items-center gap-1.5">
        <button
          type="button"
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left"
          aria-expanded={open}
          title={open ? t("收起输入和输出") : t("展开输入和输出")}
          onClick={() => setOpen((v) => !v)}
        >
          <IconChevronRight size={10} className={`shrink-0 text-neutral-500 ${open ? "rotate-90" : ""}`} />
          <span className="nx-badge nx-badge-purple shrink-0" title={item.name}>{toolLabel(item.name)}</span>
          <span className="min-w-0 flex-1 truncate font-mono text-neutral-400">{item.display}</span>
        </button>
        <span className="sr-only">
          {running ? t("工具执行中") : item.panic ? t("工具执行出错") : item.ok ? t("工具执行成功") : blocked ? (blocked.by === "policy" ? t("已被权限策略拦截") : TOOL_USER_DENIED.status) : t("工具执行失败")}
        </span>
        {item.panic && !running ? <span className="nx-badge nx-badge-red">{t("出错")}</span> : null}
        {blocked ? (
          <span className={blocked.by === "policy" ? "nx-badge nx-badge-amber" : "nx-badge"}>
            <IconShield size={10} className="mr-0.5 inline" />
            {blocked.by === "policy" ? t("已拦截") : TOOL_USER_DENIED.badge}
          </span>
        ) : running ? (
          <IconLoader size={11} className="animate-spin text-amber-300" />
        ) : item.ok ? (
          <IconCheck size={12} className="text-green-300" />
        ) : (
          <IconXCircle size={12} className="text-red-300" />
        )}
        {!running && !blocked && item.exitCode !== null && item.exitCode !== undefined && (
          <span className="font-mono text-[10px] text-neutral-500">{item.exitCode}</span>
        )}
      </div>
      {!open && headline && (
        <p className="mt-1 truncate pl-4 text-[10.5px] text-neutral-500" title={headline}>
          {headline}
        </p>
      )}
      {item.subagent ? <SubagentTimelineView timeline={item.subagent} /> : null}
      {open && (item.display || body) && (
        <div className="mt-1.5 flex flex-col gap-1.5">
          {item.display && (
            <div>
              <div className="text-[10px] text-neutral-500">{t("输入")}</div>
              <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-all font-mono text-[10.5px] text-neutral-300">
                {item.display}
              </pre>
            </div>
          )}
          {body && (
            <div>
              <div className="text-[10px] text-neutral-500">{blocked ? t("结果") : t("输出（{length} 字）", { length: output.length })}</div>
              <pre className="max-h-[26rem] overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-300">
                {body}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function SubagentTimelineView({ timeline }: { timeline: SubagentTimeline }) {
  const [open, setOpen] = useState(timeline.status === "running");
  const running = timeline.status === "running";
  const label = running
    ? t("子任务 · 运行中")
    : timeline.status === "completed"
      ? t("子任务 · 已完成")
      : timeline.status === "canceled"
        ? t("子任务 · 已取消")
        : t("子任务 · 失败");
  return (
    <div className="mt-1.5 border-t border-neutral-800 pt-1.5">
      <button
        className="flex w-full items-center gap-1.5 text-[10.5px] text-neutral-400 hover:text-neutral-200"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {running ? (
          <IconLoader size={10} className="shrink-0 animate-spin text-amber-300" />
        ) : timeline.status === "completed" ? (
          <IconCheck size={11} className="shrink-0 text-green-300" />
        ) : (
          <IconXCircle size={11} className="shrink-0 text-red-300" />
        )}
        <IconBot size={11} className="shrink-0 text-neutral-500" />
        <span className="min-w-0 flex-1 truncate text-left">{label}</span>
        <span className="shrink-0 text-neutral-500">
          {t("{length} 字 · {length2} 个工具", { length: timeline.text.length, length2: timeline.tools.length })}
        </span>
        <IconChevronRight size={10} className={open ? "rotate-90" : undefined} />
      </button>
      {open ? (
        <div className="mt-1 space-y-1">
          {timeline.text ? (
            <pre className="max-h-24 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-400">
              {timeline.text}
            </pre>
          ) : null}
          {timeline.tools.map((tool) => (
            <div key={tool.callId} className="flex items-center gap-1.5 text-[10.5px]">
              {tool.status === "running" ? (
                <IconLoader size={9} className="shrink-0 animate-spin text-amber-300" />
              ) : tool.status === "ok" ? (
                <IconCheck size={10} className="shrink-0 text-green-300" />
              ) : (
                <IconXCircle size={10} className="shrink-0 text-red-300" />
              )}
              <span className="shrink-0 font-mono text-neutral-400">{tool.name || "tool"}</span>
              {tool.panic && tool.status !== "running" ? <span className="nx-badge nx-badge-red">{t("出错")}</span> : null}
              {tool.summary ? (
                <span className="min-w-0 flex-1 truncate text-neutral-500" title={tool.summary}>
                  {tool.summary}
                </span>
              ) : null}
            </div>
          ))}
          {!running ? (
            <div className="text-[10.5px] text-neutral-500">
              {timeline.status === "completed"
                ? (timeline.summary ?? t("已完成"))
                : (timeline.error ?? timeline.summary ?? t("已结束"))}
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function PlanBubble({
  item,
  approved,
  onApprove,
}: {
  item: Extract<ChatItem, { role: "plan" }>;
  approved: boolean;
  onApprove: () => void;
}) {
  return (
    <div className="rounded-lg border border-blue-500/30 bg-blue-500/[0.07] px-2.5 py-2 text-[11.5px]">
      <div className="mb-1.5 flex items-center gap-1.5">
        <IconList size={12} className="shrink-0 text-blue-300" />
        <span className="font-medium text-[var(--nx-fg-on-tint)]">{t("方案已提交")}</span>
        <span className="text-neutral-500">{approved ? t("已批准") : t("尚未执行任何操作")}</span>
      </div>
      <div className="max-h-72 overflow-y-auto">
        <Markdown text={item.text} className="text-neutral-300" />
      </div>
      {!approved && (
        <button className="nx-btn nx-btn-outline nx-btn-xs mt-2" onClick={onApprove}>
          <IconPlay size={11} />
          {t("批准并执行方案")}
        </button>
      )}
    </div>
  );
}

export function ConfirmBody({ card }: { card: ConfirmItem }) {
  const pv = card.preview;
  const urls = extractUrls([...(card.commands ?? []), card.rendered].join("\n"));
  const urlBlock =
    urls.length > 0 ? (
      <div className="mb-1.5 rounded border border-neutral-800 bg-neutral-950/70 px-2 py-1.5">
        <div className="mb-0.5 text-[10.5px] text-neutral-500">{t("包含 URL")}</div>
        {urls.map((url) => (
          <div key={url} className="truncate font-mono text-[10.5px] text-blue-300" title={url}>
            {url}
          </div>
        ))}
      </div>
    ) : null;
  if (card.commands && card.commands.length > 0) {
    return (
      <>
        <div className="mb-1.5 max-h-52 overflow-auto whitespace-pre-wrap rounded border border-neutral-800 bg-neutral-950/70 px-2 py-1.5 font-mono text-[10.5px] leading-relaxed text-neutral-200">
          {card.commands.map((command, index) => (
            <div key={index}>$ {command}</div>
          ))}
        </div>
        {urlBlock}
        {card.reason ? (
          <div className="mb-1.5 text-[10.5px] leading-relaxed text-[var(--nx-fg-warning)]">
            {card.reason}
          </div>
        ) : null}
      </>
    );
  }
  if (!pv) {
    return (
      <>
        <pre className="mb-1.5 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[11px] text-neutral-200">
          {card.rendered}
        </pre>
        {urlBlock}
        {card.reason ? (
          <div className="mb-1.5 text-[10.5px] leading-relaxed text-[var(--nx-fg-warning)]">
            {card.reason}
          </div>
        ) : null}
      </>
    );
  }
  return (
    <>
      <div className="mb-1 flex items-baseline gap-1.5 font-mono text-[11px]">
        <span className="shrink-0 text-neutral-500">
          {pv.kind === "create" ? t("将新建") : t("将修改")}
        </span>
        <span className="min-w-0 flex-1 truncate text-neutral-200" title={pv.path}>
          {pv.path}
        </span>
      </div>
      <div className="mb-1.5 max-h-52 overflow-auto whitespace-pre-wrap rounded border border-neutral-800 bg-neutral-950/70 px-2 py-1.5 font-mono text-[10.5px] leading-relaxed">
        <DiffLines before={pv.before} after={pv.after} />
      </div>
      {urlBlock}
      {card.reason ? (
        <div className="mb-1.5 text-[10.5px] leading-relaxed text-[var(--nx-fg-warning)]">
          {card.reason}
        </div>
      ) : null}
      <details className="mb-2">
        <summary className="cursor-pointer text-[10.5px] text-[var(--nx-fg-tertiary)] hover:text-neutral-400">
          {t("查看原始参数")}
        </summary>
        <pre className="mt-1 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-500">
          {card.rendered}
        </pre>
      </details>
    </>
  );
}

// R-01 typed environment-name confirmation for a production-classified AI
// target. Each required name gets one input; the user types the actual
// environment names themselves — the submitted set is their own input,
// evaluated by the shared Go-semantics view-model, never echoed from the
// challenge. The backend re-validates the set at resume.
export function ProductionEnvironmentChallenge({
  names,
  onStateChange,
}: {
  names: string[];
  onStateChange: (canConfirm: boolean, submittedNames: string[]) => void;
}) {
  const [typedByTarget, setTypedByTarget] = useState<Record<string, string>>({});
  const challenge: EnvironmentConfirmationChallenge = useMemo(
    () => ({
      targetSetHash: "ai-production-challenge",
      requiredEnvironmentNames: names,
      productionTargets: names.map((name, index) => ({
        sessionId: `required-${index}`,
        sessionName: name,
        environment: name,
      })),
    }),
    [names],
  );
  const state = evaluateEnvironmentConfirmation(challenge, typedByTarget, challenge.targetSetHash);
  const canConfirm = state.canConfirm;
  const submittedNames = state.submittedNames;
  const submittedKey = submittedNames.join("\u0000");
  useEffect(() => {
    onStateChange(canConfirm, submittedNames);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canConfirm, submittedKey, onStateChange]);
  return (
    <div className="mb-2 rounded border border-amber-900/50 bg-amber-950/10 px-2.5 py-2" role="note">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-amber-300">
        <IconAlert size={12} />
        {t("生产环境写入风险：AI 写入将需要逐次输入实际环境名确认，本会话不会记住授权。")}
      </div>
      {names.some(isManagedDeviceEnvironment) && (
        <p className="mb-1.5 text-[11px] leading-relaxed text-neutral-300">{MANAGED_DEVICE_ENVIRONMENT_HINT}</p>
      )}
      <ul className="flex flex-col gap-1.5" aria-label={t("生产环境名列表")}>
        {state.targets.map(({ target, typed, missing, matches }) => (
          <li key={targetKey(target)} className="flex items-center gap-2 text-[11px]">
            <span className="nx-badge nx-badge-amber shrink-0">{environmentDisplayLabel(target.environment)}</span>
            <input
              className="nx-input font-mono text-xs"
              aria-label={t("输入环境名 {environment}", { environment: target.environment })}
              autoComplete="off"
              value={typed}
              onChange={(event) =>
                setTypedByTarget((current) => ({ ...current, [targetKey(target)]: event.target.value }))
              }
            />
            {!missing && !matches && <span className="shrink-0 text-red-300">{t("环境名不匹配")}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}

function DiffBubble({ item }: { item: Extract<ChatItem, { role: "diff" }> }) {
  return (
    <div className="rounded-lg border border-neutral-700 bg-neutral-900/70 px-2.5 py-2 text-[11.5px]">
      <div className="mb-1.5 flex items-center gap-1.5">
        <IconEdit size={12} className="shrink-0 text-neutral-400" />
        <span className="shrink-0 font-medium text-neutral-200">{t("文件已修改")}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-neutral-400" title={item.path}>
          {item.path}
        </span>
      </div>
      <pre className="max-h-44 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] leading-relaxed">
        <DiffLines before={item.before} after={item.after} />
      </pre>
    </div>
  );
}

function DiffLines({ before, after }: { before: string; after: string }) {
  const lines = simpleDiff(before, after);
  if (!hasVisibleChange(lines)) {
    return (
      <div className="text-neutral-500">
        {before === after ? t("（内容无变化）") : t("（仅文件末尾换行不同）")}
      </div>
    );
  }
  return (
    <>
      {lines.map((l, i) => (
        <div
          key={i}
          className={
            l.kind === "add" ? "nx-diff-add" : l.kind === "del" ? "nx-diff-del" : "text-neutral-500"
          }
        >
          {diffLineText(l)}
        </div>
      ))}
    </>
  );
}