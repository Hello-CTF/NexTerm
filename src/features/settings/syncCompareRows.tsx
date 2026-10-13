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
}

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
};

export function compareRowLabel(state: CompareRowState): { text: string; tone: string; hint: string } {
  switch (state) {
    case "local-only":
      return { text: "仅本机", tone: "", hint: "云端还没有这条。推送会新建。" };
    case "remote-only":
      return { text: "仅云端", tone: "", hint: "本机还没有这条，拉取并应用会新建。" };
    case "same":
      return { text: "已一致", tone: "nx-badge-green", hint: "两边内容相同。" };
    case "local-newer":
      return { text: "本机较新", tone: "nx-badge-amber", hint: "本机版本较新，推送会覆盖云端。" };
    case "remote-newer":
      return { text: "云端较新", tone: "nx-badge-amber", hint: "云端版本较新，拉取并应用会覆盖本机。" };
    case "local-deleted":
      return { text: "本机已删", tone: "nx-badge-amber", hint: "这条在本机已删除。" };
    case "remote-deleted":
      return { text: "云端已删", tone: "nx-badge-amber", hint: "这条在云端已被删除。" };
    case "remote-undecryptable":
      return { text: "无法解密", tone: "nx-badge-amber", hint: "云端对象与本机数据密钥不匹配或已损坏；本机同名对象可在下次同步覆盖修复。" };
  }
}

// compareSummary summary line: rows null means still reading; the diff segment is hidden when everything matches.
export function compareSummary(rows: CompareRowView[] | null): string {
  if (!rows) return "读取中…";
  if (rows.length === 0) return "本机与云端都还没有可同步的内容";
  const sameCount = rows.filter((r) => r.state === "same").length;
  return (
    `共 ${rows.length} 条 · 一致 ${sameCount} 条` +
    (sameCount < rows.length ? ` · 差异 ${rows.length - sameCount} 条` : "")
  );
}

// CompareDetails is the "compare details" collapsible block: title button + summary + row list (diffs first, guaranteed by the data side).
// rows null means no data yet (reading); error true hides the reading placeholder (the error is already presented in the alert area).
export function CompareDetails({ rows, error }: { rows: CompareRowView[] | null; error?: boolean }) {
  const panelId = useId();
  const [open, setOpen] = useState(false);
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
          {rows && (
            <div className="max-h-[320px] overflow-y-auto rounded border border-neutral-800/60">
              {rows.length === 0 && <div className="nx-hint px-2.5 py-3 text-[12px]">本机与云端都还没有可同步的内容。</div>}
              {rows.map((r) => {
                const label = compareRowLabel(r.state);
                return (
                  <div
                    key={r.id}
                    className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
                    title={label.hint}
                  >
                    <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={r.name}>
                      {r.name}
                    </span>
                    {r.kind && (
                      <span className="nx-hint shrink-0 text-[11px]">{COMPARE_KIND_LABELS[r.kind] ?? r.kind}</span>
                    )}
                    <span className={`nx-badge shrink-0 ${label.tone}`}>{label.text}</span>
                  </div>
                );
              })}
            </div>
          )}
          {!rows && !error && <div className="nx-hint py-3 text-[12px]">正在读取本机与云端数据…</div>}
        </div>
      )}
    </div>
  );
}
