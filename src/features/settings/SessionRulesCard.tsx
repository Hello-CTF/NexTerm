import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { assetApi, localFileApi, terminalApi } from "../../ipc/commands";
import { logTailApi } from "../../ipc/logTailApi";
import { WEB } from "../../ipc/env";
import { pickBrowserFile, saveBrowserBlob } from "../../ipc/webFiles";
import {
  stateRuleApi,
  type StateRuleDTO,
  type StateRuleHelperStatusDTO,
  type StateRuleScreenDTO,
} from "../../ipc/stateRuleApi";
import { useUi, useWorkspaceList } from "../../app/store";
import { getTerminalTabTitle } from "../../app/terminalViewStore";
import { ask, pickLocalFile, pickSavePath } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconArrowDown,
  IconArrowUp,
  IconDownload,
  IconEdit,
  IconFlag,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconUpload,
  IconXCircle,
} from "../../ui/icons";
import { useHostLabeler } from "../hosts/hostLabel";
import { StateRuleForm, useRuleValidation, type HostOption } from "../session/StateRuleForm";
import {
  emptyRuleForm,
  formFromRule,
  parseRuleFile,
  saveRequestFromForm,
  screenFromSnapshot,
  stateLabel,
  type StateRuleDraftForm,
} from "../session/stateRuleDraft";

// A rules file holds at most 64 short rules; anything larger is not one.
const IMPORT_MAX_BYTES = 1 << 20;

function decodeBase64Text(base64: string): string {
  const binary = atob(base64);
  return new TextDecoder().decode(Uint8Array.from(binary, (char) => char.charCodeAt(0)));
}

// The built-in local device (store.BuiltinLocalAssetID), which runs the local helper.

const STATUS_LABELS: Record<string, string> = {
  applied: "已生效",
  pending: "等待连接",
  failed: "应用失败",
};

interface TerminalChoice {
  appTabId: string;
  tabId: string;
  label: string;
}

// openTerminalChoices lists the terminal tabs whose screen a rule can be tested on.
function useTerminalChoices(): TerminalChoice[] {
  const workspaces = useWorkspaceList();
  return useMemo(() => {
    const choices: TerminalChoice[] = [];
    for (const workspace of workspaces) {
      for (const pane of workspace.panes) {
        for (const tab of pane.tabs) {
          if (tab.kind !== "terminal" || !tab.tabId || tab.dead || tab.exited) continue;
          choices.push({ appTabId: tab.id, tabId: tab.tabId, label: `${workspace.title} / ${tab.title}` });
        }
      }
    }
    return choices;
  }, [workspaces]);
}

// RuleScreenPicker reads the screen of a chosen terminal tab for the rule test.
function RuleScreenPicker({ onScreen }: { onScreen: (screen: StateRuleScreenDTO | null) => void }) {
  const choices = useTerminalChoices();
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const chosen = choices.find((choice) => choice.appTabId === selected) ?? null;

  const read = async () => {
    if (!chosen) return;
    setBusy(true);
    setError(null);
    try {
      const snapshot = await terminalApi.snapshot(chosen.tabId);
      onScreen(screenFromSnapshot(snapshot, getTerminalTabTitle(chosen.appTabId)));
    } catch (cause) {
      setError(describeError(cause));
      onScreen(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-1.5">
      <div className="nx-setting-label">在终端屏幕上测试</div>
      <div className="flex flex-wrap items-center gap-2">
        <select
          className="nx-select min-w-0 flex-1"
          aria-label="选择要测试的终端"
          value={selected}
          onChange={(event) => {
            setSelected(event.target.value);
            onScreen(null);
          }}
        >
          <option value="">{choices.length === 0 ? "暂无已打开的终端" : "选择终端…"}</option>
          {choices.map((choice) => (
            <option key={choice.appTabId} value={choice.appTabId}>
              {choice.label}
            </option>
          ))}
        </select>
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={!chosen || busy} onClick={() => void read()}>
          {busy ? <IconRefresh size={11} className="animate-spin" /> : <IconRefresh size={11} />}
          读取屏幕并测试
        </button>
      </div>
      {error && (
        <div className="nx-alert nx-alert-danger text-[12px]" role="alert">
          无法读取屏幕：{error}
        </div>
      )}
    </div>
  );
}

function RuleEditor({
  initial,
  hosts,
  hostLabel,
  onSaved,
  onCancel,
}: {
  initial: StateRuleDraftForm;
  hosts: readonly HostOption[];
  hostLabel: (id: string | undefined) => string;
  onSaved: (rule: StateRuleDTO) => void;
  onCancel: () => void;
}) {
  const [form, setForm] = useState(initial);
  const [screen, setScreen] = useState<StateRuleScreenDTO | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const validation = useRuleValidation(form, screen);
  const canSave = Boolean(validation.result?.valid) && !validation.pending && !saving;

  const save = async () => {
    if (!canSave) return;
    setSaving(true);
    setSaveError(null);
    try {
      onSaved(await stateRuleApi.save(saveRequestFromForm(form)));
    } catch (cause) {
      setSaveError(describeError(cause));
      setSaving(false);
    }
  };

  return (
    <div className="flex flex-col gap-3 rounded-md border border-[var(--nx-border)] p-3" data-rule-editor>
      <StateRuleForm form={form} onChange={setForm} hosts={hosts} hostLabel={hostLabel} validation={validation} saveError={saveError} />
      <RuleScreenPicker onScreen={setScreen} />
      <div className="flex justify-end gap-2">
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onCancel}>
          取消
        </button>
        <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" disabled={!canSave} onClick={() => void save()}>
          {saving ? "正在保存…" : "保存"}
        </button>
      </div>
    </div>
  );
}

export function SessionRulesCard() {
  const pushToast = useUi((state) => state.pushToast);
  const [rules, setRules] = useState<StateRuleDTO[] | null>(null);
  const [statuses, setStatuses] = useState<StateRuleHelperStatusDTO[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [query, setQuery] = useState("");
  // "new" for the create form, a rule id for an edit, null when no editor is open.
  const [editing, setEditing] = useState<string | null>(null);
  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const hosts: HostOption[] = useMemo(
    () => (assetsQuery.data ?? []).map((asset) => ({ id: asset.id, name: asset.name })),
    [assetsQuery.data],
  );
  const hostName = useHostLabeler();

  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setLoading(true);
    try {
      const [list, status] = await Promise.all([stateRuleApi.list(), stateRuleApi.status()]);
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setRules(list);
      setStatuses(status);
      setError(null);
    } catch (cause) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setError(describeError(cause));
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  // run performs one change and reloads, so the list and the push status always show the stored result.
  const run = async (action: () => Promise<unknown>, failure: string) => {
    if (busy) return;
    setBusy(true);
    try {
      await action();
    } catch (cause) {
      if (aliveRef.current) setError(`${failure}：${describeError(cause)}`);
    } finally {
      if (aliveRef.current) setBusy(false);
      await reload();
    }
  };

  const toggle = (rule: StateRuleDTO) =>
    void run(() => stateRuleApi.save(saveRequestFromForm({ ...formFromRule(rule), enabled: !rule.enabled })), "切换失败");

  const move = (index: number, step: -1 | 1) => {
    if (!rules) return;
    const order = rules.map((rule) => rule.id);
    const target = index + step;
    if (target < 0 || target >= order.length) return;
    [order[index], order[target]] = [order[target], order[index]];
    void run(() => stateRuleApi.reorder(order), "调整顺序失败");
  };

  const remove = async (rule: StateRuleDTO) => {
    const ok = await ask(`确定要删除状态规则「${rule.name}」吗？`, { title: "删除状态规则", kind: "warning" });
    if (!ok) return;
    await run(() => stateRuleApi.delete(rule.id), "删除失败");
  };

  const exportRules = async () => {
    try {
      const file = await stateRuleApi.export();
      const text = `${JSON.stringify(file, null, 2)}\n`;
      if (WEB) {
        await saveBrowserBlob("nexterm-state-rules.json", new Blob([text], { type: "application/json" }));
        pushToast("success", `已导出 ${file.rules.length} 条状态规则`);
      } else {
        const path = await pickSavePath("nexterm-state-rules.json");
        if (!path) return;
        const saved = await logTailApi.save(path, text);
        pushToast("success", `已导出 ${file.rules.length} 条状态规则到 ${saved.path}`);
      }
    } catch (cause) {
      setError(`导出失败：${describeError(cause)}`);
    }
  };

  const importRules = async () => {
    let text: string | null;
    try {
      if (WEB) {
        const file = await pickBrowserFile(".json,application/json");
        text = file ? await file.text() : null;
      } else {
        const path = await pickLocalFile();
        text = path ? decodeBase64Text((await localFileApi.read(path, IMPORT_MAX_BYTES)).contentBase64) : null;
      }
    } catch (cause) {
      setError(`导入失败：${describeError(cause)}`);
      return;
    }
    if (!text?.trim()) return;
    let parsed;
    try {
      parsed = parseRuleFile(text);
    } catch (cause) {
      setError(`导入失败：${describeError(cause)}`);
      return;
    }
    await run(async () => {
      const result = await stateRuleApi.import(parsed);
      pushToast("success", `导入完成：新增 ${result.created} 条，更新 ${result.updated} 条，未变更 ${result.unchanged} 条`);
    }, "导入失败");
  };

  const q = query.trim().toLowerCase();
  const visible = (rules ?? [])
    .map((rule, index) => ({ rule, index }))
    .filter(({ rule }) => !q || [rule.name, rule.pattern, stateLabel(rule.state), hostName(rule.assetId)].some((text) => text.toLowerCase().includes(q)));
  const sortable = !q;

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconFlag size={15} className="text-neutral-400" />
        <span className="nx-card-title">状态规则</span>
        {rules && <span className="nx-badge">{rules.length} 条</span>}
        <div className="nx-spacer" />
        {rules && rules.length > 0 && (
          <input
            className="nx-input nx-input-sm w-full sm:w-40"
            aria-label="搜索状态规则"
            placeholder="搜索名称 / 模式"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        )}
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void reload()}>
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
          刷新
        </button>
      </div>
      <p className="nx-hint mb-3">
        屏幕底部或窗口标题与规则匹配时，会话将显示为需要处理、运行中、空闲或刚结束。规则按顺序匹配，第一条匹配的规则生效，主机规则优先于全局规则；规则由持久终端在各主机上执行。在标签页菜单中选择「状态不对」或「这需要处理」，可根据当前屏幕起草规则。
      </p>

      {error && (
        <div className="nx-alert nx-alert-danger mb-2 flex items-start gap-2 text-[12px]" role="alert">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{error}</span>
        </div>
      )}

      <div className="mb-3 flex flex-wrap gap-2" data-setting="state-rules-import">
        <button className="nx-btn nx-btn-outline nx-btn-sm" disabled={editing !== null} onClick={() => setEditing("new")}>
          <IconPlus size={11} />
          新建规则
        </button>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy} onClick={() => void importRules()}>
          <IconUpload size={11} />
          导入
        </button>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" disabled={!rules || rules.length === 0} onClick={() => void exportRules()}>
          <IconDownload size={11} />
          导出
        </button>
      </div>

      {editing === "new" && (
        <div className="mb-3">
          <RuleEditor
            initial={emptyRuleForm()}
            hosts={hosts}
            hostLabel={hostName}
            onCancel={() => setEditing(null)}
            onSaved={() => {
              setEditing(null);
              pushToast("success", "已保存状态规则");
              void reload();
            }}
          />
        </div>
      )}

      {rules === null ? (
        !error && <div className="nx-hint py-2 text-[12px]">正在读取…</div>
      ) : rules.length === 0 ? (
        <div className="nx-hint py-2 text-[12px]">暂无状态规则。</div>
      ) : (
        <ol className="flex flex-col gap-1.5" aria-label="状态规则">
          {visible.map(({ rule, index }) => (
            <li key={rule.id} data-rule-id={rule.id}>
              <div className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-1 h-4 w-4 shrink-0"
                  aria-label={`启用 ${rule.name}`}
                  checked={rule.enabled}
                  disabled={busy}
                  onChange={() => toggle(rule)}
                />
                <div className="min-w-0 flex-1">
                  <div className={`flex flex-wrap items-baseline gap-x-2 text-[12.5px] ${rule.enabled ? "text-neutral-200" : "text-neutral-500"}`}>
                    <span className="min-w-0 truncate">{rule.name}</span>
                    <span className="nx-badge">{stateLabel(rule.state)}</span>
                    <span className="nx-hint shrink-0">
                      {rule.scope === "host" ? `仅 ${hostName(rule.assetId)}` : "所有主机"} ·{" "}
                      {rule.region === "title" ? "窗口标题" : `底部 ${rule.lines ?? 5} 行`}
                    </span>
                  </div>
                  <div className="truncate font-mono text-[11px] text-neutral-500" title={rule.pattern}>
                    {rule.pattern}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-0.5">
                  <button
                    className="nx-icon-btn"
                    aria-label={`上移 ${rule.name}`}
                    disabled={!sortable || busy || index === 0}
                    onClick={() => move(index, -1)}
                  >
                    <IconArrowUp size={12} />
                  </button>
                  <button
                    className="nx-icon-btn"
                    aria-label={`下移 ${rule.name}`}
                    disabled={!sortable || busy || index === rules.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    <IconArrowDown size={12} />
                  </button>
                  <button
                    className="nx-icon-btn"
                    aria-label={`编辑 ${rule.name}`}
                    disabled={editing !== null}
                    onClick={() => setEditing(rule.id)}
                  >
                    <IconEdit size={12} />
                  </button>
                  <button className="nx-icon-btn" aria-label={`删除 ${rule.name}`} disabled={busy} onClick={() => void remove(rule)}>
                    <IconTrash size={12} />
                  </button>
                </div>
              </div>
              {editing === rule.id && (
                <div className="mt-2">
                  <RuleEditor
                    initial={formFromRule(rule)}
                    hosts={hosts}
                    hostLabel={hostName}
                    onCancel={() => setEditing(null)}
                    onSaved={() => {
                      setEditing(null);
                      pushToast("success", "已保存状态规则");
                      void reload();
                    }}
                  />
                </div>
              )}
            </li>
          ))}
          {visible.length === 0 && <li className="nx-hint py-2 text-[12px]">无匹配的规则。</li>}
        </ol>
      )}
      {!sortable && rules && rules.length > 1 && <p className="nx-hint mt-2 text-[11.5px]">清空搜索后可调整顺序。</p>}

      <div className="mt-4">
        <div className="nx-setting-label mb-1.5" data-setting="state-rules-status">各主机的规则状态</div>
        {statuses.length === 0 ? (
          <p className="nx-hint text-[12px]">尚无主机使用持久终端，暂无规则状态。</p>
        ) : (
          <ul className="flex flex-col gap-1" aria-label="各主机的规则状态">
            {statuses.map((status) => (
              <li key={status.assetId} className="flex flex-wrap items-baseline gap-x-2 text-[12px]" data-status={status.status}>
                <span className="text-neutral-200">{hostName(status.assetId)}</span>
                <span className={status.status === "applied" ? "text-green-500" : status.status === "failed" ? "text-red-400" : "text-neutral-400"}>
                  {STATUS_LABELS[status.status] ?? status.status}
                </span>
                {status.skipped && status.skipped.length > 0 && (
                  <span className="text-[var(--nx-fg-warning)]">
                    跳过无效规则：{status.skipped.map((id) => rules?.find((rule) => rule.id === id)?.name ?? id).join("、")}
                  </span>
                )}
                {status.error && status.status !== "applied" && <span className="nx-hint break-all">{status.error}</span>}
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
