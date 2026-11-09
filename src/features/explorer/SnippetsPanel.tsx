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
  DEFAULT_WAIT_TIMEOUT_MS,
  MAX_WAIT_FOR_LENGTH,
  MAX_WAIT_TIMEOUT_MS,
  MIN_WAIT_TIMEOUT_MS,
  waitPatternError,
} from "./snippetWaitFor";
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
import { t } from "../../i18n";

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
        pushToast("error", t("片段包含未识别的变量：{join}", { join: analysis.unknownVariables.map((name) => `「${name}」`).join("、") }));
        return null;
      }
      let asset: SnippetAsset | undefined;
      const assetId = analysis.usesAsset ? activeTerminalAssetId() : undefined;
      if (assetId) {
        try {
          asset = await assetApi.get(assetId);
        } catch (cause) {
          pushToast("error", t("无法读取资产信息：{value}", { value: describeError(cause) }));
          return null;
        }
      }
      const values = await promptForValues(t("填写片段变量 · {name}", { name: s.name }), analysis.variables);
      if (values === null) return null;
      if (!multi) return { ...s, body: renderSnippetTemplate(s.body, values, asset) };
      const steps = (s.steps ?? []).map((step) => ({ ...step, command: renderSnippetTemplate(step.command, values, asset) }));
      return { ...s, steps, body: steps.map((step) => step.command).join("\n") };
    } catch (cause) {
      pushToast("error", t("无法解析片段变量：{value}", { value: describeError(cause) }));
      return null;
    }
  };

  const run = async (s: Snippet, action: "insert" | "execute") => {
    if (prompting) return;
    const epoch = useUi.getState().accountEpoch;
    const rendered = await prepareSnippet(s);
    if (rendered === null || useUi.getState().accountEpoch !== epoch) return;
    // A run that waits for output closes the panel when its first wait starts, so the terminal stays visible.
    if (await (action === "insert" ? insertSnippet(rendered) : executeSnippet(rendered, { onWaiting: onClose }))) onClose();
  };

  const insert = async (s: Snippet) => run(s, "insert");

  const execute = async (s: Snippet) => run(s, "execute");

  const remove = async (s: Snippet) => {
    if (deletingId) return;
    // The confirmation is account-scoped: a late resolve after an account switch must not
    // delete the snippet under the new account.
    const epoch = useUi.getState().accountEpoch;
    const ok = await ask(t("删除片段「{name}」？删除后无法恢复。", { name: s.name }), { kind: "warning" });
    if (!ok || useUi.getState().accountEpoch !== epoch) return;
    setDeletingId(s.id);
    try {
      await assetApi.snippetDelete(s.id);
      pushToast("info", t("已删除片段「{name}」", { name: s.name }));
      refresh();
    } catch (e) {
      pushToast("error", t("无法删除片段：{value}", { value: describeError(e) }));
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
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">{t("命令片段")}</span>
          <div className="nx-spacer" />
          <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" title={t("新建片段")} onClick={() => setEditing("new")}>
            <IconPlus size={12} />
            {t("新建片段")}
          </button>
        </div>
        <div className="nx-modal-body flex min-h-[320px] flex-1 flex-col overflow-y-auto">
          <p className="nx-hint mb-2.5">{t("插入：只写入不回车；执行：写入并回车。含换行或控制字符的片段会先确认。")}</p>
          {unmigrated > 0 && (
            <p className="nx-hint mb-2.5" role="status" data-testid="snippet-migration-notice">
              {t("有 {unmigrated} 个旧版执行流程模板未能迁移为片段，内容仍保留在本机数据库中。", { unmigrated })}
            </p>
          )}
          {blockedReason && (snippets.data?.length ?? 0) > 0 && (
            <p className="nx-hint mb-2.5" role="status">{t("{blockedReason}，才能插入或执行片段。仍可新建、编辑或删除片段。", { blockedReason })}</p>
          )}
          {(snippets.data?.length ?? 0) > 6 && (
            <input
              className="nx-input mb-2"
              placeholder={t("搜索片段名称或命令")}
              aria-label={t("搜索片段")}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          )}
          {snippets.isPending && (
            <div className="flex items-center gap-2 px-1 py-6 text-[12px] text-neutral-500">
              <IconLoader size={13} className="animate-spin" /> {t("正在加载片段…")}
            </div>
          )}
          {snippets.isError && (
            <LoadError title={t("未能加载片段")} error={snippets.error} onRetry={() => void snippets.refetch()} />
          )}
          {snippets.data && snippets.data.length === 0 && (
            <div className="flex flex-1 flex-col items-center justify-center gap-3 py-8 text-center">
              <span className="nx-empty-icon">
                <IconCommand size={18} />
              </span>
              <div className="nx-hint">{t("暂无命令片段。可将常用命令保存为片段，之后可直接插入或执行。")}</div>
              <button type="button" className="nx-btn nx-btn-sm" onClick={() => setEditing("new")}>
                <IconPlus size={12} />
                {t("新建第一个片段")}
              </button>
            </div>
          )}
          {snippets.data && snippets.data.length > 0 && visible.length === 0 && (
            <div className="nx-hint py-6 text-center">{t("没有匹配的片段。")}</div>
          )}
          <div className="flex flex-col gap-1">
            {visible.map((s) => {
              const insertLabel = t("插入片段「{name}」", { name: s.name });
              const executeLabel = t("执行片段「{name}」", { name: s.name });
              const multi = isMultiStep(s);
              const multiHostLabel = t("在多台主机执行片段「{name}」", { name: s.name });
              const preview = (multi ? s.steps?.[0]?.command ?? s.body : s.body).split("\n")[0];
              return (
                <div key={s.id} data-snippet-row className="flex items-center gap-2 rounded-md border border-neutral-800/70 px-2.5 py-1.5">
                  <IconCommand size={13} className="shrink-0 text-neutral-500" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-[12.5px] text-neutral-200">{s.name}</span>
                      {multi && <span className="nx-badge shrink-0">{t("{value} 步", { value: s.steps?.length ?? 0 })}</span>}
                    </div>
                    {s.description && <div className="truncate text-[11px] text-neutral-400" title={s.description}>{s.description}</div>}
                    <div className="truncate font-mono text-[11px] text-neutral-500" title={s.body}>
                      {preview}
                      {multi || s.body.includes("\n") ? " …" : ""}
                    </div>
                  </div>
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                    title={multi ? t("多步片段只能执行") : (blockedReason ?? t("插入到当前终端"))}
                    aria-label={insertLabel}
                    disabled={deletingId === s.id || blockedReason !== null || multi}
                    onClick={() => void insert(s)}
                  >
                    <IconInsert size={11} />
                    {t("插入")}
                  </button>
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                    title={blockedReason ?? t("在当前终端执行")}
                    aria-label={executeLabel}
                    disabled={deletingId === s.id || blockedReason !== null}
                    onClick={() => void execute(s)}
                  >
                    <IconPlay size={11} />
                    {t("执行")}
                  </button>
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                    title={t("在多台主机执行…")}
                    aria-label={multiHostLabel}
                    disabled={deletingId === s.id}
                    onClick={() => setMultiHostSnippet(s)}
                  >
                    <IconServer size={11} />
                    {t("多主机…")}
                  </button>
                  <button
                    className="nx-icon-btn nx-icon-btn-sm shrink-0"
                    title={t("编辑片段")}
                    aria-label={t("编辑片段「{name}」", { name: s.name })}
                    disabled={deletingId === s.id}
                    onClick={() => setEditing(s)}
                  >
                    <IconEdit size={12} />
                  </button>
                  <button
                    className="nx-icon-btn nx-icon-btn-sm is-danger shrink-0"
                    title={t("删除片段")}
                    aria-label={t("删除片段「{name}」", { name: s.name })}
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
            {t("关闭")}
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

// The editor keeps a wait being edited as typed; saving drops a blank pattern together with its options.
function cleanWait(step: EditorStep): EditorStep {
  const { waitFor, waitForRegex, timeoutMs, ...rest } = step;
  if (!waitFor || waitFor.trim() === "") return rest;
  return {
    ...rest,
    waitFor,
    ...(waitForRegex ? { waitForRegex: true } : {}),
    ...(timeoutMs && timeoutMs !== DEFAULT_WAIT_TIMEOUT_MS ? { timeoutMs } : {}),
  };
}

function stepWaitProblem(step: EditorStep, index: number): string | null {
  const pattern = step.waitFor ?? "";
  if (pattern.trim() === "") return null;
  if (pattern.length > MAX_WAIT_FOR_LENGTH) {
    return t("第 {value} 步的等待条件不能超过 {max} 个字符", { value: index + 1, max: MAX_WAIT_FOR_LENGTH });
  }
  if (step.timeoutMs !== undefined && (!Number.isFinite(step.timeoutMs) || step.timeoutMs < MIN_WAIT_TIMEOUT_MS || step.timeoutMs > MAX_WAIT_TIMEOUT_MS)) {
    return t("第 {value} 步的超时须在 {min} 到 {max} 秒之间", { value: index + 1, min: MIN_WAIT_TIMEOUT_MS / 1000, max: MAX_WAIT_TIMEOUT_MS / 1000 });
  }
  const regexError = waitPatternError(pattern, step.waitForRegex === true);
  return regexError === null ? null : t("第 {value} 步的正则表达式无效：{error}", { value: index + 1, error: regexError });
}

function StepWaitFields({
  step,
  index,
  problem,
  onChange,
}: {
  step: EditorStep;
  index: number;
  problem: string | null;
  onChange: (patch: Partial<EditorStep>) => void;
}) {
  const hasWait = (step.waitFor ?? "") !== "";
  const seconds = (step.timeoutMs ?? DEFAULT_WAIT_TIMEOUT_MS) / 1000;
  return (
    <div className="mt-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <input
          className="nx-input min-w-40 flex-1 font-mono text-[11.5px]"
          aria-label={t("步骤 {value} 等待输出", { value: index + 1 })}
          placeholder={index === 0 ? t("执行前等待输出（可选），如 $ ") : t("等待上一步输出（可选），如 password:")}
          maxLength={MAX_WAIT_FOR_LENGTH}
          value={step.waitFor ?? ""}
          onChange={(e) => onChange({ waitFor: e.target.value })}
        />
        <label className="flex items-center gap-1.5 text-[11px] text-neutral-400">
          <input
            type="checkbox"
            className="accent-blue-500"
            aria-label={t("步骤 {value} 等待条件使用正则表达式", { value: index + 1 })}
            checked={step.waitForRegex === true}
            disabled={!hasWait}
            onChange={(e) => onChange({ waitForRegex: e.target.checked || undefined })}
          />
          {t("正则")}
        </label>
        <label className="flex items-center gap-1.5 text-[11px] text-neutral-400">
          {t("超时")}
          <input
            type="number"
            className="nx-input w-16"
            aria-label={t("步骤 {value} 等待超时（秒）", { value: index + 1 })}
            min={MIN_WAIT_TIMEOUT_MS / 1000}
            max={MAX_WAIT_TIMEOUT_MS / 1000}
            step={1}
            disabled={!hasWait}
            value={Number.isFinite(seconds) ? seconds : ""}
            onChange={(e) => {
              const value = e.target.valueAsNumber;
              onChange({ timeoutMs: Number.isFinite(value) ? Math.round(value * 1000) : Number.NaN });
            }}
          />
          {t("秒")}
        </label>
      </div>
      {problem && <div role="alert" className="mt-1 text-[11px] text-red-400">{problem}</div>}
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

  const waitProblems = multi ? steps.map(stepWaitProblem) : [];
  const valid =
    name.trim() !== "" &&
    (multi ? steps.length > 0 && steps.every((step) => step.command.trim() !== "") && waitProblems.every((problem) => problem === null) : body.trim() !== "");

  const requestClose = async () => {
    if (saving) return;
    if (
      dirty &&
      !(await ask(t("当前修改尚未保存，关闭编辑器将丢弃这些改动。确定要继续？"), {
        title: t("放弃未保存的修改"),
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
      setError(
        multi
          ? (waitProblems.find((problem) => problem !== null) ?? t("名称不能为空，且每一步都要填写命令"))
          : t("名称和内容都不能为空"),
      );
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
        const cleanSteps = steps.map((step) => cleanWait({ ...step, name: step.name.trim() }));
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
      pushToast("success", initial ? t("已保存片段") : t("已创建片段"));
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
            {initial ? t("编辑片段") : t("新建片段")}
          </span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          <div className="nx-form-row">
            <label className="nx-label" htmlFor="snippet-name">{t("名称")}</label>
            <input
              id="snippet-name"
              ref={nameInputRef}
              className="nx-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("看容器状态")}
            />
          </div>
          {multi ? (
            <div className="nx-form-row">
              <label className="nx-label" htmlFor="snippet-description">{t("说明（可选）")}</label>
              <input
                id="snippet-description"
                className="nx-input"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>
          ) : initial?.description ? (
            <div className="nx-form-row">
              <span className="nx-label">{t("说明")}</span>
              <div data-testid="snippet-description-readonly" className="text-[12px] text-neutral-400">{initial.description}</div>
            </div>
          ) : null}
          <div className="nx-form-row">
            <div className="flex items-center gap-2">
              <span className="nx-label">{multi ? t("步骤") : t("内容")}</span>
              <div className="nx-spacer" />
              <button type="button" className="nx-btn nx-btn-sm" onClick={multi ? toSingle : toMulti}>
                {multi ? t("改为单条") : t("改为多步")}
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
                aria-label={t("内容")}
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
                        aria-label={t("步骤 {value} 名称", { value: index + 1 })}
                        placeholder={t("步骤名称（可选）")}
                        value={step.name}
                        onChange={(e) => updateStep(step.id, { name: e.target.value })}
                      />
                      <button type="button" className="nx-icon-btn nx-icon-btn-sm" disabled={index === 0} aria-label={t("上移步骤 {value}", { value: index + 1 })} title={t("上移")} onClick={() => moveStep(index, -1)}><IconArrowUp size={12} /></button>
                      <button type="button" className="nx-icon-btn nx-icon-btn-sm" disabled={index === steps.length - 1} aria-label={t("下移步骤 {value}", { value: index + 1 })} title={t("下移")} onClick={() => moveStep(index, 1)}><IconArrowDown size={12} /></button>
                      <button type="button" className="nx-icon-btn nx-icon-btn-sm is-danger" aria-label={t("删除步骤 {value}", { value: index + 1 })} title={t("删除步骤")} onClick={() => setSteps((current) => current.filter((candidate) => candidate.id !== step.id))}><IconTrash size={12} /></button>
                    </div>
                    <textarea
                      className="nx-textarea font-mono text-[11.5px]"
                      rows={2}
                      aria-label={t("步骤 {value} 命令", { value: index + 1 })}
                      aria-describedby={hintId}
                      value={step.command}
                      onChange={(e) => updateStep(step.id, { command: e.target.value })}
                    />
                    <label className="mt-1.5 flex items-center gap-1.5 text-[11px] text-neutral-400">
                      <input type="checkbox" className="accent-blue-500" aria-label={t("步骤 {value} 执行前确认", { value: index + 1 })} checked={step.confirm === true} onChange={(e) => updateStep(step.id, { confirm: e.target.checked || undefined })} />
                      {t("执行前确认")}
                    </label>
                    <label className="mt-1 flex items-center gap-1.5 text-[11px] text-neutral-400">
                      <input type="checkbox" className="accent-blue-500" aria-label={t("步骤 {value} 启用脚本交互指令", { value: index + 1 })} checked={step.interactive === true} onChange={(e) => updateStep(step.id, { interactive: e.target.checked || undefined })} />
                      {t("启用脚本交互指令（仅多主机执行；开启后该步骤输出中的 @NEXTERM: 行会被解释，并保持标准输入打开）")}
                    </label>
                    <StepWaitFields step={step} index={index} problem={waitProblems[index] ?? null} onChange={(patch) => updateStep(step.id, patch)} />
                  </div>
                ))}
                <div>
                  <button type="button" className="nx-btn nx-btn-sm" onClick={() => setSteps((current) => [...current, blankStep()])}>
                    <IconPlus size={12} />{t("添加步骤")}
                  </button>
                </div>
              </div>
            )}
            <div id={hintId} className="nx-hint mt-1.5 leading-relaxed">
              <div>
                {t("支持使用变量，运行时需填写：")}<code>{"{{service}}"}</code> {t("必填，")}<code>{"{{service=nginx}}"}</code> {t("或")} <code>{"{{service:nginx}}"}</code> {t("带默认值，")}<code>{"{{service|multiline}}"}</code> {t("多行输入。")}
                {multi && t("多步片段的所有步骤共用同一组变量，只填写一次。")}
              </div>
              {multi && (
                <div className="mt-1">
                  {t("在当前终端执行时，填写了等待输出的步骤会等终端出现该文本后再发送：第一步从当前屏幕开始匹配，之后匹配上一步发送后的输出；超时或取消会停止执行并提示。正则使用多行模式，不支持前后断言和反向引用。多主机执行逐步等待命令结束，不使用等待条件。")}
                </div>
              )}
              <div className="mt-1">
                {t("自动填入当前主机信息：")}<code>{"{{asset.name}}"}</code> {t("名称、")}<code>{"{{asset.host}}"}</code> {t("地址、")}<code>{"{{asset.tags}}"}</code> {t("标签。")}
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
            {t("取消")}
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={saving || !valid}
            onClick={() => void save()}
          >
            {saving ? t("正在保存…") : t("保存")}
          </button>
        </div>
      </div>
    </div>
  );
}
