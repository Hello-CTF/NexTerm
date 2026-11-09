import { useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { commandBlockStatus, type CommandBlock } from "./commandBlocks";
import { commandBlockDuration } from "./StickyCommandHeader";
import { IconChevronDown, IconChevronRight, IconCopy, IconList, IconLocate, IconPlay, IconSave, IconSparkles, IconTrash } from "../../ui/icons";
import { describeError } from "../../ui/errorText";
import { copyText } from "../../ui/clipboard";
import { t } from "../../i18n";

export interface CommandBlockPanelProps {
  blocks: CommandBlock[];
  getText: (index: number) => string;
  onLocate: (index: number) => void;
  onClear: () => void;
  // The three actions below are optional; a button is shown only when its handler is given.
  onRerun?: (block: CommandBlock) => void;
  onSaveAsSnippet?: (block: CommandBlock) => void;
  onExplain?: (block: CommandBlock, text: string) => void;
  onToast: (kind: "info" | "success" | "error", text: string) => void;
  // Fill a host container instead of acting as the fixed-width side panel.
  embedded?: boolean;
}

const PREVIEW_LINES = 20;

function clockOf(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}:${String(
    d.getSeconds(),
  ).padStart(2, "0")}`;
}

// The left edge of each row repeats the terminal's status mark color.
const STATUS_EDGE: Record<ReturnType<typeof commandBlockStatus>, string> = {
  running: "border-l-blue-400",
  success: "border-l-green-400/70",
  failure: "border-l-red-400",
  done: "border-l-transparent",
};

export function CommandBlockPanel({
  blocks,
  getText,
  onLocate,
  onClear,
  onRerun,
  onSaveAsSnippet,
  onExplain,
  onToast,
  embedded = false,
}: CommandBlockPanelProps) {
  // Rerun types into the live prompt, which is unsafe while a command still owns the terminal.
  const anyRunning = blocks.some((b) => b.endedAt === null);
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
      onToast("info", t("块 #{index} 的输出已滚出缓冲，无法复制", { index }));
      return;
    }
    try {
      await copyText(text);
      onToast("success", t("已复制块 #{index}", { index }));
    } catch (e) {
      onToast("error", t("复制失败：{value}", { value: describeError(e) }));
    }
  };

  const explain = (b: CommandBlock) => {
    const text = getText(b.index);
    if (!text) {
      onToast("info", t("块 #{index} 的输出已滚出缓冲，无法解释", { index: b.index }));
      return;
    }
    onExplain?.(b, text);
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
    <div data-layout={embedded ? "embedded" : "panel"} className={embedded ? "nx-command-blocks flex min-h-0 flex-1 flex-col bg-neutral-950" : "nx-command-blocks flex h-full w-[252px] shrink-0 flex-col border-l border-neutral-800/60 bg-neutral-950 @max-[560px]:absolute @max-[560px]:inset-x-0 @max-[560px]:bottom-0 @max-[560px]:z-10 @max-[560px]:h-[55%] @max-[560px]:w-auto @max-[560px]:border-t @max-[560px]:border-l-0 @max-[560px]:shadow-[0_-8px_24px_rgba(0,0,0,0.5)]"}>
      <div className="flex h-[31px] shrink-0 items-center gap-1.5 border-b border-neutral-800/60 px-2.5">
        <IconList size={13} className="text-neutral-500" />
        <span className="text-[11.5px] font-semibold text-neutral-200">{t("命令列表")}</span>
        <span className="nx-count">{blocks.length}</span>
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("清空块记录（不影响终端内容）")}
          onClick={clear}
        >
          <IconTrash size={12} />
        </button>
      </div>

      {blocks.length === 0 ? (
        <div className="nx-hint p-3">
          {t("暂无命令记录。")}
          <br />
          {t("在终端中执行命令后，可按命令复制输出或定位到终端中的位置。")}
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
                  data-status={commandBlockStatus(b)}
                  className={`absolute top-0 left-0 w-full border-b border-l-2 border-neutral-800/40 ${STATUS_EDGE[commandBlockStatus(b)]} ${
                    running ? "bg-blue-500/[0.06]" : ""
                  }`}
                  style={{ transform: `translateY(${item.start}px)` }}
                >
                  <div className="flex items-start gap-1.5 px-2.5 py-1.5">
                    <button
                      className="mt-0.5 shrink-0 text-neutral-500 hover:text-neutral-200"
                      onClick={() => toggle(b.index)}
                      title={open ? t("收起输出预览") : t("展开输出预览")}
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
                          <span className="ml-1 text-amber-300" title={t("命令包含控制字符，识别的文本可能不准确")}>
                            ?
                          </span>
                        )}
                      </div>
                      <div className="mt-0.5 font-mono text-[10px] text-neutral-500">
                        {clockOf(b.startedAt)} · {commandBlockDuration(b)}
                        {b.exitCode !== null && (
                          <span className={b.exitCode === 0 ? "text-green-300" : "text-red-300"}>
                            {" "}
                            · exit {b.exitCode}
                          </span>
                        )}
                      </div>
                    </div>
                    <span className="mt-0.5 flex shrink-0 items-center gap-0.5">
                      {onRerun && !running && b.command.trim() !== "" && (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm"
                          title={anyRunning ? t("终端里还有命令在运行，结束后才能重新运行") : t("重新运行")}
                          aria-label={t("重新运行")}
                          disabled={anyRunning}
                          onClick={() => onRerun(b)}
                        >
                          <IconPlay size={11} />
                        </button>
                      )}
                      {onSaveAsSnippet && (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm"
                          title={t("保存为片段")}
                          aria-label={t("保存为片段")}
                          onClick={() => onSaveAsSnippet(b)}
                        >
                          <IconSave size={11} />
                        </button>
                      )}
                      {onExplain && (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm"
                          title={t("让 AI 解释这段输出")}
                          aria-label={t("让 AI 解释这段输出")}
                          onClick={() => explain(b)}
                        >
                          <IconSparkles size={11} />
                        </button>
                      )}
                      <button
                        className="nx-icon-btn nx-icon-btn-sm"
                        title={t("定位到此命令")}
                        onClick={() => onLocate(b.index)}
                      >
                        <IconLocate size={11} />
                      </button>
                      <button
                        className="nx-icon-btn nx-icon-btn-sm"
                        title={t("复制该块（命令 + 输出）")}
                        onClick={() => void copy(b.index)}
                      >
                        <IconCopy size={11} />
                      </button>
                    </span>
                  </div>
                  {open && (
                    <pre className="max-h-64 overflow-auto border-t border-neutral-800/40 bg-term px-2.5 py-1.5 font-mono text-[10.5px] leading-snug whitespace-pre-wrap text-neutral-400">
                      {shown || t("（无输出）")}
                      {lines.length > PREVIEW_LINES
                        ? t("\n… 还有 {value} 行（复制可获取完整内容）", { value: lines.length - PREVIEW_LINES })
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
