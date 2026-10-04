import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { assetApi, terminalApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  IconCommand,
  IconEdit,
  IconLoader,
  IconPlay,
  IconPlus,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

export interface Snippet {
  id: string;
  name: string;
  body: string;
  groupId: string | null;
  sort: number;
}

function activeTerminalTabId(): string | null {
  const st = useUi.getState();
  const ws =
    st.workspaces.find((w) => w.id === st.activeWorkspaceId) ??
    st.workspaces[st.workspaces.length - 1] ??
    null;
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tab = pane?.tabs.find((t) => t.id === pane.activeTabId) ?? null;
  if (!tab || tab.kind !== "terminal" || !tab.tabId) return null;
  return tab.tabId;
}

type InsertRisk = "none" | "execute" | "control";

function insertRisk(body: string): InsertRisk {
  for (const ch of body) {
    const c = ch.codePointAt(0) ?? 0;
    if (c === 0x0a || c === 0x0d) return "execute";
  }
  for (const ch of body) {
    const c = ch.codePointAt(0) ?? 0;
    if (c < 0x20 || (c >= 0x7f && c <= 0x9f)) return "control";
  }
  return "none";
}

export function SnippetsPanel({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const [editing, setEditing] = useState<Snippet | "new" | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const snippets = useQuery({
    queryKey: ["snippets"],
    queryFn: () => assetApi.snippetList(),
    refetchOnWindowFocus: false,
  });

  const refresh = () => void qc.invalidateQueries({ queryKey: ["snippets"] });

  const insert = async (s: Snippet) => {
    const tabId = activeTerminalTabId();
    if (!tabId) {
      pushToast("info", "请先在当前工作区打开一个终端，再插入片段");
      return;
    }
    const risk = insertRisk(s.body);
    if (risk === "execute") {
      const n = s.body.match(/[\r\n]/g)?.length ?? 0;
      const ok = await ask(
        `片段「${s.name}」包含 ${n} 处回车/换行：插入时每一处都会立即提交执行（相当于替你按回车）。\n仍要插入吗？`,
        { kind: "warning" },
      );
      if (!ok) return;
    } else if (risk === "control") {
      const ok = await ask(
        `片段「${s.name}」包含终端控制字符（Tab 补全 / DEL / Ctrl-C / Ctrl-D / ESC 序列等）：\n插入不会替你执行命令行，但 Tab 补全钩子本身就是 shell 代码（不需要回车就会运行），其他控制字符也可能触发 readline/终端绑定动作、中断前台进程或改变终端状态。\n仍要插入吗？`,
        { kind: "warning" },
      );
      if (!ok) return;
    }
    try {
      await terminalApi.write(tabId, new TextEncoder().encode(s.body));
      pushToast(
        "success",
        risk === "none"
          ? `已插入「${s.name}」 · 未执行，确认后回车运行`
          : risk === "execute"
            ? `已插入「${s.name}」 · 已按原样写入，其中回车/换行处已逐行执行`
            : `已插入「${s.name}」 · 已按原样写入，控制字符可能已改变终端状态`,
      );
      onClose();
    } catch (e) {
      pushToast("error", `插入失败：${describeError(e)}`);
    }
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

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[420px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">命令片段</span>
          <div className="nx-spacer" />
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="新建片段"
            onClick={() => setEditing("new")}
          >
            <IconPlus size={14} />
          </button>
        </div>
        <div className="nx-modal-body">
          {snippets.isPending && (
            <div className="flex items-center gap-2 px-1 py-6 text-[12px] text-neutral-500">
              <IconLoader size={13} className="animate-spin" /> 正在加载片段…
            </div>
          )}
          {snippets.isError && (
            <div className="px-1 py-5 text-center">
              <div className="flex items-center justify-center gap-1.5 text-[12px] text-red-400">
                <IconXCircle size={13} /> 加载失败:{describeError(snippets.error)}
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
            <div key={s.id} className="nx-row group mb-0.5">
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
        <div className="nx-modal-footer">
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

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[420px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">
            {initial ? "编辑片段" : "新建片段"}
          </span>
        </div>
        <div className="nx-modal-body">
          <div className="nx-form-row">
            <label className="nx-label">名称</label>
            <input
              className="nx-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="看容器状态"
            />
          </div>
          <div className="nx-form-row">
            <label className="nx-label">内容</label>
            <textarea
              className="nx-textarea font-mono text-[11.5px]"
              rows={4}
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="docker ps --format '{{.Names}}'"
            />
            <div className="nx-hint mt-1.5">
              ↳ 插入终端时只写入输入行，不会自动执行；含回车/换行或控制字符的片段插入前会再确认一次。
            </div>
          </div>
          {error && (
            <div className="mb-2 flex items-center gap-1.5 text-[12px] text-red-400">
              <IconXCircle size={13} /> {error}
            </div>
          )}
        </div>
        <div className="nx-modal-footer">
          <button className="nx-btn nx-btn-ghost" onClick={onClose} disabled={saving}>
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
