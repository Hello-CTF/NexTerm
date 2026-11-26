import { useUi } from "../../app/store";
import { t } from "../../i18n";

// The backend quotes at most this many characters of a text reference (aicontext.RefScreenRunes), keeping the tail.
export const SELECTION_REF_RUNES = 4000;
const SELECTION_PREVIEW_RUNES = 24;

export interface SelectionRefSource {
  // The store tab the selection came from; the chip jumps back to it.
  storeTabId: string;
  title: string;
  host: string;
}

// singleLine turns line breaks and control characters into spaces and collapses whitespace so a reference label stays on one line.
function singleLine(text: string): string {
  return text.replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, " ").replace(/\s+/g, " ").trim();
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
  const runes = Array.from(selection);
  const clipped = runes.length > SELECTION_REF_RUNES ? runes.slice(-SELECTION_REF_RUNES).join("") : selection;
  if (runes.length > SELECTION_REF_RUNES) {
    toast(t("选中内容较长，AI 只会看到最后 {limit} 个字符", { limit: SELECTION_REF_RUNES }));
  }
  const previewRunes = Array.from(singleLine(selection));
  const preview =
    previewRunes.slice(0, SELECTION_PREVIEW_RUNES).join("") + (previewRunes.length > SELECTION_PREVIEW_RUNES ? "…" : "");
  const id = `selection:${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
  const ui = useUi.getState();
  ui.queueAiRef({
    kind: "text",
    id,
    label: t("终端选区 · {preview}", { preview }),
    detail: singleLine(source.host && source.host !== source.title ? `${source.title} · ${source.host}` : source.title),
    refId: id,
    text: clipped,
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
