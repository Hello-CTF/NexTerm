import { useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { assetApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { insertSnippet, executeSnippet, isMultiStep, terminalTarget, type Snippet } from "./snippetInsert";
import type { SnippetStep } from "../../ipc/types";
import { LoadError } from "../../ui/LoadError";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { useSnippetVariablePrompt } from "./SnippetVariablePrompt";
import { MultiHostRunPanel } from "./MultiHostRunPanel";
import {
  analyzeSnippetSteps,
  renderSnippetTemplate,
  type SnippetAsset,
} from "./snippetVariables";
import {
  IconCommand,
  IconEdit,
  IconInsert,
  IconLoader,
  IconArrowDown,
  IconArrowUp,
  IconPlay,
  IconPlus,
  IconServer,
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
  const [multiHostSnippet, setMultiHostSnippet] = useState<Snippet | null>(null);
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

  // A failed lookup only hides the notice.
  const migration = useQuery({
    queryKey: ["snippet-migration-status"],
    queryFn: () => assetApi.snippetMigrationStatus(),
    refetchOnWindowFocus: false,
    retry: false,
  });
  const unmigrated = migration.data?.unmigratedRunbookTemplates ?? 0;

  const refresh = () => void qc.invalidateQueries({ queryKey: ["snippets"] });

  // Variables are asked for once; every step of a multi-step snippet renders with the same values.
  const prepareSnippet = async (s: Snippet): Promise<Snippet | null> => {
    try {
      const multi = isMultiStep(s);
      const commands = multi ? (s.steps ?? []).map((step) => step.command) : [s.body];
      const analysis = analyzeSnippetSteps(commands);
      if (!analysis.hasVariables) return s;
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
      if (!multi) return { ...s, body: renderSnippetTemplate(s.body, values, asset) };
      const steps = (s.steps ?? []).map((step) => ({ ...step, command: renderSnippetTemplate(step.command, values, asset) }));
      return { ...s, steps, body: steps.map((step) => step.command).join("\n") };
    } catch (cause) {
      pushToast("error", `无法解析片段变量：${describeError(cause)}`);
      return null;
    }
  };

  const run = async (s: Snippet, action: "insert" | "execute") => {
    if (prompting) return;
    const epoch = useUi.getState().accountEpoch;
    const rendered = await prepareSnippet(s);
    if (rendered === null || useUi.getState().accountEpoch !== epoch) return;
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

  if (multiHostSnippet) {
    return (
      <MultiHostRunPanel
        snippet={multiHostSnippet}
        onBack={() => setMultiHostSnippet(null)}
        onClose={onClose}
      />
    );
  }

  const needle = query.trim().toLowerCase();
  const visible = (snippets.data ?? []).filter(
    (s) =>
      !needle ||
      s.name.toLowerCase().includes(needle) ||
      s.body.toLowerCase().includes(needle) ||
      (s.description ?? "").toLowerCase().includes(needle),
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
        </div>
        <div className="nx-modal-body flex min-h-[320px] flex-1 flex-col overflow-y-auto">
          <p className="nx-hint mb-2.5">插入：只写入不回车；执行：写入并回车。含换行或控制字符的片段会先确认。</p>
          {unmigrated > 0 && (
            <p className="nx-hint mb-2.5" role="status" data-testid="snippet-migration-notice">
              有 {unmigrated} 个旧版执行流程模板未能迁移为片段，内容仍保留在本机数据库中。
            </p>
          )}
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
            <LoadError title="未能加载片段" error={snippets.error} onRetry={() => void snippets.refetch()} />
          )}
          {snippets.data && snippets.data.length === 0 && (
            <div className="flex flex-1 flex-col items-center justify-center gap-3 py-8 text-center">
              <span className="nx-empty-icon">
                <IconCommand size={18} />
              </span>
              <div className="nx-hint">暂无命令片段。可将常用命令保存为片段，之后可直接插入或执行。</div>
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
              const multi = isMultiStep(s);
              const multiHostLabel = `在多台主机执行片段「${s.name}」`;
              const preview = (multi ? s.steps?.[0]?.command ?? s.body : s.body).split("\n")[0];
              return (
                <div key={s.id} data-snippet-row className="flex items-center gap-2 rounded-md border border-neutral-800/70 px-2.5 py-1.5">
                  <IconCommand size={13} className="shrink-0 text-neutral-500" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-[12.5px] text-neutral-200">{s.name}</span>
                      {multi && <span className="nx-badge shrink-0">{s.steps?.length ?? 0} 步</span>}
                    </div>
                    {s.description && <div className="truncate text-[11px] text-neutral-400" title={s.description}>{s.description}</div>}
                    <div className="truncate font-mono text-[11px] text-neutral-500" title={s.body}>
                      {preview}
                      {multi || s.body.includes("\n") ? " …" : ""}
                    </div>
                  </div>
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                    title={multi ? "多步片段只能执行" : (blockedReason ?? "插入到当前终端")}
                    aria-label={insertLabel}
                    disabled={deletingId === s.id || blockedReason !== null || multi}
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
                    className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                    title="在多台主机执行…"
                    aria-label={multiHostLabel}
                    disabled={deletingId === s.id}
                    onClick={() => setMultiHostSnippet(s)}
                  >
                    <IconServer size={11} />
                    多主机…
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

type EditorStep = SnippetStep;

function newStepId(): string {
  return globalThis.crypto.randomUUID();
}

function blankStep(command = ""): EditorStep {
  return { id: newStepId(), name: "", command };
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
  const initialSteps = initial?.steps ?? [];
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [body, setBody] = useState(initial?.body ?? "");
  const [multi, setMulti] = useState(initialSteps.length > 0);
  // Existing steps keep their ids and any fields the editor does not show (such as a step description).
  const [steps, setSteps] = useState<EditorStep[]>(() => initialSteps.map((step) => ({ ...step })));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const hintId = useId();
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => nameInputRef.current,
  });

  const dirty =
    name !== (initial?.name ?? "") ||
    (multi && description !== (initial?.description ?? "")) ||
    multi !== (initialSteps.length > 0) ||
    (multi ? JSON.stringify(steps) !== JSON.stringify(initialSteps) : body !== (initial?.body ?? ""));

  const updateStep = (id: string, patch: Partial<EditorStep>) =>
    setSteps((current) => current.map((step) => (step.id === id ? { ...step, ...patch } : step)));
  const moveStep = (index: number, offset: -1 | 1) =>
    setSteps((current) => {
      const destination = index + offset;
      if (destination < 0 || destination >= current.length) return current;
      const next = [...current];
      [next[index], next[destination]] = [next[destination], next[index]];
      return next;
    });

  const toMulti = () => {
    setSteps((current) => (current.length > 0 ? current : [blankStep(body)]));
    setMulti(true);
  };
  const toSingle = () => {
    if (steps.length > 0) setBody(steps.map((step) => step.command).join("\n"));
    setMulti(false);
  };

  const valid = name.trim() !== "" && (multi ? steps.length > 0 && steps.every((step) => step.command.trim() !== "") : body.trim() !== "");

  const requestClose = async () => {
    if (saving) return;
    if (
      dirty &&
      !(await ask("当前修改尚未保存，关闭编辑器将丢弃这些改动。确定要继续？", {
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
    if (!valid) {
      setError(multi ? "名称不能为空，且每一步都要填写命令" : "名称和内容都不能为空");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      // An omitted description is stored as empty, so it is only sent when there is one.
      // Single-step snippets cannot edit the description (it only syncs with multi-step snippets);
      // an existing one is sent back unchanged.
      const cleanDescription = (multi ? description : initial?.description ?? "").trim();
      const descriptionPatch = cleanDescription ? { description: cleanDescription } : {};
      if (multi) {
        const cleanSteps = steps.map((step) => ({ ...step, name: step.name.trim() }));
        const joined = cleanSteps.map((step) => step.command).join("\n");
        if (initial) {
          await assetApi.snippetUpdate(initial.id, n, joined, { ...descriptionPatch, steps: cleanSteps });
        } else {
          await assetApi.snippetCreate(n, joined, undefined, undefined, { ...descriptionPatch, steps: cleanSteps });
        }
      } else if (initial) {
        await (cleanDescription
          ? assetApi.snippetUpdate(initial.id, n, body, descriptionPatch)
          : assetApi.snippetUpdate(initial.id, n, body));
      } else {
        await (cleanDescription
          ? assetApi.snippetCreate(n, body, undefined, undefined, descriptionPatch)
          : assetApi.snippetCreate(n, body));
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
        className="nx-modal flex nx-modal-md max-h-[88dvh] flex-col"
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
            <label className="nx-label" htmlFor="snippet-name">名称</label>
            <input
              id="snippet-name"
              ref={nameInputRef}
              className="nx-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="看容器状态"
            />
          </div>
          {multi ? (
            <div className="nx-form-row">
              <label className="nx-label" htmlFor="snippet-description">说明（可选）</label>
              <input
                id="snippet-description"
                className="nx-input"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>
          ) : initial?.description ? (
            <div className="nx-form-row">
              <span className="nx-label">说明</span>
              <div data-testid="snippet-description-readonly" className="text-[12px] text-neutral-400">{initial.description}</div>
            </div>
          ) : null}
          <div className="nx-form-row">
            <div className="flex items-center gap-2">
              <span className="nx-label">{multi ? "步骤" : "内容"}</span>
              <div className="nx-spacer" />
              <button type="button" className="nx-btn nx-btn-sm" onClick={multi ? toSingle : toMulti}>
                {multi ? "改为单条" : "改为多步"}
              </button>
            </div>
            {!multi ? (
              <textarea
                id="snippet-body"
                className="nx-textarea font-mono text-[11.5px]"
                rows={8}
                value={body}
                onChange={(e) => setBody(e.target.value)}
                placeholder="docker ps --format '{{.Names}}'"
                aria-label="内容"
                aria-describedby={hintId}
              />
            ) : (
              <div className="mt-1.5 flex flex-col gap-2">
                {steps.map((step, index) => (
                  <div key={step.id} className="rounded border border-neutral-800 p-2.5">
                    <div className="mb-2 flex flex-wrap items-center gap-2">
                      <span className="nx-badge">{index + 1}</span>
                      <input
                        className="nx-input min-w-40 flex-1"
                        aria-label={`步骤 ${index + 1} 名称`}
                        placeholder="步骤名称（可选）"
                        value={step.name}
                        onChange={(e) => updateStep(step.id, { name: e.target.value })}
                      />
                      <button type="button" className="nx-icon-btn nx-icon-btn-sm" disabled={index === 0} aria-label={`上移步骤 ${index + 1}`} title="上移" onClick={() => moveStep(index, -1)}><IconArrowUp size={12} /></button>
                      <button type="button" className="nx-icon-btn nx-icon-btn-sm" disabled={index === steps.length - 1} aria-label={`下移步骤 ${index + 1}`} title="下移" onClick={() => moveStep(index, 1)}><IconArrowDown size={12} /></button>
                      <button type="button" className="nx-icon-btn nx-icon-btn-sm is-danger" aria-label={`删除步骤 ${index + 1}`} title="删除步骤" onClick={() => setSteps((current) => current.filter((candidate) => candidate.id !== step.id))}><IconTrash size={12} /></button>
                    </div>
                    <textarea
                      className="nx-textarea font-mono text-[11.5px]"
                      rows={2}
                      aria-label={`步骤 ${index + 1} 命令`}
                      aria-describedby={hintId}
                      value={step.command}
                      onChange={(e) => updateStep(step.id, { command: e.target.value })}
                    />
                    <label className="mt-1.5 flex items-center gap-1.5 text-[11px] text-neutral-400">
                      <input type="checkbox" className="accent-blue-500" aria-label={`步骤 ${index + 1} 执行前确认`} checked={step.confirm === true} onChange={(e) => updateStep(step.id, { confirm: e.target.checked || undefined })} />
                      执行前确认
                    </label>
                    <label className="mt-1 flex items-center gap-1.5 text-[11px] text-neutral-400">
                      <input type="checkbox" className="accent-blue-500" aria-label={`步骤 ${index + 1} 启用脚本交互指令`} checked={step.interactive === true} onChange={(e) => updateStep(step.id, { interactive: e.target.checked || undefined })} />
                      启用脚本交互指令（仅多主机执行；开启后该步骤输出中的 @NEXTERM: 行会被解释，并保持标准输入打开）
                    </label>
                  </div>
                ))}
                <div>
                  <button type="button" className="nx-btn nx-btn-sm" onClick={() => setSteps((current) => [...current, blankStep()])}>
                    <IconPlus size={12} />添加步骤
                  </button>
                </div>
              </div>
            )}
            <div id={hintId} className="nx-hint mt-1.5 leading-relaxed">
              <div>
                支持使用变量，运行时需填写：<code>{"{{service}}"}</code> 必填，<code>{"{{service=nginx}}"}</code> 或 <code>{"{{service:nginx}}"}</code> 带默认值，<code>{"{{service|multiline}}"}</code> 多行输入。
                {multi && "多步片段的所有步骤共用同一组变量，只填写一次。"}
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
            disabled={saving || !valid}
            onClick={() => void save()}
          >
            {saving ? "正在保存…" : "保存"}
          </button>
        </div>
      </div>
    </div>
  );
}
