// 文件编辑器（M1-T7）：CodeMirror 6，打开/编辑/保存 + 备份确认 + GBK 解码。
import { useEffect, useRef, useState } from "react";
import { EditorView } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { keymap } from "@codemirror/view";
import { indentWithTab } from "@codemirror/commands";
import { ask } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import { useUi } from "../../app/store";

// 轻量语法高亮：按扩展名选 legacy mode
import { StreamLanguage } from "@codemirror/language";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { nginx } from "@codemirror/legacy-modes/mode/nginx";
import { yaml } from "@codemirror/legacy-modes/mode/yaml";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { javascript } from "@codemirror/legacy-modes/mode/javascript";
import { sql } from "@codemirror/lang-sql";

export interface FileEditorProps {
  sessionId: string;
  path: string;
  onClose?: () => void;
}

function modeFor(path: string) {
  const lower = path.toLowerCase();
  if (lower.endsWith(".sh") || lower.endsWith(".bashrc") || lower.endsWith(".zshrc"))
    return StreamLanguage.define(shell);
  if (lower.includes("nginx")) return StreamLanguage.define(nginx);
  if (lower.endsWith(".yml") || lower.endsWith(".yaml")) return StreamLanguage.define(yaml);
  if (lower.endsWith(".ini") || lower.endsWith(".conf") || lower.endsWith(".env"))
    return StreamLanguage.define(properties);
  if (lower.endsWith(".js") || lower.endsWith(".json") || lower.endsWith(".ts"))
    return StreamLanguage.define(javascript);
  if (lower.endsWith(".sql")) return sql();
  return undefined;
}

function decodeContent(bytes: Uint8Array): { text: string; encoding: string } {
  // UTF-8 优先，失败回退 GBK（§5.4：GBK 文件不乱码）
  try {
    const text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    return { text, encoding: "utf-8" };
  } catch {
    const text = new TextDecoder("gbk").decode(bytes);
    return { text, encoding: "gbk" };
  }
}

export function FileEditor({ sessionId, path, onClose }: FileEditorProps) {
  const { pushToast } = useUi();
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [meta, setMeta] = useState<{ encoding: string; size: number } | null>(null);
  const viewRef = useRef<EditorView | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fsApi.read(sessionId, path);
        if (cancelled) return;
        const bin = Uint8Array.from(atob(res.contentBase64), (c) => c.charCodeAt(0));
        if (res.size > 5 * 1024 * 1024 && !(await ask("文件超过 5MB，确定要编辑？"))) {
          onClose?.();
          return;
        }
        const { text, encoding } = decodeContent(bin);
        setMeta({ encoding, size: res.size });
        if (hostRef.current && !viewRef.current) {
          const view = new EditorView({
            state: EditorState.create({
              doc: text,
              extensions: [
                basicSetup,
                ...(() => {
                  const m = modeFor(path);
                  return m ? [m] : [];
                })(),
                keymap.of([indentWithTab]),
                EditorView.updateListener.of((u) => {
                  if (u.docChanged) setDirty(true);
                }),
                EditorView.theme({
                  "&": { fontSize: "13px", backgroundColor: "#12141a", color: "#d7dae0" },
                }),
              ],
            }),
            parent: hostRef.current,
          });
          viewRef.current = view;
        }
      } catch (e) {
        pushToast("error", `打开失败: ${String(e)}`);
        onClose?.();
      }
    })();
    return () => {
      cancelled = true;
      viewRef.current?.destroy();
      viewRef.current = null;
    };
      }, [sessionId, path]);

  const save = async () => {
    const view = viewRef.current;
    if (!view) return;
    setSaving(true);
    try {
      const text = view.state.doc.toString();
      const bytes = new TextEncoder().encode(text);
      let b64 = "";
      const chunk = 0x8000;
      for (let i = 0; i < bytes.length; i += chunk) {
        b64 += btoa(String.fromCharCode(...bytes.subarray(i, i + chunk)));
      }
      // 默认"保存即覆盖 + 保留远端备份"（§5.4）
      await fsApi.write(sessionId, path, b64, true);
      setDirty(false);
      pushToast("success", `已保存 ${path}（远端已备份 .nexterm-bak）`);
    } catch (e) {
      pushToast("error", `保存失败: ${String(e)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex h-full flex-col bg-[#12141a]">
      <div className="flex items-center gap-2 border-b border-neutral-800 px-3 py-1.5 text-xs text-neutral-400">
        <span className="font-mono text-neutral-300">{path}</span>
        {dirty && <span className="text-amber-400">● 未保存</span>}
        {meta && (
          <span className="text-neutral-600">
            {meta.encoding} · {(meta.size / 1024).toFixed(1)} KB
          </span>
        )}
        <div className="flex-1" />
        <button
          className="rounded bg-blue-600 px-3 py-0.5 text-white hover:bg-blue-500 disabled:opacity-50"
          disabled={!dirty || saving}
          onClick={() => void save()}
        >
          {saving ? "保存中…" : "保存 (Ctrl+S)"}
        </button>
        {onClose && (
          <button className="rounded px-2 hover:bg-neutral-800" onClick={onClose}>
            ✕
          </button>
        )}
      </div>
      <div ref={hostRef} className="min-h-0 flex-1 overflow-auto" />
    </div>
  );
}
