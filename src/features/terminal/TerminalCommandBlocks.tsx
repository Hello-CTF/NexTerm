import { useQueryClient } from "@tanstack/react-query";
import { CommandBlockPanel } from "./CommandBlockPanel";
import { explainCommandBlock, rerunCommandBlock, saveCommandBlockAsSnippet } from "./commandBlockActions";
import type { CommandBlock } from "./commandBlocks";

export interface TerminalCommandBlocksProps {
  blocks: CommandBlock[];
  getText: (index: number) => string;
  onLocate: (index: number) => void;
  onClear: () => void;
  onToast: (kind: "info" | "success" | "error", text: string) => void;
  // Terminal to rerun commands in; null hides the rerun button.
  kernelTabId: string | null;
  // Terminal title, recorded as the origin of AI references.
  sourceLabel: string;
  embedded?: boolean;
}

// Wires the command block actions once so the pane and the session sidebar behave the same.
export function TerminalCommandBlocks({ kernelTabId, sourceLabel, ...panel }: TerminalCommandBlocksProps) {
  const queryClient = useQueryClient();
  return (
    <CommandBlockPanel
      {...panel}
      onRerun={kernelTabId ? (b) => void rerunCommandBlock(kernelTabId, b) : undefined}
      onSaveAsSnippet={(b) => void saveCommandBlockAsSnippet(b, queryClient)}
      onExplain={(b, text) => explainCommandBlock(b, text, sourceLabel)}
    />
  );
}
