import { useEffect, useRef, useState } from "react";
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

import { StreamLanguage } from "@codemirror/language";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { nginx } from "@codemirror/legacy-modes/mode/nginx";
import { yaml } from "@codemirror/legacy-modes/mode/yaml";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { javascript } from "@codemirror/legacy-modes/mode/javascript";
import { python } from "@codemirror/legacy-modes/mode/python";
import { sql } from "@codemirror/lang-sql";
import { describeError } from "../../ui/errorText";

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
    throw new Error("文件在打开后已被外部修改；请重新读取并合并改动后再保存");
  }
  return { sessionId, path, previous: currentText, baseContentBase64: current.contentBase64, content };
}

function modeFor(path: string) {
  const lower = path.toLowerCase();
  if (lower.endsWith(".py")) return StreamLanguage.define(python);
  if (lower.endsWith(".sh") || lower.endsWith(".bash") || lower.endsWith(".bashrc") || lower.endsWith(".zshrc") || lower.endsWith(".profile"))
    return StreamLanguage.define(shell);
  if (lower.includes("nginx")) return StreamLanguage.define(nginx);
  if (lower.endsWith(".yml") || lower.endsWith(".yaml")) return StreamLanguage.define(yaml);
  if (lower.endsWith(".ini") || lower.endsWith(".conf") || lower.endsWith(".env"))
    return StreamLanguage.define(properties);
  if (lower.endsWith(".js") || lower.endsWith(".mjs") || lower.endsWith(".json") || lower.endsWith(".ts"))
    return StreamLanguage.define(javascript);
  if (lower.endsWith(".sql")) return sql();
  return undefined;
}

const ENC_LABEL: Record<EncChoice, string> = {
  auto: "自动",
  "utf-8": "UTF-8",
  gbk: "GBK",
};

const ZOOM_STEPS = [11, 12, 13, 14.5, 16, 18];

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
  const viewRef = useRef<EditorView | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);
  const saveRef = useRef<() => void>(() => undefined);
  const saveInFlightRef = useRef(false);
  const editVersionRef = useRef(0);
  const dirtyRef = useRef(false);
  const originalRef = useRef("");
  const mountedRef = useRef(true);
  const locatorRef = useRef({ sessionId, path });
  const loadLocksRef = useRef(new Map<string, { token: number; users: number }>());
  const language = useRef(new Compartment()).current;
  locatorRef.current = { sessionId, path };
  dirtyRef.current = dirty;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

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
    const initialLocator = locatorRef.current;
    const lockKey = fileEditorLocatorKey(
      initialLocator.sessionId,
      initialLocator.path,
      caseInsensitive,
    );
    let lock = loadLocksRef.current.get(lockKey);
    if (!lock) {
      const token = beginFileEditorLoad(initialLocator.sessionId, initialLocator.path, caseInsensitive);
      if (token === null) {
        pushToast("info", "文件读取或重命名进行中，完成后请重新读取");
        onClose?.();
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
          const choice = await askChoice("文件超过 5MB，整文件编辑可能卡顿。请选择打开方式。", {
            title: "大文件",
            level: "info",
            choices: [
              {
                key: "view",
                label: "只读分块查看",
                hint: "使用日志查看器按需读取，不加载整个文件",
                primary: true,
              },
              { key: "edit", label: "编辑整个文件", hint: "读取整个文件并启用编辑与保存" },
            ],
          });
          if (cancelled) return;
          if (choice === "view") {
            const current = locatorRef.current;
            openLogTab(current.sessionId, current.path);
            onClose?.();
            return;
          }
          if (choice !== "edit") {
            onClose?.();
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
          !(await ask("文件超过 5MB，确定要编辑？", { kind: "info" }))
        ) {
          onClose?.();
          return;
        }
        if (cancelled) return;
        const bin = bytesFromBase64(res.contentBase64);
        const { text, encoding } = decodeContent(bin, reloadEnc);
        originalRef.current = text;
        setMeta({ encoding, size: res.size });
        setEol(text.includes("\r\n") ? "CRLF" : "LF");
        dirtyRef.current = false;
        const current = locatorRef.current;
        setFileEditorDirty(current.sessionId, current.path, false, caseInsensitive);
        setDirty(false);
        if (hostRef.current) {
          viewRef.current?.destroy();
          viewRef.current = null;
          const mode = modeFor(locatorRef.current.path);
          viewRef.current = new EditorView({
            state: EditorState.create({
              doc: text,
              extensions: [
                basicSetup,
                search({ top: true }),
                language.of(mode ? [mode] : []),
                keymap.of([indentWithTab]),
                nxHighlight,
                EditorView.updateListener.of((u) => {
                  if (u.docChanged) {
                    editVersionRef.current += 1;
                    dirtyRef.current = true;
                    const latest = locatorRef.current;
                    setFileEditorDirty(latest.sessionId, latest.path, true, caseInsensitive);
                    setDirty(true);
                    const s = u.state.doc.toString();
                    setEol(s.includes("\r\n") ? "CRLF" : "LF");
                  }
                }),
              ],
            }),
            parent: hostRef.current,
          });
        }
      } catch (e) {
        if (!cancelled) {
          pushToast("error", `打开失败：${describeError(e)}`);
          onClose?.();
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
  }, [sessionId, reloadEnc, reloadKey, language]);

  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    const mode = modeFor(path);
    view.dispatch({ effects: language.reconfigure(mode ? [mode] : []) });
  }, [path, language]);

  const save = async () => {
    const view = viewRef.current;
    if (!view || !dirtyRef.current || saveInFlightRef.current) return;
    const initialLocator = locatorRef.current;
    const guard = beginFileEditorSave(initialLocator.sessionId, initialLocator.path, caseInsensitive);
    if (typeof guard === "string") {
      if (guard === "unknown") {
        const operation = unknownFileEditorRename(initialLocator.sessionId, initialLocator.path);
        if (!operation) return;
        pushToast("error", "重命名结果未知，已阻止向旧路径保存；请先确认远端文件位置。");
        saveInFlightRef.current = true;
        setSaving(true);
        try {
          const choice = await askChoice(
            `重命名结果未知，不会自动重试。请先在文件列表确认旧路径和新路径，再选择实际结果。当前标签路径：${initialLocator.path}`,
            {
              title: "确认文件位置",
              level: "warning",
              choices: [
                {
                  key: "not-renamed",
                  label: "重命名未发生",
                  hint: "确认旧路径仍是要保存的位置，清除未知记录并恢复旧路径保存",
                },
                {
                  key: "renamed",
                  label: "重命名已发生",
                  hint: "确认服务端已完成重命名，迁移匹配的标签和草稿，不重复执行重命名",
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
                  `无法确认重命名已发生：目标 ${target.path} 已有打开的编辑器，请先处理该标签，未迁移或合并草稿。`,
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
          guard === "rename" ? "重命名进行中，完成后请再次保存" : "另一个读取或保存操作进行中，请等待完成",
        );
      }
      return;
    }
    saveInFlightRef.current = true;
    setSaving(true);
    try {
      if (meta && meta.encoding !== "utf-8") {
        const ok = await ask(
          `文件按原编码解码，保存后将转换为 UTF-8，使用原编码打开可能出现乱码。继续？`,
          { title: "编码会改变", kind: "warning" },
        );
        if (!ok) return;
      }
      const text = view.state.doc.toString();
      const savedVersion = editVersionRef.current;
      const bytes = new TextEncoder().encode(text);
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
        await fsApi.write(writeLocator.sessionId, writeLocator.path, b64, true);
      }
      originalRef.current = text;
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
      pushToast("success", `已保存 ${writeLocator.path}（远端已备份 .nexterm-bak）`);
    } catch (e) {
      pushToast("error", `保存失败：${describeError(e)}`);
    } finally {
      endFileEditorOperation(guard);
      saveInFlightRef.current = false;
      if (mountedRef.current) setSaving(false);
    }
  };
  saveRef.current = () => void save();

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
      pushToast("info", `换行符已转换为 ${eol === "LF" ? "CRLF" : "LF"}，尚未保存`);
    });
  };

  const reloadFromDisk = () => {
    if (saveInFlightRef.current) {
      pushToast("info", "保存进行中，完成后才能重新读取文件");
      return;
    }
    if (fileEditorRenamePending(sessionId, path)) {
      pushToast("info", "重命名进行中，完成后才能重新读取文件");
      return;
    }
    setReloadEnc(enc);
    setReloadKey((k) => k + 1);
  };

  const switchEnc = async (next: EncChoice) => {
    if (saveInFlightRef.current) {
      pushToast("info", "保存进行中，完成后才能切换编码");
      return;
    }
    if (fileEditorRenamePending(sessionId, path)) {
      pushToast("info", "重命名进行中，完成后才能切换编码");
      return;
    }
    if (
      dirtyRef.current &&
      !(await ask("切换编码会重新读取文件，未保存的改动会丢失。继续？", { kind: "warning" }))
    ) {
      return;
    }
    if (saveInFlightRef.current) {
      pushToast("info", "保存进行中，完成后才能切换编码");
      return;
    }
    if (fileEditorRenamePending(sessionId, path)) {
      pushToast("info", "重命名进行中，完成后才能切换编码");
      return;
    }
    setEnc(next);
    setReloadEnc(next);
  };

  const { Icon: FileIcon, tone } = fileVisual(path, "file");
  const steps = ZOOM_STEPS.length - 1;
  const fontPx = ZOOM_STEPS[zoom] ?? 13;

  return (
    <div className="nx-pane bg-term">
      <div className="nx-toolbar">
        <FileIcon size={14} className={`shrink-0 ${tone}`} />
        <span className="nx-toolbar-title truncate font-mono">{path}</span>
        {dirty && (
          <span className="nx-badge nx-badge-amber shrink-0">
            <span className="nx-dot" />
            未保存
          </span>
        )}
        <div className="nx-spacer" />

        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={`撤销 (${modHint()}+Z)`}
          onClick={() => withView(undo)}
        >
          <IconUndo size={13} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={`重做 (${modHint()}+Shift+Z)`}
          onClick={() => withView(redo)}
        >
          <IconRedo size={13} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={`查找 / 替换 (${modHint()}+F)`}
          onClick={() => withView(openSearchPanel)}
        >
          <IconSearch size={13} />
        </button>

        <span className="nx-divider-v" />

        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="缩小字号"
          disabled={zoom <= 0}
          onClick={() => setZoom((z) => Math.max(0, z - 1))}
        >
          <IconMinus size={13} />
        </button>
        <span
          className="w-[22px] shrink-0 text-center text-[10.5px] text-neutral-500"
          title="当前字号"
        >
          {fontPx}
        </span>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="放大字号"
          disabled={zoom >= steps}
          onClick={() => setZoom((z) => Math.min(steps, z + 1))}
        >
          <IconPlus size={13} />
        </button>

        <span className="nx-divider-v" />

        <button
          className="nx-btn nx-btn-ghost nx-btn-xs font-mono"
          title={`当前换行符 ${eol}，点击转换`}
          onClick={toggleEol}
        >
          {eol}
        </button>
        <label
          className="nx-btn nx-btn-ghost nx-btn-xs relative font-mono"
          title={`解码方式：${ENC_LABEL[enc]}${
            meta && enc === "auto" ? `（检测为 ${meta.encoding.toUpperCase()}）` : ""
          }`}
        >
          {enc === "auto" && meta ? meta.encoding.toUpperCase() : ENC_LABEL[enc]}
          <select
            className="absolute h-0 w-0 opacity-0"
            value={enc}
            disabled={saving}
            onChange={(e) => void switchEnc(e.target.value as EncChoice)}
          >
            {(Object.keys(ENC_LABEL) as EncChoice[]).map((k) => (
              <option key={k} value={k}>
                {ENC_LABEL[k]}
              </option>
            ))}
          </select>
        </label>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="重新读取（丢弃未保存改动）"
          disabled={saving}
          onClick={() => {
            if (!dirtyRef.current) {
              reloadFromDisk();
              return;
            }
            void ask("重新读取会丢弃未保存的改动。继续？").then((ok) => {
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
          title="保存（远端自动留一份 .nexterm-bak 备份）"
        >
          <IconSave size={13} />
          {saving ? "保存中…" : "保存"}
          <span className={`nx-kbd max-[560px]:hidden ${dirty ? "border-white/25 text-white" : ""}`}>{modHint()} S</span>
        </button>
        {onClose && (
          <button className="nx-icon-btn" onClick={onClose} title="关闭编辑器">
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
          <div className="nx-spacer" />
          <span className="max-[560px]:hidden">保存前自动备份为 .nexterm-bak</span>
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
