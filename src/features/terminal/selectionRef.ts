import { useUi } from "../../app/store";
import { t } from "../../i18n";
import { clipRefText, newRefId, refPreview, REF_TEXT_RUNES, singleLine } from "../ai/refText";

// The backend quotes at most this many characters of a text reference (aicontext.RefScreenRunes), keeping the tail.
export const SELECTION_REF_RUNES = REF_TEXT_RUNES;

export interface SelectionRefSource {
  // The store tab the selection came from; the chip jumps back to it.
  storeTabId: string;
  title: string;
  host: string;
}

function toast(text: string) {
  useUi.getState().pushToast("info", text);
}

// askAiAboutSelection queues the selected terminal text as a text reference for the AI panel and opens the panel.
// Like command output, the selection travels as a data reference, never as part of the user's message.
export function askAiAboutSelection(selection: string, source: SelectionRefSource): boolean {
  if (!selection.trim()) {
    toast(t("请先在终端中选中要询问的内容"));
    return false;
  }
  const { text, clipped } = clipRefText(selection, "tail");
  if (clipped) {
    toast(t("选中内容较长，AI 只会看到最后 {limit} 个字符", { limit: SELECTION_REF_RUNES }));
  }
  const id = newRefId("selection");
  const ui = useUi.getState();
  ui.queueAiRef({
    kind: "text",
    id,
    label: t("终端选区 · {preview}", { preview: refPreview(selection) }),
    detail: singleLine(source.host && source.host !== source.title ? `${source.title} · ${source.host}` : source.title),
    refId: id,
    text,
    selection: true,
    sourceTabId: source.storeTabId,
  });
  ui.setRightOpen(true);
  return true;
}

// revealSelectionSource brings the terminal tab a selection reference came from to the front.
export function revealSelectionSource(storeTabId: string): boolean {
  const ui = useUi.getState();
  const open = ui.workspaces.some((w) => w.panes.some((p) => p.tabs.some((tab) => tab.id === storeTabId)));
  if (!open) {
    toast(t("来源终端已关闭"));
    return false;
  }
  ui.setActiveTab(storeTabId);
  return true;
}
