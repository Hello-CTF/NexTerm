import type { CommandBlock } from "./commandBlocks";
import { IconClose, IconSparkles } from "../../ui/icons";
import { t } from "../../i18n";

// failedCommandBlock is the block the failed command chip offers to explain: the latest block, once the shell
// reported a non-zero exit for it (OSC 133 D). A new command replaces the latest block and so hides the chip.
export function failedCommandBlock(blocks: readonly CommandBlock[], dismissedIndex: number | null): CommandBlock | null {
  const last = blocks[blocks.length - 1];
  if (!last || last.endedAt === null || last.exitCode === null || last.exitCode === 0) return null;
  return last.index === dismissedIndex ? null : last;
}

export function FailedCommandChip({
  block,
  onExplain,
  onDismiss,
}: {
  block: CommandBlock;
  onExplain: (block: CommandBlock) => void;
  onDismiss: (block: CommandBlock) => void;
}) {
  return (
    <div
      role="status"
      data-testid="failed-command-chip"
      className="absolute right-3 bottom-2 z-10 flex items-center gap-0.5 rounded-full border border-red-400/40 bg-neutral-900/95 py-0.5 pr-0.5 pl-2.5 text-[11px] text-neutral-200 shadow-lg"
    >
      <button
        type="button"
        className="flex items-center gap-1.5 hover:text-neutral-50"
        title={block.command}
        onClick={() => onExplain(block)}
      >
        <span className="text-red-300">{t("退出码 {code}", { code: block.exitCode ?? "" })}</span>
        <span className="text-neutral-500">·</span>
        <IconSparkles size={11} className="text-blue-300" />
        {t("让 AI 解释")}
      </button>
      <button
        type="button"
        className="nx-icon-btn nx-icon-btn-sm rounded-full"
        aria-label={t("关闭提示")}
        title={t("关闭提示")}
        onClick={() => onDismiss(block)}
      >
        <IconClose size={10} />
      </button>
    </div>
  );
}
