import { useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { assetApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { insertSnippet, type Snippet } from "./snippetInsert";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import {
  IconCommand,
  IconEdit,
  IconLoader,
  IconPlay,
  IconPlus,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

export function SnippetsPanel({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const [editing, setEditing] = useState<Snippet | "new" | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const layer = useOverlayFocus(true, modalRef);

  const snippets = useQuery({
    queryKey: ["snippets"],
    queryFn: () => assetApi.snippetList(),
    refetchOnWindowFocus: false,
  });

  const refresh = () => void qc.invalidateQueries({ queryKey: ["snippets"] });

  const insert = async (s: Snippet) => {
    if (await insertSnippet(s)) onClose();
  };

  const remove = async (s: Snippet) => {
    if (deletingId) return;
    const ok = await ask(`删除片段「${s.name}」？此操作不可恢复。`, { kind: "warning" });
    if (!ok) return;
    setDeletingId(s.id);
    try {
      await assetApi.snippetDelete(s.id);
      pushToast("info", "已删除");
      refresh();
    } catch (e) {
      pushToast("error", `删除失败：${describeError(e)}`);
    } finally {
      setDeletingId(null);
    }
  };

  const onModalKeyDown = (event: ReactKeyboardEvent) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
      event.preventDefault();
      onClose();
      return;
    }
    trapOverlayTab(event, modalRef.current);
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[420px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">命令片段</span>
          <div className="nx-spacer" />
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="新建片段"
            onClick={() => setEditing("new")}
          >
            <IconPlus size={14} />
          </button>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          {snippets.isPending && (
            <div className="flex items-center gap-2 px-1 py-6 text-[12px] text-neutral-500">
              <IconLoader size={13} className="animate-spin" /> 正在加载片段…
            </div>
          )}
          {snippets.isError && (
            <div className="px-1 py-5 text-center">
              <div className="flex items-center justify-center gap-1.5 text-[12px] text-red-400">
                <IconXCircle size={13} /> 加载失败：{describeError(snippets.error)}
              </div>
              <button className="nx-btn nx-btn-outline nx-btn-sm mt-3" onClick={() => void snippets.refetch()}>
                重试
              </button>
            </div>
          )}
          {snippets.data && snippets.data.length === 0 && (
            <div className="nx-hint px-1 py-8 text-center">
              还没有片段 — 点右上角 + 新建一个。插入只把命令写进终端，含回车/换行或控制字符时会先确认。
            </div>
          )}
          {snippets.data?.map((s) => (
            <div key={s.id} className="nx-row nx-row-reserve-actions mb-0.5">
              <IconCommand size={13} className="shrink-0 text-neutral-500" />
              <span className="min-w-0 flex-1 truncate text-[12.5px]">{s.name}</span>
              <span className="nx-row-actions">
                <button
                  className="nx-icon-btn nx-icon-btn-sm"
                  title="插入到当前终端"
                  disabled={deletingId === s.id}
                  onClick={(e) => {
                    e.stopPropagation();
                    void insert(s);
                  }}
                >
                  <IconPlay size={12} />
                </button>
                <button
                  className="nx-icon-btn nx-icon-btn-sm"
                  title="编辑片段"
                  disabled={deletingId === s.id}
                  onClick={(e) => {
                    e.stopPropagation();
                    setEditing(s);
                  }}
                >
                  <IconEdit size={12} />
                </button>
                <button
                  className="nx-icon-btn nx-icon-btn-sm is-danger"
                  title="删除片段"
                  disabled={deletingId === s.id}
                  onClick={(e) => {
                    e.stopPropagation();
                    void remove(s);
                  }}
                >
                  {deletingId === s.id ? (
                    <IconLoader size={12} className="animate-spin" />
                  ) : (
                    <IconTrash size={12} />
                  )}
                </button>
              </span>
            </div>
          ))}
        </div>
        <div className="nx-modal-footer shrink-0">
          <span className="nx-hint mr-auto">插入 = 写入终端输入行；回车/换行或控制字符会先确认</span>
          <button className="nx-btn nx-btn-ghost" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
      {editing && (
        <SnippetEditor
          initial={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            refresh();
          }}
        />
      )}
    </div>
  );
}

function SnippetEditor({
  initial,
  onClose,
  onSaved,
}: {
  initial: Snippet | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const [name, setName] = useState(initial?.name ?? "");
  const [body, setBody] = useState(initial?.body ?? "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const nameId = useId();
  const bodyId = useId();
  const hintId = useId();
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => nameInputRef.current,
  });

  const dirty = name !== (initial?.name ?? "") || body !== (initial?.body ?? "");

  const requestClose = async () => {
    if (saving) return;
    if (
      dirty &&
      !(await ask("当前修改还没保存，关闭编辑器会丢掉这些改动，继续？", {
        title: "放弃未保存的修改",
        kind: "warning",
      }))
    ) {
      return;
    }
    onClose();
  };

  const save = async () => {
    if (saving) return;
    const n = name.trim();
    if (!n || !body.trim()) {
      setError("名称和内容都不能为空");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      if (initial) {
        await assetApi.snippetUpdate(initial.id, n, body);
      } else {
        await assetApi.snippetCreate(n, body);
      }
      pushToast("success", initial ? "已保存" : "已创建");
      onSaved();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setSaving(false);
    }
  };

  const onModalKeyDown = (event: ReactKeyboardEvent) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
      event.preventDefault();
      void requestClose();
      return;
    }
    trapOverlayTab(event, modalRef.current);
  };

  return (
    <div
      className="nx-overlay"
      onClick={(e) => {
        e.stopPropagation();
        void requestClose();
      }}
    >
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[420px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">
            {initial ? "编辑片段" : "新建片段"}
          </span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          <div className="nx-form-row">
            <label className="nx-label" htmlFor={nameId}>名称</label>
            <input
              id={nameId}
              ref={nameInputRef}
              className="nx-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="看容器状态"
            />
          </div>
          <div className="nx-form-row">
            <label className="nx-label" htmlFor={bodyId}>内容</label>
            <textarea
              id={bodyId}
              className="nx-textarea font-mono text-[11.5px]"
              rows={4}
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="docker ps --format '{{.Names}}'"
              aria-describedby={hintId}
            />
            <div id={hintId} className="nx-hint mt-1.5">
              ↳ 插入终端时只写入输入行，不会自动执行；含回车/换行或控制字符的片段插入前会再确认一次。
            </div>
          </div>
          {error && (
            <div className="mb-2 flex items-center gap-1.5 text-[12px] text-red-400">
              <IconXCircle size={13} /> {error}
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          <button className="nx-btn nx-btn-ghost" onClick={() => void requestClose()} disabled={saving}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={saving || !name.trim() || !body.trim()}
            onClick={() => void save()}
          >
            {saving ? "保存中…" : "保存"}
          </button>
        </div>
      </div>
    </div>
  );
}
