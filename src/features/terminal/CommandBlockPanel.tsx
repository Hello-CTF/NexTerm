// 命令块面板（M1-T9 v1）—— 块分隔 / 输出折叠 / 复制输出。
//
// 为什么「折叠」在这里而不在终端画布上：xterm.js 没有删除缓冲行的 API，
// 在画布上折叠只能涂白留空。所以折叠语义落在本面板：每块默认收起，展开看输出预览。
import { useState } from "react";
import type { CommandBlock } from "./commandBlocks";

export interface CommandBlockPanelProps {
  blocks: CommandBlock[];
  /** 由 XtermView 的 TerminalHandle 提供，从缓冲里抽出该块文本。 */
  getText: (index: number) => string;
  onLocate: (index: number) => void;
  onClear: () => void;
  onToast: (kind: "info" | "success" | "error", text: string) => void;
}

/** 输出预览最多显示的行数（复制时是全文）。 */
const PREVIEW_LINES = 20;

function clockOf(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}:${String(
    d.getSeconds(),
  ).padStart(2, "0")}`;
}

function durationOf(b: CommandBlock): string {
  if (b.endedAt === null) return "运行中";
  const s = (b.endedAt - b.startedAt) / 1000;
  return s < 1 ? `${Math.round(s * 1000)}ms` : `${s.toFixed(1)}s`;
}

export function CommandBlockPanel({
  blocks,
  getText,
  onLocate,
  onClear,
  onToast,
}: CommandBlockPanelProps) {
  const [expanded, setExpanded] = useState<number | null>(null);
  /** 展开时缓存的输出预览，避免每次渲染都去扫缓冲。 */
  const [preview, setPreview] = useState<Record<number, string>>({});

  const copy = async (index: number) => {
    const text = getText(index);
    if (!text) {
      onToast("info", `块 #${index} 的输出已滚出缓冲，无法复制`);
      return;
    }
    try {
      await navigator.clipboard.writeText(text);
      onToast("success", `已复制块 #${index}（${text.length} 字符）`);
    } catch (e) {
      onToast("error", `复制失败：${String(e)}`);
    }
  };

  const toggle = (index: number) => {
    if (expanded === index) {
      setExpanded(null);
      return;
    }
    setExpanded(index);
    setPreview((p) => ({ ...p, [index]: getText(index) }));
  };

  return (
    <div className="flex h-full w-[300px] shrink-0 flex-col border-l border-neutral-800 bg-neutral-950/80">
      <div className="flex items-center gap-2 border-b border-neutral-800 px-2 py-1 text-xs">
        <span className="font-medium text-neutral-300">命令块</span>
        <span className="text-neutral-500">{blocks.length}</span>
        <div className="flex-1" />
        <button
          className="rounded px-1.5 py-0.5 text-neutral-400 hover:bg-neutral-800"
          title="清空块记录（不影响终端内容）"
          onClick={onClear}
        >
          清空
        </button>
      </div>

      {blocks.length === 0 ? (
        <div className="p-3 text-xs leading-relaxed text-neutral-600">
          还没有命令块。
          <br />
          在终端里执行命令后，每个命令会成为一个可折叠的块。
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          {blocks.map((b) => {
            const open = expanded === b.index;
            const body = preview[b.index] ?? "";
            const lines = body ? body.split("\n") : [];
            const shown = lines.slice(0, PREVIEW_LINES).join("\n");
            return (
              <div key={b.index} className="border-b border-neutral-900">
                <div className="flex items-start gap-1 px-2 py-1.5">
                  <button
                    className="mt-0.5 w-4 shrink-0 text-left text-[10px] text-neutral-500 hover:text-neutral-300"
                    onClick={() => toggle(b.index)}
                    title={open ? "收起" : "展开输出预览"}
                  >
                    {open ? "▾" : "▸"}
                  </button>
                  <span className="mt-0.5 w-6 shrink-0 text-right text-[10px] tabular-nums text-blue-400/70">
                    #{b.index}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div
                      className="truncate font-mono text-[11px] text-neutral-200"
                      title={b.command}
                    >
                      {b.command}
                      {!b.reliable && <span className="ml-1 text-amber-400/80">?</span>}
                    </div>
                    <div className="mt-0.5 text-[10px] text-neutral-500">
                      {clockOf(b.startedAt)} · {durationOf(b)}
                    </div>
                  </div>
                  <button
                    className="shrink-0 rounded px-1 text-[10px] text-neutral-500 hover:bg-neutral-800 hover:text-neutral-300"
                    title="定位到此块"
                    onClick={() => onLocate(b.index)}
                  >
                    定位
                  </button>
                  <button
                    className="shrink-0 rounded px-1 text-[10px] text-neutral-500 hover:bg-neutral-800 hover:text-neutral-300"
                    title="复制该块（命令 + 输出）"
                    onClick={() => void copy(b.index)}
                  >
                    复制
                  </button>
                </div>
                {open && (
                  <pre className="max-h-64 overflow-auto border-t border-neutral-900 bg-[#0e1015] px-2 py-1 font-mono text-[10px] leading-snug whitespace-pre-wrap text-neutral-400">
                    {shown || "（无输出）"}
                    {lines.length > PREVIEW_LINES
                      ? `\n… 还有 ${lines.length - PREVIEW_LINES} 行（复制可取全文）`
                      : ""}
                  </pre>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
