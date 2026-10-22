import { useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import type { CommandBlock } from "./commandBlocks";
import { IconChevronDown, IconChevronRight, IconCopy, IconList, IconLocate, IconTrash } from "../../ui/icons";
import { describeError } from "../../ui/errorText";
import { copyText } from "../../ui/clipboard";

export interface CommandBlockPanelProps {
  blocks: CommandBlock[];
  getText: (index: number) => string;
  onLocate: (index: number) => void;
  onClear: () => void;
  onToast: (kind: "info" | "success" | "error", text: string) => void;
}

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
  const [expanded, setExpanded] = useState<{ index: number; text: string } | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const virtualizer = useVirtualizer({
    count: blocks.length,
    getScrollElement: () => scrollRef.current,
    getItemKey: (index) => blocks[index]?.index ?? index,
    estimateSize: () => 54,
    overscan: 6,
    initialRect: { width: 252, height: 600 },
  });

  const copy = async (index: number) => {
    const text = getText(index);
    if (!text) {
      onToast("info", `块 #${index} 的输出已滚出缓冲，无法复制`);
      return;
    }
    try {
      await copyText(text);
      onToast("success", `已复制块 #${index}`);
    } catch (e) {
      onToast("error", `复制失败：${describeError(e)}`);
    }
  };

  const toggle = (index: number) => {
    if (expanded?.index === index) {
      setExpanded(null);
      return;
    }
    setExpanded({ index, text: getText(index) });
  };

  const clear = () => {
    setExpanded(null);
    onClear();
  };

  return (
    <div className="nx-command-blocks flex h-full w-[252px] shrink-0 flex-col border-l border-neutral-800/60 bg-neutral-950">
      <div className="flex h-[31px] shrink-0 items-center gap-1.5 border-b border-neutral-800/60 px-2.5">
        <IconList size={13} className="text-neutral-500" />
        <span className="text-[11.5px] font-semibold text-neutral-200">命令块</span>
        <span className="nx-count">{blocks.length}</span>
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="清空块记录（不影响终端内容）"
          onClick={clear}
        >
          <IconTrash size={12} />
        </button>
      </div>

      {blocks.length === 0 ? (
        <div className="nx-hint p-3">
          还没有命令块。
          <br />
          在终端里执行命令后，每条命令会成为一个可折叠的块：可复制输出、可定位回终端。
        </div>
      ) : (
        <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto">
          <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((item) => {
              const b = blocks[item.index];
              if (!b) return null;
              const open = expanded?.index === b.index;
              const body = open ? expanded.text : "";
              const lines = body ? body.split("\n") : [];
              const shown = lines.slice(0, PREVIEW_LINES).join("\n");
              const running = b.endedAt === null;
              return (
                <div
                  key={item.key}
                  ref={virtualizer.measureElement}
                  data-index={item.index}
                  data-command-block={b.index}
                  className={`absolute top-0 left-0 w-full border-b border-neutral-800/40 ${
                    running ? "bg-blue-500/[0.06]" : ""
                  }`}
                  style={{ transform: `translateY(${item.start}px)` }}
                >
                  <div className="flex items-start gap-1.5 px-2.5 py-1.5">
                    <button
                      className="mt-0.5 shrink-0 text-neutral-500 hover:text-neutral-200"
                      onClick={() => toggle(b.index)}
                      title={open ? "收起输出预览" : "展开输出预览"}
                    >
                      {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
                    </button>
                    <span className="mt-0.5 w-5 shrink-0 text-right font-mono text-[10.5px] text-blue-300">
                      #{b.index}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-mono text-[11.5px] text-neutral-200" title={b.command}>
                        {b.command}
                        {!b.reliable && (
                          <span className="ml-1 text-amber-300" title="命令含控制字符，识别出的文本可能不准确">
                            ?
                          </span>
                        )}
                      </div>
                      <div className="mt-0.5 font-mono text-[10px] text-neutral-500">
                        {clockOf(b.startedAt)} · {durationOf(b)}
                        {b.exitCode !== null && (
                          <span className={b.exitCode === 0 ? "text-green-300" : "text-red-300"}>
                            {" "}
                            · exit {b.exitCode}
                          </span>
                        )}
                      </div>
                    </div>
                    <span className="mt-0.5 flex shrink-0 items-center gap-0.5">
                      <button
                        className="nx-icon-btn nx-icon-btn-sm"
                        title="定位到此块"
                        onClick={() => onLocate(b.index)}
                      >
                        <IconLocate size={11} />
                      </button>
                      <button
                        className="nx-icon-btn nx-icon-btn-sm"
                        title="复制该块（命令 + 输出）"
                        onClick={() => void copy(b.index)}
                      >
                        <IconCopy size={11} />
                      </button>
                    </span>
                  </div>
                  {open && (
                    <pre className="max-h-64 overflow-auto border-t border-neutral-800/40 bg-term px-2.5 py-1.5 font-mono text-[10.5px] leading-snug whitespace-pre-wrap text-neutral-400">
                      {shown || "（无输出）"}
                      {lines.length > PREVIEW_LINES
                        ? `\n… 还有 ${lines.length - PREVIEW_LINES} 行（复制可获取完整内容）`
                        : ""}
                    </pre>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
