import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { modelApi, type AiUsageSummaryRow, type ModelProfile, type ModelProfilesView, type ProviderTestResult } from "../../ipc/commands";
import type { AiCircuitStatusDto } from "../../ipc/types";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import {
  CIRCUIT_DEFAULT_COOLDOWN_SECONDS,
  CIRCUIT_DEFAULT_THRESHOLD,
  circuitCooldownFromInput,
  circuitCooldownLabel,
  circuitRuntimeState,
  circuitStatusText,
  circuitThresholdFromInput,
  circuitThresholdLabel,
  fallbackModelFromInput,
  fallbackModelLabel,
  idleTimeoutLabel,
  MAX_TOKENS_HARD_LIMIT,
  maxTokensFromInput,
  maxTokensLabel,
  MODEL_PARAM_DEFAULTS,
  modelParamsAtDefaults,
  requestTimeoutLabel,
  resetModelParams,
  sameModelProfile,
  selectModelProfileId,
  timeoutSecondsFromInput,
} from "./modelLifecycle";
import { formatTokens } from "./UsageRing";
import {
  IconCheck,
  IconClose,
  IconEye,
  IconEyeOff,
  IconKey,
  IconLoader,
  IconPlus,
  IconRefresh,
  IconSave,
  IconSparkles,
  IconTrash,
} from "../../ui/icons";

function blankProfile(): ModelProfile {
  return {
    id: "",
    name: "",
    baseUrl: "",
    apiKey: "",
    model: "",
    ...MODEL_PARAM_DEFAULTS,
    requestTimeoutSeconds: null,
    idleTimeoutSeconds: null,
    maxTokens: null,
    circuitFailureThreshold: null,
    circuitCooldownSeconds: null,
  };
}

export function ModelManager({
  onRequestClose,
  onDirtyChange,
  listWidthClassName = "w-full min-w-0 min-[560px]:w-[30%] min-[560px]:min-w-[150px] min-[560px]:max-w-[210px] min-[560px]:shrink-0",
}: {
  onRequestClose?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  listWidthClassName?: string;
}) {
  const pushToast = useUi((s) => s.pushToast);

  const [view, setView] = useState<ModelProfilesView | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [draft, setDraft] = useState<ModelProfile | null>(null);
  const [busy, setBusy] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [models, setModels] = useState<string[]>([]);
  const [modelsOpen, setModelsOpen] = useState(false);
  const [presets, setPresets] = useState<string[]>([]);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<ProviderTestResult | null>(null);
  const [circuitNonce, setCircuitNonce] = useState(0);

  const isNew = !!draft && draft.id === "";
  const savedProfile = view?.profiles.find((p) => p.id === draft?.id) ?? null;
  const dirty = !!draft && (!savedProfile || !sameModelProfile(savedProfile, draft));
  const isActive = !!draft && !!draft.id && view?.activeId === draft.id;
  const fieldId = useId();

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const reload = async (keepId?: string) => {
    try {
      const v = await modelApi.overview();
      setView(v);
      setLoadError(null);
      setDraft((prev) => {
        const target = selectModelProfileId(v, keepId ?? prev?.id);
        const found = v.profiles.find((p) => p.id === target);
        return found ? { ...found } : null;
      });
    } catch (e) {
      setLoadError(describeError(e));
    }
  };

  useEffect(() => {
    void reload();
    void modelApi
      .presets()
      .then(setPresets)
      .catch(() => undefined);
  }, []);

  const patch = (p: Partial<ModelProfile>) => setDraft((prev) => (prev ? { ...prev, ...p } : prev));

  const paramsAtDefaults = !!draft && modelParamsAtDefaults(draft);

  const resetParams = () => setDraft((prev) => (prev ? resetModelParams(prev) : prev));

  const guardDiscard = async (what: string): Promise<boolean> => {
    if (!dirty) return true;
    return ask(`当前修改还没保存，${what}会丢掉这些改动，继续？`, {
      title: "放弃未保存的修改",
      kind: "warning",
    });
  };

  const selectProfile = async (id: string) => {
    const p = view?.profiles.find((x) => x.id === id);
    if (!p) return;
    if (!(await guardDiscard("切换档案"))) return;
    setDraft({ ...p });
    setModels([]);
    setModelsOpen(false);
    setTestResult(null);
  };

  const newProfile = async () => {
    if (!(await guardDiscard("新增档案"))) return;
    setDraft(blankProfile());
    setModels([]);
    setModelsOpen(false);
    setTestResult(null);
  };

  const save = async () => {
    if (!draft) return;
    setBusy(true);
    try {
      const saved = await modelApi.save(draft);
      useUi.getState().bumpModelProfilesRevision();
      setCircuitNonce((n) => n + 1);
      setView((prev) => {
        if (!prev) return prev;
        const exists = prev.profiles.some((p) => p.id === saved.id);
        return {
          ...prev,
          profiles: exists
            ? prev.profiles.map((p) => (p.id === saved.id ? saved : p))
            : [...prev.profiles, saved],
        };
      });
      pushToast("info", `已保存模型档案「${saved.name}」`);
      setTestResult(null);
      await reload(saved.id);
    } catch (e) {
      pushToast("error", `保存失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const activate = async () => {
    if (!draft || !savedProfile) return;
    if (!(await guardDiscard("设为当前"))) return;
    const target = savedProfile;
    setBusy(true);
    try {
      await modelApi.activate(target.id);
      useUi.getState().bumpModelProfilesRevision();
      await reload(target.id);
      pushToast("info", `当前模型已切换为「${target.name}」`);
    } catch (e) {
      pushToast("error", `切换失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!draft || !draft.id) return;
    const ok = await ask(`删除模型档案「${draft.name || draft.model}」？此操作不可撤销。`, {
      title: "删除档案",
      kind: "warning",
    });
    if (!ok) return;
    setBusy(true);
    try {
      await modelApi.remove(draft.id);
      useUi.getState().bumpModelProfilesRevision();
      pushToast("info", "已删除");
      setModels([]);
      setModelsOpen(false);
      await reload();
    } catch (e) {
      pushToast("error", `删除失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const refreshModels = async () => {
    if (!draft) return;
    setBusy(true);
    try {
      const list = await modelApi.refresh(draft);
      setModels(list.models);
      setModelsOpen(true);
      if (list.malformed > 0) {
        pushToast("info", `端点返回的模型列表里有 ${list.malformed} 个格式异常的条目，已跳过`);
      } else if (list.models.length === 0) {
        pushToast("info", "这个端点没有返回任何模型");
      }
    } catch (e) {
      pushToast("error", `拉取模型列表失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const testConnection = async () => {
    if (!draft || !draft.id) return;
    setTesting(true);
    setTestResult(null);
    try {
      setTestResult(await modelApi.test(draft.id));
      setCircuitNonce((n) => n + 1);
    } catch (e) {
      pushToast("error", `连接测试失败：${describeError(e)}`);
    } finally {
      setTesting(false);
    }
  };

  const applyPreset = async (name: string) => {
    try {
      const tpl = await modelApi.preset(name);
      setDraft((prev) => {
        const base = prev ?? blankProfile();
        return {
          ...base,
          name: base.name.trim() ? base.name : tpl.name,
          baseUrl: tpl.baseUrl,
          model: tpl.model,
          temperature: tpl.temperature,
          contextWindow: tpl.contextWindow,
          stream: tpl.stream,
        };
      });
    } catch (e) {
      pushToast("error", `应用预设失败：${describeError(e)}`);
    }
  };

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-4 min-[560px]:flex-row">
        <div className={`flex flex-col ${listWidthClassName}`}>
          <div className="mb-1.5 flex items-center gap-1">
            <span className="text-[11px] font-medium text-neutral-300">模型档案</span>
            <div className="nx-spacer" />
            <button
              className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6"
              title="新增档案"
              onClick={() => void newProfile()}
            >
              <IconPlus size={13} />
            </button>
          </div>
          <div className="max-h-56 min-h-0 overflow-y-auto rounded-md border border-neutral-800/70 bg-neutral-950/40 p-1 min-[560px]:min-h-[220px] min-[560px]:max-h-[420px]">
            {!view ? (
              loadError ? (
                <div className="px-2 py-3 text-center">
                  <div className="nx-hint text-red-300">档案列表加载失败 · {loadError}</div>
                  <button
                    className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5"
                    onClick={() => void reload()}
                  >
                    <IconRefresh size={11} />
                    重试
                  </button>
                </div>
              ) : (
                <div className="nx-hint px-2 py-3 text-center text-[11px]">模型档案加载中…</div>
              )
            ) : (
              <>
                {loadError && (
                  <div className="mb-1 border-b border-neutral-800/60 px-2 pb-2 pt-1 text-center">
                    <div className="nx-hint text-red-300">档案列表刷新失败 · {loadError}</div>
                    <button
                      className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5"
                      onClick={() => void reload()}
                    >
                      <IconRefresh size={11} />
                      重试
                    </button>
                  </div>
                )}
                {view.profiles.length > 0 ? (
                  view.profiles.map((p) => (
                    <button
                      key={p.id}
                      className={`nx-menu-item w-full ${draft?.id === p.id ? "bg-blue-500/15" : ""}`}
                      title={p.model || p.baseUrl}
                      onClick={() => void selectProfile(p.id)}
                    >
                      <span className="nx-menu-icon">
                        {view.activeId === p.id ? (
                          <IconCheck size={12} className="text-green-300" />
                        ) : (
                          <span className="nx-dot" />
                        )}
                      </span>
                      <span className="nx-menu-label">{p.name}</span>
                      {view.activeId === p.id && (
                        <span className="nx-menu-hint text-green-400/80">当前</span>
                      )}
                    </button>
                  ))
                ) : (
                  <div className="nx-hint px-2 py-3 text-center text-[11px]">还没有模型档案</div>
                )}
              </>
            )}
          </div>
          {presets.length > 0 && (
            <div className="mt-2">
              <div className="mb-1 text-[10.5px] text-neutral-500">快速填充（预设）</div>
              <div className="flex flex-wrap gap-1">
                {presets.map((p) => (
                  <button
                    key={p}
                    className="nx-chip pointer-coarse:min-h-6"
                    title={`用 ${p} 的默认地址与模型填充表单`}
                    onClick={() => void applyPreset(p)}
                  >
                    <IconSparkles size={10} />
                    <span>{p}</span>
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>

        <div className="min-w-0 flex-1">
          {draft ? (
            <div className="flex flex-col gap-2.5">
              <Field label="展示名" htmlFor={`${fieldId}-name`}>
                <input
                  id={`${fieldId}-name`}
                  className="nx-input"
                  placeholder="例如 公司 DeepSeek"
                  value={draft.name}
                  onChange={(e) => patch({ name: e.target.value })}
                />
              </Field>

              <Field label="Base URL" htmlFor={`${fieldId}-base-url`}>
                <input
                  id={`${fieldId}-base-url`}
                  className="nx-input font-mono"
                  placeholder="https://api.deepseek.com/v1"
                  value={draft.baseUrl}
                  onChange={(e) => patch({ baseUrl: e.target.value })}
                />
              </Field>

              <Field label="API Key" htmlFor={`${fieldId}-api-key`}>
                <div className="nx-field">
                  <span className="nx-field-icon">
                    <IconKey size={12} />
                  </span>
                  <input
                    id={`${fieldId}-api-key`}
                    className="nx-input pr-8 font-mono"
                    type={showKey ? "text" : "password"}
                    placeholder="sk-..."
                    value={draft.apiKey}
                    autoComplete="off"
                    onChange={(e) => patch({ apiKey: e.target.value })}
                  />
                  <button
                    className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 absolute top-1/2 right-1 -translate-y-1/2"
                    title={showKey ? "隐藏密钥" : "显示密钥"}
                    onClick={() => setShowKey((v) => !v)}
                  >
                    {showKey ? <IconEyeOff size={12} /> : <IconEye size={12} />}
                  </button>
                </div>
              </Field>

              <Field label="模型名" htmlFor={`${fieldId}-model`}>
                <div className="relative flex gap-1">
                  <input
                    id={`${fieldId}-model`}
                    className="nx-input min-w-0 flex-1 font-mono"
                    placeholder="deepseek-chat"
                    value={draft.model}
                    onChange={(e) => patch({ model: e.target.value })}
                  />
                  <button
                    className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
                    title="用当前 Base URL + API Key 拉取 /models"
                    disabled={busy}
                    onClick={() => void refreshModels()}
                  >
                    {busy ? <IconLoader size={12} className="animate-spin" /> : <IconRefresh size={12} />}
                    刷新模型列表
                  </button>
                  {modelsOpen && models.length > 0 && (
                    <div className="absolute top-full right-0 z-10 mt-1 max-h-52 w-[min(280px,calc(100vw-64px))] overflow-y-auto rounded-md border border-neutral-700 bg-neutral-900 p-1 shadow-lg">
                      <div className="nx-menu-title">点一个填入模型名</div>
                      {models.map((m) => (
                        <button
                          key={m}
                          className="nx-menu-item w-full"
                          onClick={() => {
                            patch({ model: m });
                            setModelsOpen(false);
                          }}
                        >
                          <span className="nx-menu-label font-mono">{m}</span>
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              </Field>

              <Field label="回退模型（可选）" htmlFor={`${fieldId}-fallback`}>
                <input
                  id={`${fieldId}-fallback`}
                  className="nx-input font-mono"
                  placeholder="主模型失败时改用的模型名"
                  value={fallbackModelLabel(draft)}
                  onChange={(e) => patch({ fallbackModel: fallbackModelFromInput(e.target.value) })}
                />
                <div className="nx-hint mt-1 text-[10.5px]">
                  不改这里就保留原设置；清空后保存 = 明确移除回退。
                </div>
              </Field>

              <div className="flex flex-col gap-3 min-[400px]:flex-row">
                <div className="flex-1">
                  <Field label="温度 (0–2)" htmlFor={`${fieldId}-temperature`}>
                    <input
                      id={`${fieldId}-temperature`}
                      className="nx-input font-mono"
                      type="number"
                      min={0}
                      max={2}
                      step={0.1}
                      value={draft.temperature}
                      onChange={(e) => patch({ temperature: Number(e.target.value) })}
                    />
                  </Field>
                </div>
                <div className="flex-1">
                  <Field label="上下文窗口 (tokens)" htmlFor={`${fieldId}-context`}>
                    <input
                      id={`${fieldId}-context`}
                      className="nx-input font-mono"
                      type="number"
                      min={1000}
                      max={2000000}
                      step={1000}
                      value={draft.contextWindow}
                      onChange={(e) => patch({ contextWindow: Number(e.target.value) })}
                    />
                  </Field>
                </div>
              </div>

              <Field label="最大输出 tokens（留空不限）" htmlFor={`${fieldId}-max-tokens`}>
                <input
                  id={`${fieldId}-max-tokens`}
                  className="nx-input font-mono"
                  type="number"
                  min={1}
                  max={MAX_TOKENS_HARD_LIMIT}
                  step={1}
                  placeholder="不限"
                  value={maxTokensLabel(draft)}
                  onChange={(e) => patch({ maxTokens: maxTokensFromInput(e.target.value) })}
                />
                <div className="nx-hint mt-1 text-[10.5px]">
                  留空 = 不限制单次输出（旧档案默认）；填写后按上下文窗口一半、最高 {MAX_TOKENS_HARD_LIMIT} 生效。
                </div>
              </Field>

              <div className="flex flex-col gap-3 min-[400px]:flex-row">
                <div className="flex-1">
                  <Field label="请求总超时（秒）" htmlFor={`${fieldId}-request-timeout`}>
                    <input
                      id={`${fieldId}-request-timeout`}
                      className="nx-input font-mono"
                      type="number"
                      min={1}
                      max={3600}
                      step={1}
                      placeholder="默认 300"
                      value={requestTimeoutLabel(draft)}
                      onChange={(e) => patch({ requestTimeoutSeconds: timeoutSecondsFromInput(e.target.value, false) })}
                    />
                  </Field>
                </div>
                <div className="flex-1">
                  <Field label="流空闲超时（秒，0 关闭）" htmlFor={`${fieldId}-idle-timeout`}>
                    <input
                      id={`${fieldId}-idle-timeout`}
                      className="nx-input font-mono"
                      type="number"
                      min={0}
                      max={3600}
                      step={1}
                      placeholder="默认 60"
                      value={idleTimeoutLabel(draft)}
                      onChange={(e) => patch({ idleTimeoutSeconds: timeoutSecondsFromInput(e.target.value, true) })}
                    />
                  </Field>
                </div>
              </div>

              <div className="flex flex-col gap-3 min-[400px]:flex-row">
                <div className="flex-1">
                  <Field label="熔断失败阈值（次）" htmlFor={`${fieldId}-circuit-threshold`}>
                    <input
                      id={`${fieldId}-circuit-threshold`}
                      className="nx-input font-mono"
                      type="number"
                      min={1}
                      max={100}
                      step={1}
                      placeholder={`默认 ${CIRCUIT_DEFAULT_THRESHOLD}`}
                      value={circuitThresholdLabel(draft)}
                      onChange={(e) => patch({ circuitFailureThreshold: circuitThresholdFromInput(e.target.value) })}
                    />
                  </Field>
                </div>
                <div className="flex-1">
                  <Field label="熔断冷却（秒）" htmlFor={`${fieldId}-circuit-cooldown`}>
                    <input
                      id={`${fieldId}-circuit-cooldown`}
                      className="nx-input font-mono"
                      type="number"
                      min={1}
                      max={3600}
                      step={1}
                      placeholder={`默认 ${CIRCUIT_DEFAULT_COOLDOWN_SECONDS}`}
                      value={circuitCooldownLabel(draft)}
                      onChange={(e) => patch({ circuitCooldownSeconds: circuitCooldownFromInput(e.target.value) })}
                    />
                  </Field>
                </div>
              </div>
              <div className="nx-hint mt-0.5 text-[10.5px]">
                阈值与冷却留空则用默认值；熔断只统计网络与服务端错误。
              </div>
              {isNew ? (
                <div className="nx-hint mt-0.5 text-[10.5px]">保存后可查看熔断状态</div>
              ) : (
                <CircuitRuntimeStatus key={draft.id} profileId={draft.id} nonce={circuitNonce} />
              )}

              <Field label="代理（留空则跟随系统代理）" htmlFor={`${fieldId}-proxy`}>
                <input
                  id={`${fieldId}-proxy`}
                  className="nx-input font-mono"
                  placeholder="http://127.0.0.1:7890"
                  value={draft.proxy ?? ""}
                  onChange={(e) => patch({ proxy: e.target.value || null })}
                />
              </Field>

              <label className="flex cursor-pointer items-center gap-2 text-[12px] text-neutral-300">
                <input
                  className="nx-check"
                  type="checkbox"
                  checked={draft.stream}
                  onChange={(e) => patch({ stream: e.target.checked })}
                />
                流式输出（关闭后整块返回，部分自建端点更稳）
              </label>

              <div className="nx-hint mt-0.5">
                API Key 加密后存进本机 sqlite，不会上传到任何服务器。
              </div>
            </div>
          ) : (
            <div className="flex h-full min-h-[240px] flex-col items-center justify-center gap-2 text-center">
              <IconKey size={20} className="text-[var(--nx-fg-tertiary)]" />
              <div className="nx-hint max-w-[280px]">
                左侧选一份档案来编辑，或点 + 新增一份。也可以先用预设快速填充。
              </div>
              <button className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => void newProfile()}>
                <IconPlus size={12} />
                新增档案
              </button>
            </div>
          )}
        </div>
      </div>

      {testResult && (
        <div
          className={`rounded-md border px-2.5 py-2 text-[11px] ${
            testResult.modelsOk && testResult.chatOk
              ? "border-green-800/60 bg-green-900/20 text-green-200"
              : "border-red-800/60 bg-red-900/20 text-red-200"
          }`}
        >
          {testResult.modelsOk && testResult.chatOk ? (
            "连接正常：模型列表与对话均可用"
          ) : (
            <div className="flex flex-col gap-0.5">
              {!testResult.modelsOk && <span>模型列表：{testResult.modelsError || "不可用"}</span>}
              {!testResult.chatOk && <span>对话：{testResult.chatError || "不可用"}</span>}
            </div>
          )}
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2 border-t border-neutral-800/60 pt-3">
        {draft ? (
          <>
            <span className="nx-hint mr-auto self-center text-[10.5px]">
              {isNew ? "新档案（尚未保存）" : isActive ? "当前" : `${draft.name}`}
              {dirty && <span className="ml-1 text-[var(--nx-fg-warning)]">· 有未保存的修改</span>}
            </span>
            <button
              className="nx-btn nx-btn-outline nx-btn-sm"
              title="仅把温度、上下文窗口、代理、流式输出与回退模型恢复默认；名称、地址、密钥与模型名保持不变，点「保存」后才写入档案"
              disabled={busy || paramsAtDefaults}
              onClick={resetParams}
            >
              <IconRefresh size={12} />
              恢复默认参数
            </button>
            <button
              className="nx-btn nx-btn-outline nx-btn-sm"
              title={
                isNew
                  ? "保存后才能测试连接"
                  : dirty
                    ? "有未保存的修改，保存后才能测试这份档案"
                    : "用这份已保存的档案测试连通性"
              }
              disabled={busy || testing || isNew || dirty}
              onClick={() => void testConnection()}
            >
              {testing ? <IconLoader size={12} className="animate-spin" /> : <IconRefresh size={12} />}
              测试连接
            </button>
            <button
              className="nx-btn nx-btn-outline nx-btn-sm"
              title={isNew ? "保存后才能设为当前" : isActive ? "已经是当前档案" : "切换到这个档案"}
              disabled={busy || isNew || isActive}
              onClick={() => void activate()}
            >
              <IconCheck size={12} />
              设为当前
            </button>
            <button
              className="nx-btn nx-btn-danger nx-btn-sm"
              disabled={busy || isNew}
              onClick={() => void remove()}
            >
              <IconTrash size={12} />
              删除
            </button>
            <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={busy} onClick={() => void save()}>
              {busy ? <IconLoader size={12} className="animate-spin" /> : <IconSave size={12} />}
              保存
            </button>
          </>
        ) : (
          onRequestClose && (
            <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onRequestClose}>
              关闭
            </button>
          )
        )}
      </div>
    </div>
  );
}

function CircuitRuntimeStatus({ profileId, nonce }: { profileId: string; nonce: number }) {
  const [status, setStatus] = useState<AiCircuitStatusDto | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());

  const load = useCallback(async () => {
    try {
      setStatus(await modelApi.circuitStatus(profileId));
      setError(null);
    } catch (e) {
      setError(describeError(e));
    }
  }, [profileId]);

  useEffect(() => {
    void load();
  }, [load, nonce]);

  const state = status ? circuitRuntimeState(status, now) : null;
  const openUntil = status?.openUntil;

  useEffect(() => {
    if (state !== "open" || openUntil == null) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [state, openUntil]);

  return (
    <div className="mt-0.5 flex items-start gap-1.5">
      <span className="nx-hint flex-1 text-[10.5px]">
        {error ? (
          <span className="text-red-300">熔断状态读取失败 · {error}</span>
        ) : !status ? (
          "熔断状态加载中…"
        ) : (
          <>
            <span
              className={
                state === "open"
                  ? "text-red-300"
                  : state === "closed"
                    ? "text-[var(--nx-fg-warning)]"
                    : undefined
              }
            >
              {circuitStatusText(status, now)}
            </span>
            <span className="text-neutral-500">；与聊天里的「可重试」标记是两回事</span>
          </>
        )}
      </span>
      <button
        className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6 shrink-0"
        title="重新读取熔断状态"
        onClick={() => void load()}
      >
        <IconRefresh size={11} />
      </button>
    </div>
  );
}

function sceneLabel(source: string): string {
  switch (source) {
    case "cron":
      return "定时任务";
    case "subagent":
      return "子代理";
    default:
      return "交互聊天";
  }
}

function formatLatency(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return "—";
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.round(ms)}ms`;
}

export function UsageSummarySection() {
  const [rows, setRows] = useState<AiUsageSummaryRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [names, setNames] = useState<Record<string, string>>({});

  useEffect(() => {
    let canceled = false;
    void modelApi
      .usageSummary()
      .then((list) => {
        if (!canceled) setRows(list);
      })
      .catch((e) => {
        if (!canceled) setError(describeError(e));
      });
    void modelApi
      .overview()
      .then((view) => {
        if (canceled) return;
        const map: Record<string, string> = {};
        for (const p of view.profiles) map[p.id] = p.name;
        setNames(map);
      })
      .catch(() => undefined);
    return () => {
      canceled = true;
    };
  }, []);

  return (
    <div className="border-t border-neutral-800/60 pt-3">
      <div className="mb-1.5 text-[11px] font-medium text-neutral-300">用量汇总（按场景 / 档案）</div>
      {error ? (
        <div className="nx-hint text-red-300">用量加载失败 · {error}</div>
      ) : !rows ? (
        <div className="nx-hint text-[11px]">用量加载中…</div>
      ) : rows.length === 0 ? (
        <div className="nx-hint text-[11px]">还没有已完成的 AI 运行</div>
      ) : (
        <div className="max-h-40 overflow-y-auto rounded-md border border-neutral-800/70">
          <table className="w-full text-[11px]">
            <thead>
              <tr className="border-b border-neutral-800/70 text-neutral-500">
                <th className="px-2 py-1 text-left font-medium">场景</th>
                <th className="px-2 py-1 text-left font-medium">档案</th>
                <th className="px-2 py-1 text-right font-medium">运行</th>
                <th className="px-2 py-1 text-right font-medium">输入</th>
                <th className="px-2 py-1 text-right font-medium">输出</th>
                <th className="px-2 py-1 text-right font-medium">缓存写入</th>
                <th className="px-2 py-1 text-right font-medium">平均延迟</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={`${row.source}/${row.profileId}`} className="border-b border-neutral-800/40 last:border-b-0">
                  <td className="px-2 py-1 text-neutral-300">{sceneLabel(row.source)}</td>
                  <td className="max-w-[140px] truncate px-2 py-1 text-neutral-300" title={row.profileId}>
                    {row.profileId ? (names[row.profileId] ?? row.profileId) : "未记录"}
                  </td>
                  <td className="px-2 py-1 text-right font-mono text-neutral-400">{row.runs}</td>
                  <td className="px-2 py-1 text-right font-mono text-neutral-400">{formatTokens(row.tokensIn)}</td>
                  <td className="px-2 py-1 text-right font-mono text-neutral-400">{formatTokens(row.tokensOut)}</td>
                  <td className="px-2 py-1 text-right font-mono text-neutral-400">{formatTokens(row.cacheCreationTokens)}</td>
                  <td className="px-2 py-1 text-right font-mono text-neutral-400">{formatLatency(row.averageLatencyMs)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

export function ModelPanel({ onClose }: { onClose: () => void }) {
  const [dirty, setDirty] = useState(false);
  const modalRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const layer = useOverlayFocus(true, modalRef);

  const requestClose = async () => {
    if (
      dirty &&
      !(await ask("当前修改还没保存，关闭面板会丢掉这些改动，继续？", {
        title: "放弃未保存的修改",
        kind: "warning",
      }))
    ) {
      return;
    }
    onClose();
  };

  return (
    <div className="nx-overlay z-[70]" onClick={() => void requestClose()}>
      <div
        ref={modalRef}
        className="nx-modal max-w-[760px]"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (!layer.isTopmost()) return;
          if (e.key === "Escape" && !e.repeat && !isImeKeyEvent(e)) {
            e.preventDefault();
            void requestClose();
            return;
          }
          trapOverlayTab(e, modalRef.current);
        }}
      >
        <div className="nx-modal-header">
          <IconKey size={14} className="text-neutral-400" />
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">模型配置</span>
          <span className="nx-hint ml-1 text-[10.5px]">多份档案 · 密钥存在本机 sqlite</span>
          <div className="nx-spacer" />
          <button className="nx-icon-btn nx-icon-btn-sm pointer-coarse:min-h-6 pointer-coarse:min-w-6" title="关闭" onClick={() => void requestClose()}>
            <IconClose size={13} />
          </button>
        </div>

        <div className="nx-modal-body">
          <ModelManager onDirtyChange={setDirty} onRequestClose={() => void requestClose()} />
          <UsageSummarySection />
        </div>
      </div>
    </div>
  );
}

function Field({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: ReactNode;
}) {
  return (
    <div>
      <label className="nx-label" htmlFor={htmlFor}>{label}</label>
      {children}
    </div>
  );
}
