import { commandBlockStatus, type CommandBlock } from "./commandBlocks";
import { t } from "../../i18n";

const STATUS_DOT: Record<ReturnType<typeof commandBlockStatus>, string> = {
  running: "bg-blue-400",
  success: "bg-green-400",
  failure: "bg-red-400",
  done: "bg-neutral-500",
};

export function commandBlockDuration(block: Pick<CommandBlock, "startedAt" | "endedAt">): string {
  if (block.endedAt === null) return t("运行中");
  const s = (block.endedAt - block.startedAt) / 1000;
  return s < 1 ? `${Math.round(s * 1000)}ms` : `${s.toFixed(1)}s`;
}

// StickyCommandHeader pins the command whose output fills the top of the terminal while its command line is scrolled
// out of view (as in Warp); clicking it scrolls back to that command.
export function StickyCommandHeader({ block, onLocate }: { block: CommandBlock; onLocate: (index: number) => void }) {
  const status = commandBlockStatus(block);
  return (
    <button
      type="button"
      data-testid="sticky-command-header"
      data-status={status}
      className="absolute top-1 left-1.5 z-10 flex max-w-[min(640px,calc(100%-24px))] items-center gap-1.5 rounded-md border border-neutral-700/70 bg-neutral-900/90 px-2 py-0.5 text-left font-mono text-[11px] text-neutral-200 shadow-md backdrop-blur-sm hover:border-neutral-500 hover:bg-neutral-800/95"
      title={t("滚动到命令 #{index}", { index: block.index })}
      onClick={() => onLocate(block.index)}
    >
      <span className={`size-1.5 shrink-0 rounded-full ${STATUS_DOT[status]}`} aria-hidden />
      <span className="shrink-0 text-blue-300">#{block.index}</span>
      <span className="min-w-0 truncate">{block.command}</span>
      <span className="shrink-0 text-[10px] text-neutral-500">
        {commandBlockDuration(block)}
        {block.exitCode !== null && (
          <span className={block.exitCode === 0 ? "text-green-300" : "text-red-300"}> · exit {block.exitCode}</span>
        )}
      </span>
    </button>
  );
}
