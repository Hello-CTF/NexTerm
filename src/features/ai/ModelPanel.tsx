// 模型配置（P0-3）：多份模型档案（BYOK）的增删改与切换。
//
// 拆成两层，是为了让同一份实现同时服务两个入口：
//   · `ModelPanel`  —— 弹窗外壳（overlay / 遮罩 / 标题栏），AI 侧栏在用；
//   · `ModelManager` —— 左档案列表 + 右编辑表单 + 底部操作条，**不含** overlay 与遮罩，
//                       设置页把它直接内联进「AI 模型」卡片。
// 为什么非拆不可：设置页原先另写了一份单 provider 表单（getProvider/setProvider + 硬编码
// 预设表），与这里的两套字段和默认值迟早漂移，最后变成「两份模型设置」——
// 拆出可内联的 `ModelManager` 后，两边就只有一处真相。
//
// 「自己管自己」：列表、草稿、保存全在 `ModelManager` 内部，外部不喂任何数据；
// 因此它既能在卡片里内联，也能塞进弹窗。
//
// 两条自己定的规矩：
//   · 展示名（name）是给列表看的，模型名（model）是发给接口的 —— 两者刻意分开，
//     同一个模型挂在不同 baseUrl 上时，用户靠展示名区分，不会被 model 串味；
//   · 「刷新模型列表」按**表单当前值**去请求，不要求先保存 ——
//     用户想先验证 baseUrl + key 能不能连，这个顺序必须支持。
import { useEffect, useState, type ReactNode } from "react";
import { modelApi, type ModelProfile, type ModelProfilesView } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
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

/* ── 纯函数 ─────────────────────────────────────────────────────────────── */

/** 一张空白档案（新增时的初值）。数值取 `ProviderConfig` 的缺省。 */
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
    // 新档案明确不带回退；已有档案的草稿是整份展开，原值不会被这里碰到。
    fallbackModel: null,
  };
}

/* ── 可内联的模型管理主体 ───────────────────────────────────────────────── */

export function ModelManager({
  onRequestClose,
  onDirtyChange,
  listWidthClassName = "w-[30%] min-w-[150px] max-w-[210px] shrink-0",
}: {
  /** 仅供弹窗用：无草稿时显示「关闭」按钮，点它走父级的关闭流程（父级负责 guard）。 */
  onRequestClose?: () => void;
  /**
   * 把「有没有未保存改动」透给父级 —— 弹窗的遮罩 / 右上角 X 在 `ModelPanel` 里，
   * 它自己拿不到 dirty，只能靠这个回调决定关之前要不要拦一下。
   */
  onDirtyChange?: (dirty: boolean) => void;
  /**
   * 档案列表宽度。**不写死固定像素**：设置页卡片（≤720px）与侧栏弹窗都要用，
   * 固定 188px 在窄容器里会把右边的表单挤变形，所以默认给相对宽度。
   */
  listWidthClassName?: string;
}) {
  const pushToast = useUi((s) => s.pushToast);

  /** 后端档案总览（列表 + 激活项）。 */
  const [view, setView] = useState<ModelProfilesView | null>(null);
  /** 正在编辑的草稿（null = 还没选/还没新增）。 */
  const [draft, setDraft] = useState<ModelProfile | null>(null);
  const [busy, setBusy] = useState(false);
  const [showKey, setShowKey] = useState(false);
  /** 「刷新模型列表」拉回的候选模型名 + 是否展开。 */
  const [models, setModels] = useState<string[]>([]);
  const [modelsOpen, setModelsOpen] = useState(false);
  const [presets, setPresets] = useState<string[]>([]);

  const isNew = !!draft && draft.id === "";
  const savedProfile = view?.profiles.find((p) => p.id === draft?.id) ?? null;
  const dirty = !!draft && (!savedProfile || !sameModelProfile(savedProfile, draft));
  const isActive = !!draft && !!draft.id && view?.activeId === draft.id;

  // 只把「脏没脏」透出去，不把草稿本身交出去 —— 父级不需要、也不该改草稿。
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  /** 重新拉列表；keepId 指定重载后选中哪一条（缺省沿用当前草稿/激活项）。 */
  const reload = async (keepId?: string) => {
    try {
      const v = await modelApi.overview();
      setView(v);
      setDraft((prev) => {
        const target = selectModelProfileId(v, keepId ?? prev?.id);
        const found = v.profiles.find((p) => p.id === target);
        return found ? { ...found } : null;
      });
    } catch (e) {
      pushToast("error", `读取模型档案失败：${describeError(e)}`);
    }
  };

  useEffect(() => {
    void reload();
    void modelApi
      .presets()
      .then(setPresets)
      .catch(() => undefined);
    // 只在挂载时拉一次；后续由具体操作触发 reload
  }, []);

  const patch = (p: Partial<ModelProfile>) => setDraft((prev) => (prev ? { ...prev, ...p } : prev));

  /** 有未保存改动时先问一句，避免静默丢弃。 */
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
          // 预设只填连接参数，不覆盖已有的展示名（除非还空着）与 API Key
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
        {/* 左：档案列表 */}
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
            {view && view.profiles.length > 0 ? (
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

        {/* 右：编辑表单 */}
        <div className="min-w-0 flex-1">
          {draft ? (
            <div className="flex flex-col gap-2.5">
              <Field label="展示名">
                <input
                  className="nx-input"
                  placeholder="例如 公司 DeepSeek"
                  value={draft.name}
                  onChange={(e) => patch({ name: e.target.value })}
                />
              </Field>

              <Field label="Base URL">
                <input
                  className="nx-input font-mono"
                  placeholder="https://api.deepseek.com/v1"
                  value={draft.baseUrl}
                  onChange={(e) => patch({ baseUrl: e.target.value })}
                />
              </Field>

              <Field label="API Key">
                <div className="nx-field">
                  <span className="nx-field-icon">
                    <IconKey size={12} />
                  </span>
                  <input
                    className="nx-input pr-8 font-mono"
                    type={showKey ? "text" : "password"}
                    placeholder="sk-..."
                    value={draft.apiKey}
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

              <Field label="模型名">
                <div className="relative flex gap-1">
                  <input
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

              {/* 回退模型：草稿整份携带原值，不碰它 ⇒ 保存任何其它字段都原样保留；
                  只有在这个框里清空才是「明确清除」。两种动作都不藏。 */}
              <Field label="回退模型（可选）">
                <input
                  className="nx-input font-mono"
                  placeholder="主模型失败时改用的模型名"
                  aria-label="回退模型"
                  value={fallbackModelLabel(draft)}
                  onChange={(e) => patch({ fallbackModel: fallbackModelFromInput(e.target.value) })}
                />
                <div className="nx-hint mt-1 text-[10.5px]">
                  不改这里就保留原设置；清空后保存 = 明确移除回退。
                </div>
              </Field>

              <div className="flex gap-3">
                <div className="flex-1">
                  <Field label="温度 (0–2)">
                    <input
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
                  <Field label="上下文窗口 (tokens)">
                    <input
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

              <Field label="代理（留空则跟随系统代理）">
                <input
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

      {/* 底部操作条：刻意用普通边框而不是 nx-modal-footer —— 内联进设置页卡片时没有弹窗 chrome */}
      <div className="flex items-center gap-2 border-t border-neutral-800/60 pt-3">
        {draft ? (
          <>
            <span className="nx-hint mr-auto self-center text-[10.5px]">
              {isNew ? "新档案（尚未保存）" : isActive ? "当前激活" : `${draft.name}`}
              {dirty && <span className="ml-1 text-amber-400">· 有未保存的修改</span>}
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

/* ── 弹窗外壳 ───────────────────────────────────────────────────────────── */

/**
 * 对外签名保持不变（AI 侧栏在用）：只包一层 overlay，内容交给 `ModelManager`。
 * 遮罩与右上角 X 的关闭必须先过「未保存」这道问询 —— 它们在这里，
 * 拿不到子组件的 dirty，所以用 `onDirtyChange` 把状态同步上来。
 */
export function ModelPanel({ onClose }: { onClose: () => void }) {
  const [dirty, setDirty] = useState(false);

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
      <div className="nx-modal max-w-[760px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <IconKey size={14} className="text-neutral-400" />
          <span className="text-[13px] font-semibold text-neutral-100">模型配置</span>
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

/** 表单一行：标签 + 控件（统一间距，免得每处各写一遍）。 */
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <label className="nx-label">{label}</label>
      {children}
    </div>
  );
}
