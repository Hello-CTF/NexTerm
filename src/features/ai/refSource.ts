import type { RefChip } from "./panel/aiSidebarShared";
import { revealSelectionSource } from "../terminal/selectionRef";
import { revealFileSource } from "../files/fileAiRef";
import { t } from "../../i18n";

// refSourceHint is the tooltip of a chip that can jump back to where it came from, or null when it cannot.
export function refSourceHint(chip: RefChip): string | null {
  if (chip.sourceTabId) return t("{detail}，点击跳到来源终端", { detail: chip.detail });
  if (chip.sourceFile) return t("{detail}，点击跳到来源文件", { detail: chip.detail });
  return null;
}

// revealRefSource brings back the terminal tab or file a reference chip came from.
export function revealRefSource(chip: RefChip): boolean {
  if (chip.sourceTabId) return revealSelectionSource(chip.sourceTabId);
  if (chip.sourceFile) return revealFileSource(chip.sourceFile);
  return false;
}
