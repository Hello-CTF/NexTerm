import { useId, useState } from "react";
import { IconChevronDown, IconChevronRight } from "../../ui/icons";

// 账号同步逐条对比的共享行视图: 网页版 CompareConsole(浏览器本地计算) 与桌面
// DesktopSyncConsole(sync_compare IPC) 共用同一套徽章/汇总/列表, 保证两端展示一致。

export type CompareRowState =
  | "local-only"
  | "remote-only"
  | "same"
  | "local-newer"
  | "remote-newer"
  | "local-deleted"
  | "remote-deleted"
  | "remote-undecryptable";

// CompareRowView 是行渲染所需的最小形状; 网页版内部 Row(携带 local/remote 明细)与
// 桌面端 sync_compare 返回行都结构化兼容它。
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

// compareSummary 汇总行: rows 为 null 表示仍在读取; 全部一致时不显示差异段。
export function compareSummary(rows: CompareRowView[] | null): string {
  if (!rows) return "读取中…";
  if (rows.length === 0) return "本机与云端都还没有可同步的内容";
  const sameCount = rows.filter((r) => r.state === "same").length;
  return (
    `共 ${rows.length} 条 · 一致 ${sameCount} 条` +
    (sameCount < rows.length ? ` · 差异 ${rows.length - sameCount} 条` : "")
  );
}

// CompareDetails 是「对比详情」折叠块: 标题按钮 + 汇总 + 行列表(差异在前由数据侧保证)。
// rows 为 null 表示尚无数据(读取中); error 为 true 时隐藏读取占位(错误已由告警区呈现)。
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
