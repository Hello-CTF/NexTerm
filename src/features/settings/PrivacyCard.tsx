import { useCallback, useEffect, useRef, useState } from "react";
import { privacyApi, type PrivacyConfig, type PrivacyPrecision, type PrivacyStatus } from "../../ipc/privacy";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { useAuth } from "../auth/store";

const precisionNames: Record<PrivacyPrecision, string> = {
  int8: "INT8 · 推荐，体积小",
  fp32: "FP32 · 精度优先",
  fp16: "FP16 · 半精度",
};
const mib = (bytes: number) => `${(bytes / 1024 / 1024).toFixed(1)} MiB`;

export function PrivacyCard() {
  const owner = useAuth((state) => state.user?.id) ?? "local";
  return <PrivacyCardBody key={owner} />;
}

function PrivacyCardBody() {
  const [status, setStatus] = useState<PrivacyStatus | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [precision, setPrecision] = useState<PrivacyPrecision>("int8");
  const mounted = useRef(true);
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

  const run = async (action: () => Promise<void>) => {
    requestGeneration.current++;
    setBusy(true); setError("");
    try { await action(); } catch (cause) {
      if (mounted.current) setError(describeError(cause));
    } finally { if (mounted.current) setBusy(false); }
  };
  const configure = async (config: PrivacyConfig) => {
    const next = await privacyApi.configure(config);
    if (mounted.current) setStatus(next);
  };
  const selected = status?.config.revision ? `${status.config.revision}:${status.config.precision}` : "";
  const progress = status?.download.progress;

  return (
    <section id="settings-ai-privacy" className="nx-card scroll-mt-12">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <span className="nx-card-title">AI 信息脱敏</span>
        <span className="nx-badge">AstrLink Guard · 本地检测</span>
      </div>
      <p className="nx-hint mb-3">
        默认关闭。开启后，在{WEB ? "当前 NexTerm 服务端" : "当前设备"}检测发送给 AI 的文本，将敏感片段替换为占位符，并在回复和工具执行前本地还原。同一会话跨轮保持稳定占位符。
      </p>
      {error && <p role="alert" className="mb-3 text-xs text-red-400">{error}</p>}
      {!status ? (
        <button type="button" className="nx-btn nx-btn-outline" onClick={() => void refresh()}>加载隐私设置</button>
      ) : (
        <div className="flex flex-col gap-3">
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={status.config.enabled} disabled={busy || (!selected && !status.config.enabled)}
              onChange={(event) => void run(() => configure({ ...status.config, enabled: event.target.checked }))} />
            启用 AI 信息脱敏
          </label>
          <label className="flex flex-col gap-1 text-xs text-neutral-400">
            使用的本地模型
            <select className="nx-input" aria-label="使用的本地隐私模型" value={selected} disabled={busy}
              onChange={(event) => {
                const installation = status.installed.find((item) => `${item.revision}:${item.precision}` === event.target.value);
                if (installation) void run(() => configure({ ...status.config, ...installation }));
              }}>
              <option value="" disabled>请先下载并选择模型</option>
              {status.installed.map((item) => (
                <option key={`${item.revision}:${item.precision}`} value={`${item.revision}:${item.precision}`}>
                  {item.version} · {item.precision.toUpperCase()} · {mib(Object.values(item.files).reduce((sum, file) => sum + file.size, 0))}
                </option>
              ))}
            </select>
          </label>
          <div className="flex flex-wrap items-center gap-2">
            <select className="nx-input w-auto" aria-label="下载模型精度" value={precision} disabled={busy || status.download.running}
              onChange={(event) => setPrecision(event.target.value as PrivacyPrecision)}>
              {Object.entries(precisionNames).map(([value, name]) => <option key={value} value={value}>{name}</option>)}
            </select>
            <button type="button" className="nx-btn nx-btn-outline" disabled={busy || status.download.running}
              onClick={() => void run(async () => { const next = await privacyApi.download(precision); if (mounted.current) setStatus(next); })}>
              下载最新模型
            </button>
            <button type="button" className="nx-btn nx-btn-outline" disabled={busy}
              onClick={() => void run(async () => { await privacyApi.checkUpdates(); await refresh(); })}>
              检查模型更新
            </button>
          </div>
          {status.latest && <p className="nx-hint">最新正式版：{status.latest.version}。下载不会自动启用脱敏或切换当前模型。</p>}
          {status.download.running && (
            <div className="flex flex-col gap-1" role="status">
              <div className="flex items-center justify-between gap-2 text-xs">
                <span>{progress && progress.total > 0 ? `${mib(progress.downloaded)} / ${mib(progress.total)}` : "正在获取模型版本与文件清单…"}</span>
                <button type="button" className="nx-btn nx-btn-outline" disabled={busy}
                  onClick={() => void run(async () => { await privacyApi.cancelDownload(); await refresh(); })}>取消下载</button>
              </div>
              <progress className="w-full" aria-label="隐私模型下载进度" max={progress?.total || 1} value={progress?.downloaded || 0} />
            </div>
          )}
          {status.download.error && <p role="alert" className="text-xs text-amber-400">{status.download.error}</p>}
          <p className="nx-hint">
            模型不包含在安装包中，下载来源为 Hugging Face / QuantumNous/astrlink-guard。INT8 适合日常使用；FP32 体积更大，代码和凭证检测精度略高；FP16 体积介于两者之间，速度取决于设备运行时。
          </p>
          <p className="nx-hint">
            模型可能漏检或误报。当前只支持文本脱敏，启用后包含图片等媒体的请求会被阻止；检测失败时也会阻止发送并显示原因。原有日志和记忆的安全脱敏继续生效。
          </p>
        </div>
      )}
    </section>
  );
}
