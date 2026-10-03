// 文件编辑器（M1-T7）：CodeMirror 6，打开/编辑/保存 + 备份确认 + GBK 解码。
//
// 工具条对齐参考实现：保存 / 撤销 / 重做 / 查找 / 缩放 / 换行符 / 编码。
// 其中「换行符」是真的转换（重写整篇文档的换行），「编码」是真的换解码器
// （会重新读盘），不是只显示一个标签。
import { useEffect, useRef, useState } from "react";
import { EditorView, keymap } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { indentWithTab, redo, undo } from "@codemirror/commands";
import { openSearchPanel, search } from "@codemirror/search";
import { ask } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
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
import { isActiveFileEditor, setFileEditorDirty, shouldClearEditorDirty, shouldHandleEditorSave } from "./editorGuards";

// 轻量语法高亮：按扩展名选 legacy mode
import { StreamLanguage } from "@codemirror/language";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { nginx } from "@codemirror/legacy-modes/mode/nginx";
import { yaml } from "@codemirror/legacy-modes/mode/yaml";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { javascript } from "@codemirror/legacy-modes/mode/javascript";
import { python } from "@codemirror/legacy-modes/mode/python";
import { sql } from "@codemirror/lang-sql";
import { describeError } from "../../ui/errorText";

export interface FileEditorProps {
  sessionId: string;
  path: string;
  onClose?: () => void;
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

/** 字号档位（工具条上的 -/+ 在这几个值之间走）。 */
const ZOOM_STEPS = [11, 12, 13, 14.5, 16, 18];

export function FileEditor({ sessionId, path, onClose }: FileEditorProps) {
  const { pushToast } = useUi();
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [meta, setMeta] = useState<{ encoding: string; size: number } | null>(null);
  const [zoom, setZoom] = useState(2);
  /** 换行符：从文档内容实时推导，工具条上那个 LF / CRLF 就是它。 */
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
  dirtyRef.current = dirty;

  useEffect(() => {
    setFileEditorDirty(sessionId, path, dirty);
    if (!dirty) return;
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [sessionId, path, dirty]);

  useEffect(() => {
    return () => setFileEditorDirty(sessionId, path, false);
  }, [sessionId, path]);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const res = await fsApi.read(sessionId, path);
        if (cancelled) return;
        if (res.size > 5 * 1024 * 1024 && !(await ask("文件超过 5MB，确定要编辑？", { kind: "info" }))) {
          onClose?.();
          return;
        }
        if (cancelled) return;
        const bin = bytesFromBase64(res.contentBase64);
        const { text, encoding } = decodeContent(bin, reloadEnc);
        setMeta({ encoding, size: res.size });
        setEol(text.includes("\r\n") ? "CRLF" : "LF");
        dirtyRef.current = false;
        setFileEditorDirty(sessionId, path, false);
        setDirty(false);
        if (hostRef.current) {
          // 换编码会重建视图，所以旧实例必须显式销毁（否则会叠加两个画布）
          viewRef.current?.destroy();
          viewRef.current = null;
          const mode = modeFor(path);
          viewRef.current = new EditorView({
            state: EditorState.create({
              doc: text,
              extensions: [
                basicSetup,
                // 顶部搜索面板（basicSetup 只带了快捷键，面板本身要显式装）
                search({ top: true }),
                ...(mode ? [mode] : []),
                keymap.of([indentWithTab]),
                nxHighlight,
                EditorView.updateListener.of((u) => {
                  if (u.docChanged) {
                    editVersionRef.current += 1;
                    dirtyRef.current = true;
                    setFileEditorDirty(sessionId, path, true);
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
        pushToast("error", `打开失败: ${describeError(e)}`);
        onClose?.();
      }
    })();
    return () => {
      cancelled = true;
      viewRef.current?.destroy();
      viewRef.current = null;
    };
    // reloadEnc / reloadKey 变化 = 重新读盘重解码
  }, [sessionId, path, reloadEnc, reloadKey]);

  const save = async () => {
    const view = viewRef.current;
    if (!view || !dirtyRef.current || saveInFlightRef.current) return;
    saveInFlightRef.current = true;
    setSaving(true);
    try {
      // 非 UTF-8 解码的文件没法按原编码写回（前端没有 GBK 编码器），
      // 所以这里必须显式告知，而不是悄悄写成 UTF-8。
      if (meta && meta.encoding !== "utf-8") {
        const ok = await ask(
          `这个文件是按 ${meta.encoding.toUpperCase()} 解码的。\n保存会写成 UTF-8，非 ASCII 字符的字节会变。继续？`,
          { title: "编码会改变", kind: "warning" },
        );
        if (!ok) return;
      }
      const text = view.state.doc.toString();
      const savedVersion = editVersionRef.current;
      const bytes = new TextEncoder().encode(text);
      const b64 = bytesToBase64(bytes);
      // 默认"保存即覆盖 + 保留远端备份"（§5.4）
      await fsApi.write(sessionId, path, b64, true);
      setMeta({ encoding: "utf-8", size: bytes.length });
      if (meta && meta.encoding !== "utf-8") setEnc("utf-8");
      if (shouldClearEditorDirty(savedVersion, editVersionRef.current)) {
        dirtyRef.current = false;
        setFileEditorDirty(sessionId, path, false);
        setDirty(false);
      }
      pushToast("success", `已保存 ${path}（远端已备份 .nexterm-bak）`);
    } catch (e) {
      pushToast("error", `保存失败: ${describeError(e)}`);
    } finally {
      saveInFlightRef.current = false;
      setSaving(false);
    }
  };
  saveRef.current = () => void save();

  // Ctrl/Cmd+S 保存
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== "s") return;
      const state = useUi.getState();
      if (!isActiveFileEditor(state, sessionId, path)) return;
      e.preventDefault();
      if (shouldHandleEditorSave(state, sessionId, path, dirtyRef.current, saveInFlightRef.current)) {
        saveRef.current();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [sessionId, path]);

  const withView = (fn: (v: EditorView) => void) => {
    const v = viewRef.current;
    if (v) fn(v);
  };

  /** LF ⇄ CRLF：整篇转换（脏标记由 updateListener 自动接上）。 */
  const toggleEol = () => {
    withView((v) => {
      const src = v.state.doc.toString();
      const next = eol === "LF" ? src.replace(/\r?\n/g, "\r\n") : src.replace(/\r\n/g, "\n");
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: next } });
      setEol(eol === "LF" ? "CRLF" : "LF");
      pushToast("info", `换行符已转为 ${eol === "LF" ? "CRLF" : "LF"}，记得保存`);
    });
  };

  const reloadFromDisk = () => {
    if (saveInFlightRef.current) {
      pushToast("info", "保存进行中，完成后才能重新读取文件");
      return;
    }
    setReloadEnc(enc);
    setReloadKey((k) => k + 1);
  };

  /** 切换解码方式：有未保存改动时先确认（会重新读盘）。 */
  const switchEnc = async (next: EncChoice) => {
    if (saveInFlightRef.current) {
      pushToast("info", "保存进行中，完成后才能切换编码");
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

        {/* 编辑动作 */}
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="撤销 (Ctrl+Z)"
          onClick={() => withView(undo)}
        >
          <IconUndo size={13} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="重做 (Ctrl+Shift+Z)"
          onClick={() => withView(redo)}
        >
          <IconRedo size={13} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="查找 / 替换 (Ctrl+F)"
          onClick={() => withView(openSearchPanel)}
        >
          <IconSearch size={13} />
        </button>

        <span className="nx-divider-v" />

        {/* 缩放 */}
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

        {/* 换行符 / 编码 */}
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
            meta && enc === "auto" ? `（实测 ${meta.encoding.toUpperCase()}）` : ""
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
          className={`nx-btn nx-btn-sm ${dirty ? "nx-btn-primary" : "nx-btn-ghost"}`}
          disabled={!dirty || saving}
          onClick={() => void save()}
          title="保存（远端自动留一份 .nexterm-bak 备份）"
        >
          <IconSave size={13} />
          {saving ? "保存中…" : "保存"}
          <span className="nx-kbd border-white/25 text-white/70">Ctrl S</span>
        </button>
        {onClose && (
          <button className="nx-icon-btn" onClick={onClose} title="关闭编辑器">
            <IconClose size={14} />
          </button>
        )}
      </div>

      {meta && (
        <div className="flex h-[22px] shrink-0 items-center gap-2 border-b border-neutral-800/60 px-3 text-[10.5px] text-neutral-500">
          <IconLocate size={10} />
          <span>{(meta.size / 1024).toFixed(1)} KB</span>
          <span className="text-neutral-700">|</span>
          <span>{meta.encoding.toUpperCase()}</span>
          <span className="text-neutral-700">|</span>
          <span>{eol}</span>
          <div className="nx-spacer" />
          <span>保存前自动备份为 .nexterm-bak</span>
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
