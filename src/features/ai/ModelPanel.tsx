import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { modelApi, type ModelProfile, type ModelProfilesView } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import {
  fallbackModelFromInput,
  fallbackModelLabel,
  sameModelProfile,
  selectModelProfileId,
} from "./modelLifecycle";
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
    temperature: 0.3,
    contextWindow: 32768,
    proxy: null,
    stream: true,
    fallbackModel: null,
  };
}

export function ModelManager({
  onRequestClose,
  onDirtyChange,
  listWidthClassName = "w-[30%] min-w-[150px] max-w-[210px] shrink-0",
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
  };

  const newProfile = async () => {
    if (!(await guardDiscard("新增档案"))) return;
    setDraft(blankProfile());
    setModels([]);
    setModelsOpen(false);
  };

  const save = async () => {
    if (!draft) return;
    setBusy(true);
    try {
      const saved = await modelApi.save(draft);
      useUi.getState().bumpModelProfilesRevision();
      pushToast("info", `已保存模型档案「${saved.name}」`);
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
      setModels(list);
      setModelsOpen(true);
      if (list.length === 0) pushToast("info", "这个端点没有返回任何模型");
    } catch (e) {
      pushToast("error", `拉取模型列表失败：${describeError(e)}`);
    } finally {
      setBusy(false);
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
      <div className="flex gap-4">
        <div className={`flex flex-col ${listWidthClassName}`}>
          <div className="mb-1.5 flex items-center gap-1">
            <span className="text-[11px] font-medium text-neutral-300">模型档案</span>
            <div className="nx-spacer" />
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="新增档案"
              onClick={() => void newProfile()}
            >
              <IconPlus size={13} />
            </button>
          </div>
          <div className="min-h-[220px] max-h-[420px] overflow-y-auto rounded-md border border-neutral-800/70 bg-neutral-950/40 p-1">
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
                    className="nx-chip"
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
                    className="nx-icon-btn nx-icon-btn-sm absolute top-1/2 right-1 -translate-y-1/2"
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
                    <div className="absolute top-full right-0 z-10 mt-1 max-h-52 w-[280px] overflow-y-auto rounded-md border border-neutral-700 bg-neutral-900 p-1 shadow-lg">
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

              <div className="flex gap-3">
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
                API Key 以明文存进本机 sqlite（与 AI 权限配置同一约定），不会上传到任何服务器。
              </div>
            </div>
          ) : (
            <div className="flex h-full min-h-[240px] flex-col items-center justify-center gap-2 text-center">
              <IconKey size={20} className="text-neutral-600" />
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

      <div className="flex items-center gap-2 border-t border-neutral-800/60 pt-3">
        {draft ? (
          <>
            <span className="nx-hint mr-auto self-center text-[10.5px]">
              {isNew ? "新档案（尚未保存）" : isActive ? "当前激活" : `${draft.name}`}
              {dirty && <span className="ml-1 text-[var(--nx-fg-warning)]">· 有未保存的修改</span>}
            </span>
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
          <button className="nx-icon-btn nx-icon-btn-sm" title="关闭" onClick={() => void requestClose()}>
            <IconClose size={13} />
          </button>
        </div>

        <div className="nx-modal-body">
          <ModelManager onDirtyChange={setDirty} onRequestClose={() => void requestClose()} />
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
