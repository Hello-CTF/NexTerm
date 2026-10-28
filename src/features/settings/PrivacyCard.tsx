import { useCallback, useEffect, useRef, useState } from "react";
import { privacyApi, privacyBroken, type PrivacyConfig, type PrivacyInstallation, type PrivacyPrecision, type PrivacyStatus } from "../../ipc/privacy";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { confirmDialog } from "../../ui/dialogs";
import { IconShield } from "../../ui/icons";
import { useAuth } from "../auth/store";

const precisionNames: Record<PrivacyPrecision, string> = {
  int8: "INT8 · 推荐，体积小",
  fp32: "FP32 · 精度优先",
  fp16: "FP16 · 半精度",
};
const mib = (bytes: number) => `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
const approxMib = (bytes: number) => Math.max(1, Math.round(bytes / 1024 / 1024));
const installationSize = (item: PrivacyInstallation) => Object.values(item.files).reduce((sum, file) => sum + file.size, 0);
const keyOf = (item: { revision: string; precision: PrivacyPrecision }) => `${item.revision}:${item.precision}`;

export function PrivacyCard() {
  const owner = useAuth((state) => state.user?.id) ?? "local";
  return <PrivacyCardBody key={owner} />;
}

function PrivacyCardBody() {
  const [status, setStatus] = useState<PrivacyStatus | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [enabling, setEnabling] = useState(false);
  const [checked, setChecked] = useState(false);
  const [checkError, setCheckError] = useState("");
  const [precision, setPrecision] = useState<PrivacyPrecision>("int8");
  const mounted = useRef(true);
  const autoChecked = useRef(false);
  const requestGeneration = useRef(0);
  const refresh = useCallback(async () => {
    const generation = ++requestGeneration.current;
    try {
      const value = await privacyApi.status();
      if (mounted.current && generation === requestGeneration.current) { setStatus(value); setError(""); }
    } catch (cause) {
      if (mounted.current && generation === requestGeneration.current) setError(describeError(cause));
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => { mounted.current = false; };
  }, [refresh]);
  useEffect(() => {
    if (!status?.download.running || busy) return;
    const timer = window.setInterval(() => { void refresh(); }, 1000);
    return () => window.clearInterval(timer);
  }, [refresh, status?.download.running, busy]);
  // Sizes come from the release manifests, so fetch them once before the first download.
  useEffect(() => {
    if (!status || autoChecked.current || status.latest || !status.canManage || !status.runtime.supported) return;
    autoChecked.current = true;
    void privacyApi.checkUpdates()
      .then(() => refresh())
      .catch((cause) => { if (mounted.current) setCheckError(describeError(cause)); });
  }, [status, refresh]);

  const run = async (action: () => Promise<void>) => {
    requestGeneration.current++;
    setBusy(true); setError(""); setCheckError("");
    try { await action(); } catch (cause) {
      if (mounted.current) setError(describeError(cause));
    } finally { if (mounted.current) { setBusy(false); setEnabling(false); } }
  };
  const configure = async (config: PrivacyConfig) => {
    if (config.enabled) setEnabling(true);
    const next = await privacyApi.configure(config);
    if (mounted.current) setStatus(next);
  };
  const remove = async (item: PrivacyInstallation) => {
    if (!(await confirmDialog(`删除模型 ${item.version} ${item.precision.toUpperCase()}（${mib(installationSize(item))}）？`))) return;
    await run(async () => { const next = await privacyApi.remove(item.revision, item.precision); if (mounted.current) setStatus(next); });
  };

  const config = status?.config;
  const selected = config?.revision ? keyOf(config) : "";
  const progress = status?.download.progress;
  const supported = status?.runtime.supported ?? true;
  const broken = status ? privacyBroken(status) : false;
  const missingSelection = !!status && !!selected && !status.installed.some((item) => keyOf(item) === selected);
  const selectedSize = status?.latestSizes?.[precision];
  const completed = status?.download.completed;
  const hasLatest = !!status?.latest && status.installed.some((item) => item.revision === status.latest?.revision);

  return (
    <section id="settings-ai-privacy" className="nx-card scroll-mt-12">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">AI 信息脱敏</span>
        <span className="nx-badge">AstrLink Guard · 本地检测</span>
      </div>
      <p className="nx-hint mb-1.5">
        默认关闭。开启后，在{WEB ? "当前 NexTerm 服务端" : "当前设备"}检测发送给 AI 的文本，将敏感片段替换为占位符；AI 回复和工具参数在本地还原后再显示或执行，同一会话跨轮保持稳定占位符。
      </p>
      <p className="nx-hint mb-3">
        对{WEB ? "当前账号" : "这台设备"}的所有 AI 请求生效：对话、定时任务、上下文压缩、子代理和工具输出。设置和模型不参与同步，每台设备需单独下载。模型不可用时请求会被阻止，不会以明文发送。目前仅支持文本。
      </p>
      {error && <div role="alert" className="nx-alert nx-alert-danger mb-3 text-[12px]">{error}</div>}
      {!status || !config ? (
        <button type="button" className="nx-btn nx-btn-outline" onClick={() => void refresh()}>加载隐私设置</button>
      ) : (
        <div className="flex flex-col gap-3">
          {broken ? (
            <div role="alert" className="nx-alert nx-alert-danger text-[12px]">
              已启用脱敏，但所选模型不可用：{supported ? status.selection?.error : status.runtime.reason}。在恢复或关闭前，所有 AI 请求都会被阻止。
            </div>
          ) : !supported && (
            <div role="alert" className="nx-alert nx-alert-danger text-[12px]">此设备无法运行本地脱敏：{status.runtime.reason}。</div>
          )}
          <div className="flex flex-col gap-1">
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" className="h-4 w-4" checked={config.enabled}
                disabled={busy || (!config.enabled && (!selected || missingSelection || !supported))}
                onChange={(event) => void run(() => configure({ ...config, enabled: event.target.checked }))} />
              启用 AI 信息脱敏
            </label>
            {enabling ? (
              <p role="status" className="nx-hint text-[11.5px]">正在校验并加载本地模型…</p>
            ) : !config.enabled && !selected && <p className="nx-hint text-[11.5px]">需先下载并选择模型</p>}
          </div>
          <label className="flex flex-col gap-1 text-xs text-neutral-400">
            使用的本地模型
            <select className="nx-input" aria-label="使用的本地隐私模型" value={selected} disabled={busy}
              onChange={(event) => {
                const installation = status.installed.find((item) => keyOf(item) === event.target.value);
                if (installation) void run(() => configure({ ...config, ...installation }));
              }}>
              <option value="" disabled>请先下载并选择模型</option>
              {missingSelection && (
                <option value={selected} disabled>{config.version || config.revision.slice(0, 7)} · {config.precision.toUpperCase()}（文件缺失）</option>
              )}
              {status.installed.map((item) => (
                <option key={keyOf(item)} value={keyOf(item)}>
                  {item.version} · {item.precision.toUpperCase()} · {mib(installationSize(item))}
                </option>
              ))}
            </select>
          </label>
          {status.canManage ? (
            <div className="flex flex-wrap items-center gap-2">
              <select className="nx-input w-auto" aria-label="下载模型精度" value={precision} disabled={busy || status.download.running || !supported}
                onChange={(event) => setPrecision(event.target.value as PrivacyPrecision)}>
                {(Object.keys(precisionNames) as PrivacyPrecision[]).map((value) => {
                  const size = status.latestSizes?.[value];
                  return <option key={value} value={value}>{precisionNames[value]}{size ? ` · 约 ${approxMib(size)} MiB` : ""}</option>;
                })}
              </select>
              <button type="button" className="nx-btn nx-btn-outline" disabled={busy || status.download.running || !supported}
                onClick={() => void run(async () => { const next = await privacyApi.download(precision); if (mounted.current) setStatus(next); })}>
                下载 {precision.toUpperCase()}{selectedSize ? `（约 ${approxMib(selectedSize)} MiB）` : ""}
              </button>
              <button type="button" className="nx-btn nx-btn-outline" disabled={busy || !supported}
                onClick={() => void run(async () => { await privacyApi.checkUpdates(); await refresh(); if (mounted.current) setChecked(true); })}>
                检查模型更新
              </button>
            </div>
          ) : (
            <p className="nx-hint text-[11.5px]">模型由管理员在服务端下载和管理。</p>
          )}
          {checkError && <p className="nx-hint text-[11.5px]">暂时无法获取最新模型信息：{checkError}</p>}
          {status.latest && (
            <p className="nx-hint text-[11.5px]">
              {status.installed.length === 0
                ? `最新正式版：${status.latest.version}。下载不会自动启用脱敏。`
                : hasLatest
                  ? checked ? "当前已是最新" : `最新正式版：${status.latest.version}，已下载。`
                  : `有新版本 ${status.latest.version}，下载后可切换`}
            </p>
          )}
          {status.download.running && (
            <div className="flex flex-col gap-1" role="status">
              <div className="flex items-center justify-between gap-2 text-xs">
                <span>{progress && progress.total > 0 ? `${mib(progress.downloaded)} / ${mib(progress.total)}` : "正在获取模型版本与文件清单…"}</span>
                <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" disabled={busy}
                  onClick={() => void run(async () => { await privacyApi.cancelDownload(); await refresh(); })}>取消下载</button>
              </div>
              <progress className="w-full" aria-label="隐私模型下载进度" max={progress?.total || 1} value={progress?.downloaded || 0} />
            </div>
          )}
          {!status.download.running && completed && (
            <div role="status" className="nx-alert nx-alert-success flex flex-wrap items-center gap-2 text-[12px]">
              <span>下载完成：{completed.version} {status.download.precision.toUpperCase()}。</span>
              {selected !== keyOf({ revision: completed.revision, precision: status.download.precision }) && (
                <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" disabled={busy}
                  onClick={() => void run(() => configure({ ...config, ...completed, precision: status.download.precision }))}>使用此模型</button>
              )}
            </div>
          )}
          {!status.download.running && status.download.canceled && (
            <p role="status" className="nx-hint text-[11.5px]">{status.download.error}</p>
          )}
          {!status.download.running && status.download.error && !status.download.canceled && (
            <div role="alert" className="nx-alert nx-alert-danger text-[12px]">
              <div>{status.download.error}</div>
              {status.download.detail && <div className="mt-0.5 break-words font-mono text-[11px] opacity-70">{status.download.detail}</div>}
            </div>
          )}
          {status.installed.length > 0 && (
            <div className="flex flex-col gap-1">
              <div className="text-xs text-neutral-400">已下载的模型 · 共占用 {mib(status.diskUsage)}</div>
              {status.installed.map((item) => (
                <div key={keyOf(item)} className="flex items-center justify-between gap-2 text-[11.5px]">
                  <span>{item.version} · {item.precision.toUpperCase()} · {mib(installationSize(item))}{keyOf(item) === selected ? " · 当前使用" : ""}</span>
                  {status.canManage && (
                    <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy || status.download.running || (config.enabled && keyOf(item) === selected)}
                      title={config.enabled && keyOf(item) === selected ? "正在使用，请先关闭脱敏或切换到其他模型" : undefined}
                      onClick={() => void remove(item)}>删除</button>
                  )}
                </div>
              ))}
            </div>
          )}
          <p className="nx-hint">
            模型不包含在安装包中，下载来源为 Hugging Face / QuantumNous/astrlink-guard。INT8 适合日常使用；FP32 体积更大，代码和凭证检测精度略高；FP16 体积介于两者之间，速度取决于设备运行时。模型可能漏检或误报；原有日志和记忆的安全脱敏继续生效。
          </p>
        </div>
      )}
    </section>
  );
}
