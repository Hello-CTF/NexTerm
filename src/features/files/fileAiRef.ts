import { openFileTab, useUi } from "../../app/store";
import { t } from "../../i18n";
import { clipRefText, newRefId, REF_TEXT_RUNES, singleLine } from "../ai/refText";
import type { RefFileSource } from "../ai/panel/aiSidebarShared";
import { requestFileReveal } from "./fileReveal";
import { baseName } from "./pathUtils";

function toast(text: string) {
  useUi.getState().pushToast("info", text);
}

function hostName(sessionId: string): string {
  return useUi.getState().sessions.find((session) => session.id === sessionId)?.name ?? "";
}

function fileDetail(sessionId: string, path: string): string {
  const host = hostName(sessionId);
  return singleLine(host ? `${path} · ${host}` : path);
}

export function lineRangeLabel(line: number, endLine: number): string {
  return line === endLine ? `${line}` : `${line}-${endLine}`;
}

export interface EditorSelectionRef {
  sessionId: string;
  path: string;
  text: string;
  // 1-based, inclusive.
  line: number;
  endLine: number;
}

// askAiAboutEditorSelection queues the selected lines of a file as a text reference, labelled with the file and
// line range. The excerpt travels as a data reference (with its path, so the AI is told which file it is from),
// never as part of the user's message.
export function askAiAboutEditorSelection(selection: EditorSelectionRef): boolean {
  if (!selection.text.trim()) {
    toast(t("请先在编辑器中选中要询问的内容"));
    return false;
  }
  const { text, clipped } = clipRefText(selection.text, "head");
  if (clipped) toast(t("选中内容较长，AI 只会看到前 {limit} 个字符", { limit: REF_TEXT_RUNES }));
  const id = newRefId("editor");
  const ui = useUi.getState();
  ui.queueAiRef({
    kind: "text",
    id,
    label: `${baseName(selection.path)}:${lineRangeLabel(selection.line, selection.endLine)}`,
    detail: fileDetail(selection.sessionId, selection.path),
    refId: id,
    path: selection.path,
    sessionId: selection.sessionId,
    text,
    selection: true,
    sourceFile: { sessionId: selection.sessionId, path: selection.path, line: selection.line, endLine: selection.endLine },
  });
  ui.setRightOpen(true);
  return true;
}

// fileRefId keys a whole-file reference by host and path, so the same file referenced twice stays one chip.
export function fileRefId(sessionId: string, path: string): string {
  return `path:${encodeURIComponent(sessionId)}:${path}`;
}

// askAiAboutFile queues a whole file as a file reference. Only the path travels; the backend reads the file from
// the host when the message is sent, so the file does not need to be open.
export function askAiAboutFile(sessionId: string, path: string, opts: { unsaved?: boolean } = {}): boolean {
  if (!sessionId || !path) return false;
  const id = fileRefId(sessionId, path);
  const ui = useUi.getState();
  ui.queueAiRef({
    kind: "file",
    id,
    label: baseName(path) || path,
    detail: fileDetail(sessionId, path),
    refId: id,
    path,
    sessionId,
    sourceFile: { sessionId, path },
  });
  if (opts.unsaved) toast(t("AI 读取的是主机上已保存的版本，编辑器中未保存的改动不会包含在内"));
  ui.setRightOpen(true);
  return true;
}

// revealFileSource opens the file a reference came from and moves to its line range.
export function revealFileSource(source: RefFileSource): boolean {
  if (!useUi.getState().sessions.some((session) => session.id === source.sessionId)) {
    toast(t("来源文件所在的会话已关闭"));
    return false;
  }
  if (source.line) {
    requestFileReveal(source.sessionId, source.path, { line: source.line, endLine: source.endLine ?? source.line });
  }
  openFileTab(source.sessionId, source.path);
  return true;
}
