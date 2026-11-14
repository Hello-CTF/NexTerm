import { useCallback, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { EditorView, keymap } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { Compartment, EditorState } from "@codemirror/state";
import { indentWithTab, redo, undo } from "@codemirror/commands";
import { openSearchPanel, search } from "@codemirror/search";
import { ask, askChoice } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import { openLogTab, useUi } from "../../app/store";
import { modHint } from "../../app/platform";
import { nxHighlight } from "../../ui/editorTheme";
import {
  IconClose,
  IconLocate,
  IconMinus,
  IconPlus,
  IconRedo,
  IconRefresh,
  IconSave,
  IconSearch,
  IconUndo,
} from "../../ui/icons";
import { fileVisual } from "./fileTypes";
import { revealOffset, subscribeFileReveal, takeFileReveal } from "./fileReveal";
import { bytesFromBase64, bytesToBase64, decodeContent, type EncChoice } from "./fileCodec";
import {
  beginFileEditorLoad,
  beginFileEditorSave,
  endFileEditorOperation,
  fileEditorCaseInsensitive,
  fileEditorLocatorKey,
  fileEditorRenamePending,
  isActiveFileEditor,
  setFileEditorDirty,
  shouldClearEditorDirty,
  shouldHandleEditorSave,
  unknownFileEditorRename,
  unknownFileEditorRenameByToken,
  unknownFileEditorRenameConflict,
} from "./editorGuards";

import {
  FILE_LANGUAGE_OPTIONS,
  detectFileLanguage,
  loadFileLanguageExtension,
  fileLanguageLabel,
  type FileLanguageChoice,
} from "./fileLanguages";
import { describeError } from "../../ui/errorText";
import { diffLineText, simpleDiff } from "../ai/diff";
import { subscribeFsChanged, useFsWatch } from "./fsWatch";
import { baseName, parentOf } from "./pathUtils";
import { Select } from "../../ui/Select";
import { t } from "../../i18n";

export interface FileEditorSaveChange {
  sessionId: string;
  path: string;
  previous: string;
  baseContentBase64: string;
  content: string;
}

export interface FileEditorProps {
  sessionId: string;
  path: string;
  onClose?: () => void;
  saveFile?: (change: FileEditorSaveChange) => Promise<boolean>;
}

export async function loadFileEditorSaveChange({
  sessionId,
  path,
  editorBase,
  content,
  encoding,
}: {
  sessionId: string;
  path: string;
  editorBase: string;
  content: string;
  encoding: EncChoice;
}): Promise<FileEditorSaveChange> {
  const current = await fsApi.read(sessionId, path);
  const currentText = decodeContent(bytesFromBase64(current.contentBase64), encoding).text;
  if (currentText !== editorBase) {
    throw new Error(t("文件在打开后已被外部修改；请重新读取并合并改动后再保存"));
  }
  return { sessionId, path, previous: currentText, baseContentBase64: current.contentBase64, content };
}

const ENC_LABEL: Record<EncChoice, string> = {
  auto: t("自动"),
  "utf-8": "UTF-8",
  gbk: "GBK",
};

const ZOOM_STEPS = [11, 12, 13, 14.5, 16, 18];

// A file version as the host stores it: what a conditional save expects to replace.
export interface FileVersion {
  size: number;
  sha256: string;
}

export async function fileVersionOf(bytes: Uint8Array): Promise<FileVersion> {
  const digest = await crypto.subtle.digest("SHA-256", bytes.slice().buffer);
  return {
    size: bytes.length,
    sha256: Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join(""),
  };
}

// The file on the host changed while the editor had it open: "changed" while it holds unsaved edits, "conflict"
// after a save was refused because of it, "deleted" once it is gone.
type DiskChange =
  | { kind: "changed" | "conflict"; text: string; version: FileVersion; encoding: string; size: number }
  | { kind: "deleted" };

function fsErrorIsMissing(error: unknown): boolean {
  return !!error && typeof error === "object" && (error as { code?: unknown }).code === "not_found";
}

function isVersionConflict(error: unknown): boolean {
  if (!error || typeof error !== "object") return false;
  const { code, detail } = error as { code?: unknown; detail?: unknown };
  return code === "conflict" && !!detail && typeof detail === "object" && (detail as { reason?: unknown }).reason === "changed";
}

export function FileEditor({ sessionId, path, onClose, saveFile }: FileEditorProps) {
  const qc = useQueryClient();
  const pushToast = useUi((state) => state.pushToast);
  const caseInsensitive = useUi((state) =>
    fileEditorCaseInsensitive(state.sessions.find((session) => session.id === sessionId)?.kind ?? ""),
  );
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [meta, setMeta] = useState<{ encoding: string; size: number } | null>(null);
  const [zoom, setZoom] = useState(2);
  const [eol, setEol] = useState<"LF" | "CRLF">("LF");
  const [enc, setEnc] = useState<EncChoice>("auto");
  const [reloadEnc, setReloadEnc] = useState<EncChoice>("auto");
  const [reloadKey, setReloadKey] = useState(0);
  const [languageSelection, setLanguageSelection] = useState<{
    sessionId: string;
    path: string;
    choice: FileLanguageChoice;
  }>({ sessionId, path, choice: "auto" });
  const languageChoice =
    languageSelection.sessionId === sessionId && languageSelection.path === path
      ? languageSelection.choice
      : "auto";
  const viewRef = useRef<EditorView | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);
  const saveRef = useRef<() => void>(() => undefined);
  const saveInFlightRef = useRef(false);
  const editVersionRef = useRef(0);
  const dirtyRef = useRef(false);
  const originalRef = useRef("");
  // versionRef resolves to the host version the editor content started from; null when unknown or the file is gone.
  // It is hashed in the background so loading and saving never wait for it.
  const versionRef = useRef<Promise<FileVersion | null>>(Promise.resolve(null));
  // applyingDiskRef keeps a reload from the host from marking the editor dirty.
  const applyingDiskRef = useRef(false);
  const [diskChange, setDiskChange] = useState<DiskChange | null>(null);
  const [showDiff, setShowDiff] = useState(false);
  const mountedRef = useRef(true);
  const locatorRef = useRef({ sessionId, path });
  const loadLocksRef = useRef(new Map<string, { token: number; users: number }>());
  const onCloseRef = useRef(onClose);
  const caseInsensitiveRef = useRef(caseInsensitive);
  const languageChoiceRef = useRef(languageChoice);
  const language = useRef(new Compartment()).current;
  const languageRequestRef = useRef(0);
  const languageAppliedRef = useRef(false);
  const revealRef = useRef<() => void>(() => undefined);
  // The editor opens as plain text; highlighting is applied when the language pack arrives. A load
  // failure leaves plain text in place, and a newer request or a replaced view discards stale results.
  const applyLanguage = useCallback(
    (view: EditorView, filePath: string, choice: FileLanguageChoice) => {
      const request = ++languageRequestRef.current;
      void loadFileLanguageExtension(filePath, choice).then(
        (extension) => {
          if (request !== languageRequestRef.current || viewRef.current !== view) return;
          const plain = Array.isArray(extension) && extension.length === 0;
          // A fresh editor already is plain text, so there is nothing to reconfigure.
          if (plain && !languageAppliedRef.current) return;
          languageAppliedRef.current = !plain;
          view.dispatch({ effects: language.reconfigure(extension) });
        },
        () => {
          if (request !== languageRequestRef.current || viewRef.current !== view) return;
          if (!languageAppliedRef.current) return;
          languageAppliedRef.current = false;
          view.dispatch({ effects: language.reconfigure([]) });
        },
      );
    },
    [language],
  );
  locatorRef.current = { sessionId, path };
  dirtyRef.current = dirty;
  onCloseRef.current = onClose;
  caseInsensitiveRef.current = caseInsensitive;
  languageChoiceRef.current = languageChoice;

  // A terminal path link asks for a line; it applies once the document is loaded, or now when it already is.
  useEffect(() => {
    const reveal = () => {
      const view = viewRef.current;
      if (!view) return;
      const position = takeFileReveal(locatorRef.current.sessionId, locatorRef.current.path);
      if (!position) return;
      const offset = revealOffset(view.state.doc, position);
      view.dispatch({ selection: { anchor: offset }, effects: EditorView.scrollIntoView(offset, { y: "center" }) });
      view.focus();
    };
    revealRef.current = reveal;
    return subscribeFileReveal(reveal);
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    setLanguageSelection((current) => {
      if (current.sessionId === sessionId && current.path === path && current.choice === "auto") {
        return current;
      }
      return { sessionId, path, choice: "auto" };
    });
  }, [sessionId, path]);

  useEffect(() => {
    setFileEditorDirty(sessionId, path, dirty, caseInsensitive);
    if (!dirty) return;
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [sessionId, path, dirty, caseInsensitive]);

  useEffect(() => {
    return () => setFileEditorDirty(sessionId, path, false, caseInsensitive);
  }, [sessionId, path, caseInsensitive]);

  useEffect(() => {
    let cancelled = false;
    const ci = caseInsensitiveRef.current;
    const initialLocator = locatorRef.current;
    const lockKey = fileEditorLocatorKey(
      initialLocator.sessionId,
      initialLocator.path,
      ci,
    );
    let lock = loadLocksRef.current.get(lockKey);
    if (!lock) {
      const token = beginFileEditorLoad(initialLocator.sessionId, initialLocator.path, ci);
      if (token === null) {
        pushToast("info", t("文件读取或重命名进行中，完成后请重新读取"));
        onCloseRef.current?.();
        return;
      }
      lock = { token, users: 0 };
      loadLocksRef.current.set(lockKey, lock);
    }
    lock.users += 1;
    const token = lock.token;
    void (async () => {
      try {
        let probedSize: number | null = null;
        try {
          const probeLocator = locatorRef.current;
          const probe = await fsApi.readRange(probeLocator.sessionId, probeLocator.path, 0, 1);
          probedSize = probe.size;
        } catch {
          probedSize = null;
        }
        if (cancelled) return;
        let largeEditConfirmed = false;
        if (probedSize !== null && probedSize > 5 * 1024 * 1024) {
          const choice = await askChoice(t("文件超过 5MB，整文件编辑可能响应缓慢。请选择打开方式。"), {
            title: t("大文件"),
            level: "info",
            choices: [
              {
                key: "view",
                label: t("只读分块查看"),
                hint: t("使用日志查看器按需读取，不加载整个文件"),
                primary: true,
              },
              { key: "edit", label: t("编辑整个文件"), hint: t("读取整个文件并启用编辑与保存") },
            ],
          });
          if (cancelled) return;
          if (choice === "view") {
            const current = locatorRef.current;
            openLogTab(current.sessionId, current.path);
            onCloseRef.current?.();
            return;
          }
          if (choice !== "edit") {
            onCloseRef.current?.();
            return;
          }
          largeEditConfirmed = true;
        }
        const readMaxBytes = probedSize === null ? undefined : probedSize + 1024;
        const readLocator = locatorRef.current;
        const res = await fsApi.read(readLocator.sessionId, readLocator.path, readMaxBytes);
        if (cancelled) return;
        if (
          !largeEditConfirmed &&
          res.size > 5 * 1024 * 1024 &&
          !(await ask(t("文件超过 5MB，确定要编辑？"), { kind: "info" }))
        ) {
          onCloseRef.current?.();
          return;
        }
        if (cancelled) return;
        const bin = bytesFromBase64(res.contentBase64);
        const { text, encoding } = decodeContent(bin, reloadEnc);
        originalRef.current = text;
        versionRef.current = fileVersionOf(bin).catch(() => null);
        setDiskChange(null);
        setShowDiff(false);
        setMeta({ encoding, size: res.size });
        setEol(text.includes("\r\n") ? "CRLF" : "LF");
        dirtyRef.current = false;
        const current = locatorRef.current;
        setFileEditorDirty(current.sessionId, current.path, false, ci);
        setDirty(false);
        if (hostRef.current) {
          viewRef.current?.destroy();
          viewRef.current = null;
          viewRef.current = new EditorView({
            state: EditorState.create({
              doc: text,
              extensions: [
                basicSetup,
                search({ top: true }),
                language.of([]),
                keymap.of([indentWithTab]),
                nxHighlight,
                EditorView.updateListener.of((u) => {
                  if (u.docChanged && !applyingDiskRef.current) {
                    editVersionRef.current += 1;
                    dirtyRef.current = true;
                    const latest = locatorRef.current;
                    setFileEditorDirty(latest.sessionId, latest.path, true, ci);
                    setDirty(true);
                    const s = u.state.doc.toString();
                    setEol(s.includes("\r\n") ? "CRLF" : "LF");
                  }
                }),
              ],
            }),
            parent: hostRef.current,
          });
          languageAppliedRef.current = false;
          applyLanguage(viewRef.current, locatorRef.current.path, languageChoiceRef.current);
          revealRef.current();
        }
      } catch (e) {
        if (!cancelled) {
          pushToast("error", t("打开失败：{value}", { value: describeError(e) }));
          onCloseRef.current?.();
        }
      } finally {
        const current = loadLocksRef.current.get(lockKey);
        if (current?.token === token) {
          current.users -= 1;
          if (current.users === 0) {
            endFileEditorOperation(token);
            loadLocksRef.current.delete(lockKey);
          }
        }
      }
    })();
    return () => {
      cancelled = true;
      viewRef.current?.destroy();
      viewRef.current = null;
    };
  }, [sessionId, reloadEnc, reloadKey, language, pushToast, applyLanguage]);

  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    applyLanguage(view, path, languageChoice);
  }, [path, languageChoice, applyLanguage]);

  const save = async () => {
    const view = viewRef.current;
    if (!view || !dirtyRef.current || saveInFlightRef.current) return;
    const initialLocator = locatorRef.current;
    const guard = beginFileEditorSave(initialLocator.sessionId, initialLocator.path, caseInsensitive);
    if (typeof guard === "string") {
      if (guard === "unknown") {
        const operation = unknownFileEditorRename(initialLocator.sessionId, initialLocator.path);
        if (!operation) return;
        pushToast("error", t("重命名结果未知，已阻止向旧路径保存；请先确认远端文件位置。"));
        saveInFlightRef.current = true;
        setSaving(true);
        try {
          const choice = await askChoice(
            t("重命名结果未知，不会自动重试。请先在文件列表确认旧路径和新路径，再选择实际结果。当前标签页路径：{path}", { path: initialLocator.path }),
            {
              title: t("确认文件位置"),
              level: "warning",
              choices: [
                {
                  key: "not-renamed",
                  label: t("重命名未发生"),
                  hint: t("确认旧路径仍是要保存的位置，清除未知记录并恢复旧路径保存"),
                },
                {
                  key: "renamed",
                  label: t("重命名已发生"),
                  hint: t("确认服务端已完成重命名，迁移匹配的标签页和草稿，不重复执行重命名"),
                },
              ],
            },
          );
          if (choice === "not-renamed" || choice === "renamed") {
            const current = unknownFileEditorRenameByToken(operation.token);
            if (!current) return;
            if (choice === "renamed") {
              const state = useUi.getState();
              const target = unknownFileEditorRenameConflict(state, current);
              if (target) {
                state.setActiveTab(target.id);
                pushToast(
                  "error",
                  t("无法确认重命名已发生：目标 {path} 已有打开的编辑器，请先处理该标签页，未迁移或合并草稿。", { path: target.path }),
                );
                return;
              }
            }
            useUi.getState().resolveUnknownFileEditorRename(
              current.sessionId,
              current.from,
              choice === "renamed",
            );
            void qc.invalidateQueries({ queryKey: ["fs", current.sessionId] });
          }
        } finally {
          saveInFlightRef.current = false;
          if (mountedRef.current) setSaving(false);
        }
      } else {
        pushToast(
          "info",
          guard === "rename" ? t("重命名进行中，完成后请再次保存") : t("另一个读取或保存操作进行中，请等待完成"),
        );
      }
      return;
    }
    saveInFlightRef.current = true;
    setSaving(true);
    try {
      if (meta && meta.encoding !== "utf-8") {
        const ok = await ask(
          t("文件按原编码解码，保存后将转换为 UTF-8，使用原编码打开可能出现乱码。继续？"),
          { title: t("编码会改变"), kind: "warning" },
        );
        if (!ok) return;
      }
      const text = view.state.doc.toString();
      const savedVersion = editVersionRef.current;
      const bytes = new TextEncoder().encode(text);
      const savedHostVersion = fileVersionOf(bytes).catch(() => null);
      const b64 = bytesToBase64(bytes);
      const writeLocator = locatorRef.current;
      if (saveFile) {
        const change = await loadFileEditorSaveChange({
          sessionId: writeLocator.sessionId,
          path: writeLocator.path,
          editorBase: originalRef.current,
          content: text,
          encoding: reloadEnc,
        });
        if (!(await saveFile(change))) return;
      } else {
        const expected = await versionRef.current;
        await fsApi.write(writeLocator.sessionId, writeLocator.path, b64, undefined, undefined, undefined, expected ?? undefined);
      }
      versionRef.current = savedHostVersion;
      originalRef.current = text;
      if (mountedRef.current) {
        setDiskChange(null);
        setShowDiff(false);
      }
      if (mountedRef.current) {
        setMeta({ encoding: "utf-8", size: bytes.length });
        if (meta && meta.encoding !== "utf-8") setEnc("utf-8");
      }
      if (shouldClearEditorDirty(savedVersion, editVersionRef.current)) {
        dirtyRef.current = false;
        if (mountedRef.current) {
          const current = locatorRef.current;
          setFileEditorDirty(current.sessionId, current.path, false, caseInsensitive);
          setDirty(false);
        }
      }
      pushToast("success", t("已保存 {path}", { path: writeLocator.path }));
    } catch (e) {
      if (isVersionConflict(e)) {
        void checkDisk("conflict");
      } else {
        pushToast("error", t("保存失败：{value}", { value: describeError(e) }));
      }
    } finally {
      endFileEditorOperation(guard);
      saveInFlightRef.current = false;
      if (mountedRef.current) setSaving(false);
    }
  };
  saveRef.current = () => void save();

  // checkDisk reads the file again after the host reported a change, or after a save was refused because of one.
  // An unchanged editor takes the new content in place; one with unsaved edits asks first.
  const checkDiskRef = useRef<(reason: "changed" | "conflict") => Promise<void>>(async () => undefined);
  checkDiskRef.current = async (reason) => {
    const view = viewRef.current;
    if (!view || (reason === "changed" && saveInFlightRef.current)) return;
    const locator = locatorRef.current;
    // The locator object is rebuilt on every render; only a different file makes the result stale.
    const stale = () =>
      locatorRef.current.sessionId !== locator.sessionId || locatorRef.current.path !== locator.path || !mountedRef.current;
    let res: Awaited<ReturnType<typeof fsApi.read>>;
    try {
      res = await fsApi.read(locator.sessionId, locator.path);
    } catch (e) {
      if (stale()) return;
      if (fsErrorIsMissing(e)) {
        versionRef.current = Promise.resolve(null);
        setDiskChange({ kind: "deleted" });
      }
      return;
    }
    if (stale() || viewRef.current !== view) return;
    const bin = bytesFromBase64(res.contentBase64);
    const version = await fileVersionOf(bin);
    const known = await versionRef.current;
    if (stale() || viewRef.current !== view) return;
    if (known && known.size === version.size && known.sha256 === version.sha256) {
      if (reason === "changed") setDiskChange((current) => (current?.kind === "deleted" ? null : current));
      return;
    }
    const { text, encoding } = decodeContent(bin, reloadEnc);
    if (reason === "changed" && !dirtyRef.current) {
      applyDiskText(view, text, version, encoding, res.size);
      pushToast("info", t("{path} 已在主机上被修改，已重新读取", { path: baseName(locator.path) }));
      return;
    }
    setDiskChange({ kind: reason, text, version, encoding, size: res.size });
  };
  const checkDisk = (reason: "changed" | "conflict") => checkDiskRef.current(reason);

  // applyDiskText replaces the document with the host content, keeping the cursor and scroll position where they fit.
  const applyDiskText = (view: EditorView, text: string, version: FileVersion, encoding: string, size: number) => {
    const head = Math.min(view.state.selection.main.head, text.length);
    const scrollTop = view.scrollDOM.scrollTop;
    applyingDiskRef.current = true;
    try {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, selection: { anchor: head } });
    } finally {
      applyingDiskRef.current = false;
    }
    view.scrollDOM.scrollTop = scrollTop;
    originalRef.current = text;
    versionRef.current = Promise.resolve(version);
    dirtyRef.current = false;
    const current = locatorRef.current;
    setFileEditorDirty(current.sessionId, current.path, false, caseInsensitive);
    setDirty(false);
    setMeta({ encoding, size });
    setEol(text.includes("\r\n") ? "CRLF" : "LF");
    setDiskChange(null);
    setShowDiff(false);
  };

  const parentDir = parentOf(path);
  useFsWatch(sessionId, parentDir ? [parentDir] : [], meta !== null);
  useEffect(() => {
    if (!parentDir) return;
    const name = baseName(path);
    let timer = 0;
    const off = subscribeFsChanged((event) => {
      if (event.sessionId !== sessionId || event.dir !== parentDir) return;
      if (event.names?.length && !event.names.includes(name)) return;
      window.clearTimeout(timer);
      timer = window.setTimeout(() => void checkDiskRef.current("changed"), 300);
    });
    return () => {
      off();
      window.clearTimeout(timer);
    };
  }, [sessionId, path, parentDir]);

  const takeDiskVersion = () => {
    const view = viewRef.current;
    if (!view || !diskChange || diskChange.kind === "deleted") return;
    applyDiskText(view, diskChange.text, diskChange.version, diskChange.encoding, diskChange.size);
  };

  // keepMine accepts the host version as the base the editor content replaces, so the next save overwrites it.
  const keepMine = (thenSave: boolean) => {
    if (!diskChange) return;
    versionRef.current = Promise.resolve(diskChange.kind === "deleted" ? null : diskChange.version);
    setDiskChange(null);
    setShowDiff(false);
    if (thenSave) saveRef.current();
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== "s") return;
      const state = useUi.getState();
      if (!isActiveFileEditor(state, sessionId, path, caseInsensitive)) return;
      e.preventDefault();
      if (
        shouldHandleEditorSave(
          state,
          sessionId,
          path,
          dirtyRef.current,
          saveInFlightRef.current,
          caseInsensitive,
        )
      ) {
        saveRef.current();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [sessionId, path, caseInsensitive]);

  const withView = (fn: (v: EditorView) => void) => {
    const v = viewRef.current;
    if (v) fn(v);
  };

  const toggleEol = () => {
    withView((v) => {
      const src = v.state.doc.toString();
      const next = eol === "LF" ? src.replace(/\r?\n/g, "\r\n") : src.replace(/\r\n/g, "\n");
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: next } });
      setEol(eol === "LF" ? "CRLF" : "LF");
      pushToast("info", t("换行符已转换为 {value}，尚未保存", { value: eol === "LF" ? "CRLF" : "LF" }));
    });
  };

  const reloadFromDisk = () => {
    if (saveInFlightRef.current) {
      pushToast("info", t("保存进行中，完成后才能重新读取文件"));
      return;
    }
    if (fileEditorRenamePending(sessionId, path)) {
      pushToast("info", t("重命名进行中，完成后才能重新读取文件"));
      return;
    }
    setReloadEnc(enc);
    setReloadKey((k) => k + 1);
  };

  const switchEnc = async (next: EncChoice) => {
    if (saveInFlightRef.current) {
      pushToast("info", t("保存进行中，完成后才能切换编码"));
      return;
    }
    if (fileEditorRenamePending(sessionId, path)) {
      pushToast("info", t("重命名进行中，完成后才能切换编码"));
      return;
    }
    if (
      dirtyRef.current &&
      !(await ask(t("切换编码会重新读取文件，未保存的改动会丢失。继续？"), { kind: "warning" }))
    ) {
      return;
    }
    if (saveInFlightRef.current) {
      pushToast("info", t("保存进行中，完成后才能切换编码"));
      return;
    }
    if (fileEditorRenamePending(sessionId, path)) {
      pushToast("info", t("重命名进行中，完成后才能切换编码"));
      return;
    }
    setEnc(next);
    setReloadEnc(next);
  };

  const { Icon: FileIcon, tone } = fileVisual(path, "file");
  const autoLanguage = detectFileLanguage(path);
  const steps = ZOOM_STEPS.length - 1;
  const fontPx = ZOOM_STEPS[zoom] ?? 13;

  return (
    <div className="nx-pane bg-term">
      <div className="nx-toolbar nx-wrap-toolbar nx-files-editor-bar">
        <FileIcon size={14} className={`shrink-0 ${tone}`} />
        <span className="nx-toolbar-title truncate font-mono">{path}</span>
        {dirty && (
          <span className="nx-badge nx-badge-amber shrink-0">
            <span className="nx-dot" />
            {t("未保存")}
          </span>
        )}
        <div className="nx-spacer" />

        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("撤销 ({value}+Z)", { value: modHint() })}
          aria-label={t("撤销")}
          onClick={() => withView(undo)}
        >
          <IconUndo size={13} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("重做 ({value}+Shift+Z)", { value: modHint() })}
          aria-label={t("重做")}
          onClick={() => withView(redo)}
        >
          <IconRedo size={13} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("查找 / 替换 ({value}+F)", { value: modHint() })}
          aria-label={t("查找 / 替换")}
          onClick={() => withView(openSearchPanel)}
        >
          <IconSearch size={13} />
        </button>

        <span className="nx-divider-v" />

        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("缩小字号")}
          aria-label={t("缩小字号")}
          disabled={zoom <= 0}
          onClick={() => setZoom((z) => Math.max(0, z - 1))}
        >
          <IconMinus size={13} />
        </button>
        <span
          className="w-[22px] shrink-0 text-center text-[10.5px] text-neutral-500"
          title={t("当前字号")}
        >
          {fontPx}
        </span>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("放大字号")}
          aria-label={t("放大字号")}
          disabled={zoom >= steps}
          onClick={() => setZoom((z) => Math.min(steps, z + 1))}
        >
          <IconPlus size={13} />
        </button>

        <span className="nx-divider-v" />

        <button
          className="nx-btn nx-btn-ghost nx-btn-xs font-mono"
          title={t("当前换行符 {eol}，单击可转换", { eol })}
          onClick={toggleEol}
        >
          {eol}
        </button>
        <Select
          className="shrink-0"
          triggerClassName="nx-btn nx-btn-ghost nx-btn-xs font-mono"
          ariaLabel={t("解码方式")}
          title={t("解码方式：{value}{value2}", { value: ENC_LABEL[enc], value2: meta && enc === "auto" ? t("（检测为 {toUpperCase}）", { toUpperCase: meta.encoding.toUpperCase() }) : "" })}
          value={enc}
          disabled={saving}
          options={(Object.keys(ENC_LABEL) as EncChoice[]).map((k) =>
            k === "auto"
              ? {
                  value: k,
                  label: enc === "auto" && meta ? meta.encoding.toUpperCase() : ENC_LABEL[k],
                  description: t("自动检测"),
                }
              : { value: k, label: ENC_LABEL[k] },
          )}
          onChange={(value) => void switchEnc(value as EncChoice)}
        />
        <Select
          className="w-[124px] shrink-0"
          ariaLabel={t("语法高亮")}
          title={t("语法高亮（手动选择仅用于当前打开的文件）")}
          value={languageChoice}
          options={[
            { value: "auto", label: autoLanguage ? t("自动（{value}）", { value: fileLanguageLabel(autoLanguage) }) : t("自动") },
            { value: "plain", label: t("纯文本") },
            ...FILE_LANGUAGE_OPTIONS.map((option) => ({ value: option.id, label: option.label })),
          ]}
          onChange={(value) =>
            setLanguageSelection({
              sessionId,
              path,
              choice: value as FileLanguageChoice,
            })
          }
        />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("重新读取（丢弃未保存改动）")}
          aria-label={t("重新读取")}
          disabled={saving}
          onClick={() => {
            if (!dirtyRef.current) {
              reloadFromDisk();
              return;
            }
            void ask(t("重新读取会丢弃未保存的改动。继续？")).then((ok) => {
              if (ok) reloadFromDisk();
            });
          }}
        >
          <IconRefresh size={13} />
        </button>

        <span className="nx-divider-v" />

        <button
          className={`nx-btn nx-btn-sm max-[560px]:order-first ${dirty ? "nx-btn-primary" : "nx-btn-ghost"}`}
          disabled={!dirty || saving}
          onClick={() => void save()}
          title={t("保存")}
        >
          <IconSave size={13} />
          {saving ? t("正在保存…") : t("保存")}
          <span className={`nx-kbd max-[560px]:hidden ${dirty ? "border-white/25 text-white" : ""}`}>{modHint()} S</span>
        </button>
        {onClose && (
          <button className="nx-icon-btn" onClick={onClose} title={t("关闭编辑器")}
          aria-label={t("关闭编辑器")}>
            <IconClose size={14} />
          </button>
        )}
      </div>

      {meta && (
        <div className="flex h-[22px] shrink-0 items-center gap-2 overflow-hidden border-b border-neutral-800/60 px-3 text-[10.5px] text-neutral-500">
          <IconLocate size={10} />
          <span>{(meta.size / 1024).toFixed(1)} KB</span>
          <span className="text-neutral-700">|</span>
          <span>{meta.encoding.toUpperCase()}</span>
          <span className="text-neutral-700">|</span>
          <span>{eol}</span>
        </div>
      )}

      {diskChange && (
        <div
          className="shrink-0 border-b border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-[11.5px] text-amber-300"
          role="alert"
          data-testid="file-disk-change"
          data-kind={diskChange.kind}
        >
          <div className="flex flex-wrap items-center gap-2">
            <span className="min-w-0 flex-1">
              {diskChange.kind === "deleted"
                ? t("此文件已在主机上被删除或移走，保存会重新创建它。")
                : diskChange.kind === "conflict"
                  ? t("保存被拒绝：文件在打开后已在主机上被修改。")
                  : t("此文件已在主机上被修改，而编辑器中有未保存的改动。")}
            </span>
            {diskChange.kind === "deleted" ? (
              <button className="nx-btn nx-btn-sm" onClick={() => keepMine(false)}>
                {t("知道了")}
              </button>
            ) : (
              <>
                <button className="nx-btn nx-btn-sm" onClick={() => setShowDiff((v) => !v)}>
                  {showDiff ? t("隐藏差异") : t("对比差异")}
                </button>
                <button className="nx-btn nx-btn-sm" onClick={takeDiskVersion}>
                  {t("使用主机上的版本")}
                </button>
                <button className="nx-btn nx-btn-sm" onClick={() => keepMine(diskChange.kind === "conflict")}>
                  {diskChange.kind === "conflict" ? t("用我的版本覆盖") : t("保留我的修改")}
                </button>
              </>
            )}
          </div>
          {showDiff && diskChange.kind !== "deleted" && (
            <pre
              className="mt-1.5 max-h-48 overflow-auto rounded bg-black/30 p-2 font-mono text-[11px] leading-4 text-neutral-300"
              data-testid="file-disk-diff"
            >
              {simpleDiff(diskChange.text, viewRef.current?.state.doc.toString() ?? "")
                .map((line) => diffLineText(line))
                .join("\n") || t("内容相同")}
            </pre>
          )}
        </div>
      )}

      <div
        ref={hostRef}
        className="min-h-0 flex-1 overflow-auto"
        style={{ fontSize: fontPx }}
      />
    </div>
  );
}
