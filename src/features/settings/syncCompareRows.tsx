import { useId, useState } from "react";
import { IconChevronDown, IconChevronRight } from "../../ui/icons";

// Shared row view for account sync per-row comparison: the web CompareConsole (computed locally in the browser) and the desktop
// DesktopSyncConsole (sync_compare IPC) share the same badges/summary/list, keeping both ends consistent.

export type CompareRowState =
  | "local-only"
  | "remote-only"
  | "same"
  | "local-newer"
  | "remote-newer"
  | "local-deleted"
  | "remote-deleted"
  | "remote-undecryptable";

// CompareRowView is the minimal shape needed to render a row; both the web-internal Row (carrying local/remote details) and the
// desktop sync_compare returned rows are structurally compatible with it.
export interface CompareRowView {
  id: string;
  kind: string;
  name: string;
  state: CompareRowState;
  hasLocalRepairCopy?: boolean;
}

export type UndecryptableAction = "retry" | "restore-local" | "clear-cloud";

export const COMPARE_KIND_LABELS: Record<string, string> = {
  group: "分组",
  asset: "资产",
  snippet: "片段",
  credential: "凭据",
  tombstone: "删除标记",
  transcript: "会话记录",
  known_host: "已知主机",
  ai_profile: "AI 模型档案",
  ai_permission: "AI 权限",
  ai_grant: "AI 授权",
  preference: "偏好设置",
  state_rule: "状态规则",
  runbook_template: "Runbook 模板",
  skill: "AI 技能",
  memory: "AI 记忆",
  cron: "定时任务",
};

export function compareRowLabel(state: CompareRowState): { text: string; tone: string; hint: string } {
  switch (state) {
    case "local-only":
      return { text: "仅本机", tone: "", hint: "服务端还没有这条。推送会新建。" };
    case "remote-only":
      return { text: "仅服务端", tone: "", hint: "本机还没有这条，拉取并应用会新建。" };
    case "same":
      return { text: "已一致", tone: "nx-badge-green", hint: "两边内容相同。" };
    case "local-newer":
      return { text: "本机较新", tone: "nx-badge-amber", hint: "本机版本较新，推送会覆盖服务端。" };
    case "remote-newer":
      return { text: "服务端较新", tone: "nx-badge-amber", hint: "服务端版本较新，拉取并应用会覆盖本机。" };
    case "local-deleted":
      return { text: "本机已删", tone: "nx-badge-amber", hint: "这条在本机已删除。" };
    case "remote-deleted":
      return { text: "服务端已删", tone: "nx-badge-amber", hint: "这条在服务端已被删除。" };
    case "remote-undecryptable":
      return { text: "无法解密", tone: "nx-badge-amber", hint: "服务端对象与当前数据密钥不匹配或已损坏；自动同步已跳过此对象，修复需要手动确认。" };
  }
}

const SUMMARY_KIND_ORDER = [
  "asset", "group", "snippet", "credential", "known_host", "ai_profile", "ai_permission",
  "ai_grant", "preference", "state_rule", "runbook_template", "skill", "memory", "cron", "transcript", "tombstone",
];

// Internal bookkeeping objects are not worth a count when they match; a differing one is described plainly.
function summaryKindLabel(kind: string): string {
  if (kind === "tombstone") return "已删除的项目";
  if (!kind) return "无法识别的对象";
  return COMPARE_KIND_LABELS[kind] ?? kind;
}

const OPAQUE_ID_RE = /^[0-9A-HJKMNP-TV-Z]{26}(?:\s|$)/;

// compareRowTitle picks a human-readable title; raw ids and internal keys are demoted to secondary text.
export function compareRowTitle(r: CompareRowView): { title: string; secondary?: string } {
  if (r.kind === "tombstone") return { title: "已删除的项目", secondary: r.name || r.id };
  if (r.kind === "ai_grant") return { title: "AI 授权", secondary: r.name };
  if (!r.name || r.name === r.id || OPAQUE_ID_RE.test(r.name)) {
    return { title: r.kind ? (COMPARE_KIND_LABELS[r.kind] ?? r.kind) : "无法识别的对象", secondary: r.name || r.id };
  }
  return { title: r.name };
}

// compareSummary summary line grouped by category; rows null means still reading. Matching rows are only counted, never listed.
export function compareSummary(rows: CompareRowView[] | null): string {
  if (!rows) return "读取中…";
  if (rows.length === 0) return "本机与服务端都还没有可同步的内容";
  const counts = new Map<string, number>();
  let diff = 0;
  for (const r of rows) {
    if (r.state !== "same") diff++;
    else if (r.kind === "tombstone" || !r.kind) continue;
    const key = r.state === "same" ? r.kind : r.kind || "";
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  const keys = [...counts.keys()].sort((x, y) => {
    const ix = SUMMARY_KIND_ORDER.indexOf(x);
    const iy = SUMMARY_KIND_ORDER.indexOf(y);
    return (ix < 0 ? 99 : ix) - (iy < 0 ? 99 : iy);
  });
  const groups = keys.map((k) => `${summaryKindLabel(k)} ${counts.get(k)}`).join(" · ");
  const verdict = diff === 0 ? "全部一致" : `差异 ${diff} 条`;
  return groups ? `${groups} — ${verdict}` : verdict;
}

// CompareDetails is the "compare details" collapsible block: title button + summary + row list (diffs first, guaranteed by the data side).
// rows null means no data yet (reading); error true hides the reading placeholder (the error is already presented in the alert area).
export function CompareDetails({ rows, error, onUndecryptableAction, actionsBusy }: {
  rows: CompareRowView[] | null;
  error?: boolean;
  onUndecryptableAction?: (id: string, action: UndecryptableAction) => void;
  actionsBusy?: boolean;
}) {
  const panelId = useId();
  const [open, setOpen] = useState(false);
  const diffRows = (rows ?? []).filter((r) => r.state !== "same");
  return (
    <div className="mb-1">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
          aria-expanded={open}
          aria-controls={panelId}
          onClick={() => setOpen((v) => !v)}
        >
          {open ? (
            <IconChevronDown size={12} className="shrink-0" />
          ) : (
            <IconChevronRight size={12} className="shrink-0" />
          )}
          对比详情
        </button>
        <span className="nx-hint text-[11px]">{compareSummary(rows)}</span>
      </div>
      {open && (
        <div id={panelId} className="mt-2">
          {rows && rows.length === 0 && <div className="nx-hint py-3 text-[12px]">本机与服务端都还没有可同步的内容。</div>}
          {rows && rows.length > 0 && diffRows.length === 0 && (
            <div className="nx-hint px-0.5 py-2 text-[12px]">本机与服务端全部一致，无需处理。</div>
          )}
          {rows && diffRows.length > 0 && (
            <div className="max-h-[320px] overflow-y-auto rounded border border-neutral-800/60">
              {diffRows.map((r) => {
                const label = compareRowLabel(r.state);
                const { title, secondary } = compareRowTitle(r);
                return (
                  <div
                    key={r.id}
                    className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
                    title={label.hint}
                  >
                    <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={secondary ? `${title} (${secondary})` : title}>
                      {title}
                      {secondary && <span className="nx-hint ml-2 text-[11px]">{secondary}</span>}
                    </span>
                    {r.kind && title !== (COMPARE_KIND_LABELS[r.kind] ?? r.kind) && (
                      <span className="nx-hint shrink-0 text-[11px]">{COMPARE_KIND_LABELS[r.kind] ?? r.kind}</span>
                    )}
                    <span className={`nx-badge shrink-0 ${label.tone}`}>{label.text}</span>
                    {r.state === "remote-undecryptable" && onUndecryptableAction && (
                      <span className="flex shrink-0 flex-wrap items-center gap-1.5">
                        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={actionsBusy}
                          onClick={() => onUndecryptableAction(r.id, "retry")}>重试读取</button>
                        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={actionsBusy}
                          onClick={() => onUndecryptableAction(r.id, r.hasLocalRepairCopy ? "restore-local" : "clear-cloud")}>
                          {r.hasLocalRepairCopy ? "用本机副本修复" : "清除服务端副本"}
                        </button>
                      </span>
                    )}
                  </div>
                );
              })}
            </div>
          )}
          {!rows && !error && <div className="nx-hint py-3 text-[12px]">正在读取本机与服务端数据…</div>}
        </div>
      )}
    </div>
  );
}
