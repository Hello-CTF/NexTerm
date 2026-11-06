import { useState } from "react";
import {
  CONFIRM_ACTION_LABELS,
  CONFIRM_ALWAYS_BLOCK_LABEL,
  CONFIRM_HELP,
  CONFIRM_RESOLUTION,
  confirmTitle,
  toolLabel,
  TOOL_USER_DENIED,
  type ConfirmDecision,
} from "../../../src/features/ai/panel/aiConfirmLabels";
import { IconBot, IconCheck, IconChevronRight, IconInfo, IconRefresh, IconShield } from "../../../src/ui/icons";
import { pick, useToasts, type DemoProps } from "./common";

const copy = {
  zh: {
    title: "AI 助手", scope: "web-01", reset: "重置演示",
    prompt: "api-server 最近响应变慢，帮我看看容器资源。",
    reasoning: "先看容器资源占用，再决定要不要重启。",
    reasoningLabel: (n: number) => `思考过程（${n} 字）`,
    diagnosis: "api-server 内存用到 812MiB / 1GiB，接近上限。重启可以先缓解，需要你确认。",
    allowed: "重启完成，健康检查返回 ok，内存回落到 214MiB。",
    denied: "好的，没有重启。可以先看看 /api/reports 的慢查询再决定。",
    tool: toolLabel("exec_commands"), confirmTitle: confirmTitle(undefined), help: CONFIRM_HELP, helpLabel: "授权说明",
    actions: CONFIRM_ACTION_LABELS, alwaysBlock: CONFIRM_ALWAYS_BLOCK_LABEL, resolution: CONFIRM_RESOLUTION,
    sessionScope: "docker 命令", sessionNote: (scope: string) => `「本会话允许」后，本次对话内不再询问：${scope}`,
    allowTitle: "只允许这一次", denyTitle: "拒绝本次操作，AI 将收到拒绝通知（Esc）",
    blockTitle: "打开设置中的拦截规则，并预填当前操作",
    blockToast: "演示：App 中会打开设置里的拦截规则，并预填 docker restart api-server",
    input: "输入", output: (n: number) => `输出（${n} 字）`, result: "结果",
    expand: "展开输入和输出", collapse: "收起输入和输出",
    ok: "工具执行成功", rejected: TOOL_USER_DENIED,
  },
  en: {
    title: "AI assistant", scope: "web-01", reset: "Reset demo",
    prompt: "api-server has slowed down. Check its container resources.",
    reasoning: "Check container resource usage first, then decide whether a restart is needed.",
    reasoningLabel: (n: number) => `Reasoning (${n} chars)`,
    diagnosis: "api-server uses 812MiB of its 1GiB limit. A restart would relieve it and needs your approval.",
    allowed: "Restart finished. The health check returns ok and memory dropped to 214MiB.",
    denied: "OK, no restart. Looking at the slow /api/reports queries first is another option.",
    tool: "Run commands", confirmTitle: "Your approval is needed", helpLabel: "About approvals",
    help: "Allow once: this time only. Allow for session: no more prompts in this conversation. Always allow: saved to grants, revocable. Dangerous, unknowable and production actions always ask.",
    actions: { allow: "Allow once", allow_session: "Allow for session", allow_persistent: "Always allow", deny: "Deny" } as Record<ConfirmDecision, string>,
    alwaysBlock: "Always block",
    resolution: { allow: "Allowed once", allow_session: "Allowed for this session", allow_persistent: "Always allowed (revocable in grants)", deny: "Denied" } as Record<ConfirmDecision, string>,
    sessionScope: "docker commands", sessionNote: (scope: string) => `After "Allow for session", this conversation stops asking about: ${scope}`,
    allowTitle: "Allow this time only", denyTitle: "Deny this action; the AI is told it was denied (Esc)",
    blockTitle: "Open the interception rules in settings, prefilled with this action",
    blockToast: "Demo: the app opens the interception rules in settings, prefilled with docker restart api-server",
    input: "Input", output: (n: number) => `Output (${n} chars)`, result: "Result",
    expand: "Expand input and output", collapse: "Collapse input and output",
    ok: "Tool succeeded", rejected: { badge: "Denied", status: "You denied this", body: "You denied this action. Nothing was run." },
  },
};

interface Tool {
  display: string;
  output: string;
  exitCode?: number;
  denied?: boolean;
}

const stats: Tool = { display: "docker stats --no-stream", exitCode: 0, output: "NAME          CPU %   MEM USAGE / LIMIT\napi-server    4.8%    812MiB / 1GiB\npostgres      0.7%    126MiB / 2GiB\nredis         0.2%    18MiB / 512MiB" };
const restart: Tool = { display: "docker restart api-server", exitCode: 0, output: "api-server" };
const verify: Tool = { display: "curl -s localhost:8080/healthz && docker stats --no-stream api-server", exitCode: 0, output: "{\"status\":\"ok\"}\nNAME          CPU %   MEM USAGE / LIMIT\napi-server    1.2%    214MiB / 1GiB" };

type Copy = (typeof copy)["zh"] | (typeof copy)["en"];

// ToolBubble mirrors the app's collapsed tool card: tool badge, input, outcome; opening it shows input and output.
function ToolBubble({ tool, t }: { tool: Tool; t: Copy }) {
  const [open, setOpen] = useState(false);
  const body = tool.denied ? t.rejected.body : tool.output;
  return <div className="demo-tool">
    <div className="demo-tool-head">
      <button type="button" className="demo-tool-toggle" aria-expanded={open} title={open ? t.collapse : t.expand} onClick={() => setOpen((v) => !v)}>
        <IconChevronRight size={10} className={open ? "demo-rotate" : undefined} />
        <span className="nx-badge nx-badge-purple">{t.tool}</span>
        <span className="demo-tool-display">{tool.display}</span>
      </button>
      <span className="demo-sr">{tool.denied ? t.rejected.status : t.ok}</span>
      {tool.denied
        ? <span className="nx-badge"><IconShield size={10} />{t.rejected.badge}</span>
        : <IconCheck size={12} className="demo-ok" />}
      {!tool.denied && tool.exitCode !== undefined && <span className="demo-tool-exit">{tool.exitCode}</span>}
    </div>
    {!open && tool.denied && <p className="demo-tool-headline">{body}</p>}
    {open && <div className="demo-tool-body">
      <div><div className="demo-tool-label">{t.input}</div><pre>{tool.display}</pre></div>
      <div><div className="demo-tool-label">{tool.denied ? t.result : t.output(body.length)}</div><pre>{body}</pre></div>
    </div>}
  </div>;
}

export default function AiDemo({ language }: DemoProps) {
  const t = pick(language, copy);
  const { push: pushToast, view: toastView } = useToasts(language);
  const [decision, setDecision] = useState<ConfirmDecision | null>(null);
  const allowed = decision !== null && decision !== "deny";

  return <div className="nx-pane demo-ai">
    <div className="nx-toolbar">
      <IconBot size={14} className="demo-muted" />
      <span className="nx-toolbar-title">{t.title}</span>
      <span className="nx-hint">{t.scope}</span>
      <span className="nx-spacer" />
      {decision !== null && <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm demo-control" onClick={() => setDecision(null)}><IconRefresh size={12} />{t.reset}</button>}
    </div>
    <div className="demo-ai-stream">
      <div className="demo-ai-user">{t.prompt}</div>
      <details className="demo-ai-reasoning">
        <summary>{t.reasoningLabel(t.reasoning.length)}</summary>
        <div>{t.reasoning}</div>
      </details>
      <ToolBubble tool={stats} t={t} />
      <div className="demo-ai-assistant">{t.diagnosis}</div>
      {decision === null ? <div
        className="nx-alert demo-confirm"
        role="group"
        aria-label={`${t.confirmTitle}：${t.tool}`}
        onKeyDown={(event) => { if (event.key === "Escape") setDecision("deny"); }}
      >
        <div className="demo-confirm-head">
          <IconShield size={13} />
          <span>{t.confirmTitle}</span>
          <span className="nx-spacer" />
          <span className="nx-badge nx-badge-amber">{t.tool}</span>
          <span className="demo-help" title={t.help} aria-label={t.helpLabel} role="img"><IconInfo size={12} /></span>
        </div>
        <div className="demo-confirm-commands">$ {restart.display}</div>
        <div className="demo-confirm-actions">
          <button type="button" className="nx-btn nx-btn-primary nx-btn-xs" title={t.allowTitle} onClick={() => setDecision("allow")}>{t.actions.allow}</button>
          <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" onClick={() => setDecision("allow_session")}>{t.actions.allow_session}</button>
          <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" onClick={() => setDecision("allow_persistent")}>{t.actions.allow_persistent}</button>
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" title={t.denyTitle} onClick={() => setDecision("deny")}>{t.actions.deny}</button>
          <span className="nx-spacer" />
          <button type="button" className="demo-always-block" title={t.blockTitle} onClick={() => pushToast("info", t.blockToast)}>{t.alwaysBlock}</button>
        </div>
        <div className="demo-confirm-note">{t.sessionNote(t.sessionScope)}</div>
      </div> : <>
        <div className="demo-ai-record"><IconShield size={10} /><span>{t.tool} · {t.resolution[decision]}</span></div>
        {allowed ? <>
          <ToolBubble tool={restart} t={t} />
          <ToolBubble tool={verify} t={t} />
          <div className="demo-ai-assistant">{t.allowed}</div>
        </> : <>
          <ToolBubble tool={{ ...restart, denied: true }} t={t} />
          <div className="demo-ai-assistant">{t.denied}</div>
        </>}
      </>}
    </div>
    {toastView}
  </div>;
}
