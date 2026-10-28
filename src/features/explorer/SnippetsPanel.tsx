import { useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { assetApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { insertSnippet, executeSnippet, terminalTarget, type Snippet } from "./snippetInsert";
import { LoadError } from "../../ui/LoadError";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { useSnippetVariablePrompt } from "./SnippetVariablePrompt";
import { RunbookPanel } from "./RunbookPanel";
import {
  analyzeSnippetTemplate,
  renderSnippetTemplate,
  type SnippetAsset,
} from "./snippetVariables";
import {
  IconCommand,
  IconEdit,
  IconInsert,
  IconLoader,
  IconPlay,
  IconPlus,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

function activeTerminalAssetId(): string | undefined {
  const state = useUi.getState();
  const workspace =
    state.workspaces.find((candidate) => candidate.id === state.activeWorkspaceId) ??
    state.workspaces[state.workspaces.length - 1];
  const pane = workspace?.panes.find((candidate) => candidate.id === workspace.activePaneId) ?? workspace?.panes[0];
  const tab = pane?.tabs.find((candidate) => candidate.id === pane.activeTabId);
  return tab?.assetId ?? workspace?.assetId;
}

export function SnippetsPanel({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const { promptForValues, promptElement, prompting } = useSnippetVariablePrompt();
  const [editing, setEditing] = useState<Snippet | "new" | null>(null);
  const [runbookOpen, setRunbookOpen] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const modalRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const layer = useOverlayFocus(true, modalRef);
  // Insert/execute act on the active terminal; without one they are disabled with the reason instead of failing on click.
  const blockedReason = useUi((s) => {
    void s.workspaces; void s.activeWorkspaceId;
    const target = terminalTarget();
    return "blocked" in target ? target.blocked : null;
  });

  const snippets = useQuery({
    queryKey: ["snippets"],
    queryFn: () => assetApi.snippetList(),
    refetchOnWindowFocus: false,
  });

  const refresh = () => void qc.invalidateQueries({ queryKey: ["snippets"] });

  const prepareBody = async (s: Snippet): Promise<string | null> => {
    try {
      const analysis = analyzeSnippetTemplate(s.body);
      if (!analysis.hasVariables) return s.body;
      if (analysis.unknownVariables.length > 0) {
        pushToast("error", `片段包含未识别的变量：${analysis.unknownVariables.map((name) => `「${name}」`).join("、")}`);
        return null;
      }
      let asset: SnippetAsset | undefined;
      const assetId = analysis.usesAsset ? activeTerminalAssetId() : undefined;
      if (assetId) {
        try {
          asset = await assetApi.get(assetId);
        } catch (cause) {
          pushToast("error", `无法读取资产信息：${describeError(cause)}`);
          return null;
        }
      }
      const values = await promptForValues(`填写片段变量 · ${s.name}`, analysis.variables);
      if (values === null) return null;
      return renderSnippetTemplate(s.body, values, asset);
    } catch (cause) {
      pushToast("error", `无法解析片段变量：${describeError(cause)}`);
      return null;
    }
  };

  const run = async (s: Snippet, action: "insert" | "execute") => {
    if (prompting) return;
    const epoch = useUi.getState().accountEpoch;
    const body = await prepareBody(s);
    if (body === null || useUi.getState().accountEpoch !== epoch) return;
    const rendered = { ...s, body };
    if (await (action === "insert" ? insertSnippet(rendered) : executeSnippet(rendered))) onClose();
  };

  const insert = async (s: Snippet) => run(s, "insert");

  const execute = async (s: Snippet) => run(s, "execute");

  const remove = async (s: Snippet) => {
    if (deletingId) return;
    // The confirmation is account-scoped: a late resolve after an account switch must not
    // delete the snippet under the new account.
    const epoch = useUi.getState().accountEpoch;
    const ok = await ask(`删除片段「${s.name}」？删除后无法恢复。`, { kind: "warning" });
    if (!ok || useUi.getState().accountEpoch !== epoch) return;
    setDeletingId(s.id);
    try {
      await assetApi.snippetDelete(s.id);
      pushToast("info", `已删除片段「${s.name}」`);
      refresh();
    } catch (e) {
      pushToast("error", `无法删除片段：${describeError(e)}`);
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

  if (runbookOpen) {
    return (
      <RunbookPanel
        snippets={snippets.data ?? []}
        onBack={() => setRunbookOpen(false)}
        onClose={onClose}
      />
    );
  }

  const needle = query.trim().toLowerCase();
  const visible = (snippets.data ?? []).filter(
    (s) => !needle || s.name.toLowerCase().includes(needle) || s.body.toLowerCase().includes(needle),
  );

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal flex nx-modal-md max-h-[80vh] flex-col"
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
          <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" title="新建片段" onClick={() => setEditing("new")}>
            <IconPlus size={12} />
            新建片段
          </button>
          <button
            type="button"
            className="nx-btn nx-btn-sm"
            title="按顺序在多台主机上运行多个片段或命令"
            onClick={() => setRunbookOpen(true)}
          >
            <IconPlay size={12} />
            执行流程
          </button>
        </div>
        <div className="nx-modal-body flex min-h-[320px] flex-1 flex-col overflow-y-auto">
          <p className="nx-hint mb-2.5">
            「插入」将命令写入当前终端的输入行，不按回车；「执行」写入后立即回车运行。片段含换行或控制字符时，将先请求确认。
          </p>
          {blockedReason && (snippets.data?.length ?? 0) > 0 && (
            <p className="nx-hint mb-2.5" role="status">{blockedReason}，才能插入或执行片段。仍可新建、编辑或删除片段。</p>
          )}
          {(snippets.data?.length ?? 0) > 6 && (
            <input
              className="nx-input mb-2"
              placeholder="搜索片段名称或命令"
              aria-label="搜索片段"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          )}
          {snippets.isPending && (
            <div className="flex items-center gap-2 px-1 py-6 text-[12px] text-neutral-500">
              <IconLoader size={13} className="animate-spin" /> 正在加载片段…
            </div>
          )}
          {snippets.isError && (
            <LoadError title="片段加载失败" error={snippets.error} onRetry={() => void snippets.refetch()} />
          )}
          {snippets.data && snippets.data.length === 0 && (
            <div className="flex flex-1 flex-col items-center justify-center gap-3 py-8 text-center">
              <span className="nx-empty-icon">
                <IconCommand size={18} />
              </span>
              <div className="nx-hint">暂无命令片段。可将常用命令保存为片段，之后一键插入或执行。</div>
              <button type="button" className="nx-btn nx-btn-sm" onClick={() => setEditing("new")}>
                <IconPlus size={12} />
                新建第一个片段
              </button>
            </div>
          )}
          {snippets.data && snippets.data.length > 0 && visible.length === 0 && (
            <div className="nx-hint py-6 text-center">没有匹配的片段。</div>
          )}
          <div className="flex flex-col gap-1">
            {visible.map((s) => {
              const insertLabel = `插入片段「${s.name}」`;
              const executeLabel = `执行片段「${s.name}」`;
              const preview = s.body.split("\n")[0];
              return (
                <div key={s.id} data-snippet-row className="flex items-center gap-2 rounded-md border border-neutral-800/70 px-2.5 py-1.5">
                  <IconCommand size={13} className="shrink-0 text-neutral-500" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[12.5px] text-neutral-200">{s.name}</div>
                    <div className="truncate font-mono text-[11px] text-neutral-500" title={s.body}>
                      {preview}
                      {s.body.includes("\n") ? " …" : ""}
                    </div>
                  </div>
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                    title={blockedReason ?? "插入到当前终端"}
                    aria-label={insertLabel}
                    disabled={deletingId === s.id || blockedReason !== null}
                    onClick={() => void insert(s)}
                  >
                    <IconInsert size={11} />
                    插入
                  </button>
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                    title={blockedReason ?? "在当前终端执行"}
                    aria-label={executeLabel}
                    disabled={deletingId === s.id || blockedReason !== null}
                    onClick={() => void execute(s)}
                  >
                    <IconPlay size={11} />
                    执行
                  </button>
                  <button
                    className="nx-icon-btn nx-icon-btn-sm shrink-0"
                    title="编辑片段"
                    aria-label={`编辑片段「${s.name}」`}
                    disabled={deletingId === s.id}
                    onClick={() => setEditing(s)}
                  >
                    <IconEdit size={12} />
                  </button>
                  <button
                    className="nx-icon-btn nx-icon-btn-sm is-danger shrink-0"
                    title="删除片段"
                    aria-label={`删除片段「${s.name}」`}
                    disabled={deletingId === s.id}
                    onClick={() => void remove(s)}
                  >
                    {deletingId === s.id ? <IconLoader size={12} className="animate-spin" /> : <IconTrash size={12} />}
                  </button>
                </div>
              );
            })}
          </div>
        </div>
        <div className="nx-modal-footer shrink-0">
          <div className="nx-spacer" />
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
      {promptElement}
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
      pushToast("success", initial ? "已保存片段" : "已创建片段");
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
        className="nx-modal flex nx-modal-md flex-col"
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
              rows={8}
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="docker ps --format '{{.Names}}'"
              aria-describedby={hintId}
            />
            <div id={hintId} className="nx-hint mt-1.5 leading-relaxed">
              <div>
                可以用变量，运行时会让你填写：<code>{"{{service}}"}</code> 必填，<code>{"{{service=nginx}}"}</code> 带默认值，<code>{"{{service|multiline}}"}</code> 多行输入。
              </div>
              <div className="mt-1">
                自动填入当前主机信息：<code>{"{{asset.name}}"}</code> 名称、<code>{"{{asset.host}}"}</code> 地址、<code>{"{{asset.tags}}"}</code> 标签。
              </div>
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
