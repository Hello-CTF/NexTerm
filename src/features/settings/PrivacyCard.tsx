import { useCallback, useEffect, useRef, useState } from "react";
import { privacyApi, privacyBroken, type PrivacyConfig, type PrivacyInstallation, type PrivacyPrecision, type PrivacyStatus } from "../../ipc/privacy";
import { WEB } from "../../ipc/env";
import { useUi } from "../../app/store";
import { DownloadProgress } from "../../ui/DownloadProgress";
import { Select } from "../../ui/Select";
import { confirmDialog } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { formatBytes } from "../../ui/format";
import { IconRefresh, IconShield } from "../../ui/icons";
import { useAuth } from "../auth/store";
import { SummaryDialog } from "./SummaryDialog";
import { modelDisplayName } from "./privacyModel";

const precisionNames: Record<PrivacyPrecision, string> = {
  int8: "INT8",
  fp16: "FP16",
  fp32: "FP32",
};
const precisionDescriptions: Record<PrivacyPrecision, string> = {
  int8: "推荐",
  fp16: "均衡",
  fp32: "精度优先",
};
const installationSize = (item: PrivacyInstallation) => Object.values(item.files).reduce((sum, file) => sum + file.size, 0);
const keyOf = (item: { revision: string; precision: PrivacyPrecision }) => `${item.revision}:${item.precision}`;

export function PrivacyCard() {
  const owner = useAuth((state) => state.user?.id) ?? "local";
  return <PrivacyCardBody key={owner} />;
}

function PrivacyCardBody() {
  const pushToast = useUi((state) => state.pushToast);
  const [status, setStatus] = useState<PrivacyStatus | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [enabling, setEnabling] = useState(false);
  const [checkError, setCheckError] = useState("");
  const [precision, setPrecision] = useState<PrivacyPrecision>("int8");
  const mounted = useRef(true);
  const autoChecked = useRef(false);
  // undefined until the first status arrives: a completion already recorded then is stale, not news.
  const notifiedCompletion = useRef<string | undefined>(undefined);
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
    if (!(await confirmDialog(`确定要删除模型 ${modelDisplayName(item)} ${item.version} ${item.precision.toUpperCase()}（${formatBytes(installationSize(item))}）吗？`))) return;
    await run(async () => { const next = await privacyApi.remove(item.revision, item.precision); if (mounted.current) setStatus(next); });
  };

  const config = status?.config;
  const selected = config?.revision ? keyOf(config) : "";
  const progress = status?.download.progress;
  const supported = status?.runtime.supported ?? true;
  const broken = status ? privacyBroken(status) : false;
  const missingSelection = !!status && !!selected && !status.installed.some((item) => keyOf(item) === selected);
  const currentInstallation = status?.installed.find((item) => keyOf(item) === selected);
  const completed = status?.download.completed;
  const completedKey = completed ? keyOf({ revision: completed.revision, precision: status?.download.precision ?? "int8" }) : "";
  const noModelHint = status && !status.canManage && status.installed.length === 0 ? "请联系管理员下载模型" : "尚未下载模型";
  const hasLatest = !!status?.latest && status.installed.some((item) => item.revision === status.latest?.revision);

  useEffect(() => {
    if (!status) return;
    if (notifiedCompletion.current === undefined) {
      notifiedCompletion.current = completedKey;
      return;
    }
    if (!completed || !completedKey || notifiedCompletion.current === completedKey) return;
    notifiedCompletion.current = completedKey;
    pushToast("success", `模型 ${modelDisplayName(completed)} ${completed.version} ${status.download.precision.toUpperCase()} 下载完成`);
  }, [completed, completedKey, pushToast, status]);

  return (
    <section id="settings-ai-privacy" className="nx-card scroll-mt-12">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">AI 信息脱敏</span>
        <span className="nx-badge">本地处理</span>
      </div>
      <p className="nx-hint mb-1">
        发送给 AI 前，在{WEB ? "当前 NexTerm 服务端" : "本机"}将敏感文本替换为占位符。
      </p>
      <p className="nx-hint mb-3">仅处理文本；图片不会脱敏，将原样发送。</p>
      {error && <div role="alert" className="nx-alert nx-alert-danger mb-3 text-[12px]">{error}</div>}
      {!status || !config ? (
        <button type="button" className="nx-btn nx-btn-outline" onClick={() => void refresh()}>加载隐私设置</button>
      ) : (
        <div className="flex flex-col gap-3">
          {broken ? (
            <div role="alert" className="nx-alert nx-alert-danger text-[12px]">
              {supported
                ? `已启用脱敏，但所选模型不可用：${status.selection?.error ?? "未知原因"}。`
                : `${WEB ? "服务端" : "本机"}的脱敏运行时不可用：${status.runtime.reason ?? "未知原因"}。`}
              在恢复或关闭脱敏前，所有 AI 请求都将被阻止。{supported && !status.canManage ? "请联系管理员下载模型。" : ""}
            </div>
          ) : !supported && (
            <div role="alert" className="nx-alert nx-alert-danger text-[12px]">本地脱敏当前不可用：{status.runtime.reason}。</div>
          )}
          <label className="flex cursor-pointer items-center gap-3">
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] text-neutral-200">启用脱敏</div>
              <p className="nx-hint mt-0.5">
                {enabling ? "正在校验并加载本地模型…" : "模型不可用时阻止 AI 请求，不会以明文发送。"}
              </p>
            </div>
            <input type="checkbox" className="h-4 w-4 shrink-0" checked={config.enabled}
              disabled={busy || (!config.enabled && (!selected || missingSelection || !supported))}
              onChange={(event) => void run(() => configure({ ...config, enabled: event.target.checked }))} />
          </label>
          <div className="border-t border-neutral-800/60 pt-3">
            <div className="text-[12.5px] text-neutral-200">本地模型</div>
            <SummaryDialog
              manageable={status.canManage || status.installed.length > 0}
              buttonLabel={status.installed.length === 0 ? "下载模型" : "管理"}
              title="管理本地脱敏模型"
              description="选择、下载和删除本地隐私模型。"
              summary={
                <>
                  <span className="block truncate">
                    {currentInstallation
                      ? `${modelDisplayName(currentInstallation)} · ${currentInstallation.precision.toUpperCase()} · ${currentInstallation.version} · ${formatBytes(installationSize(currentInstallation))}`
                      : noModelHint}
                    {status.installed.length > 0 ? ` · 已下载 ${status.installed.length} 个` : ""}
                  </span>
                  {status.latest && status.installed.length > 0 && !hasLatest && <span className="nx-badge nx-badge-green mt-1">可更新至 {status.latest.version}</span>}
                </>
              }
            >
              <div className="flex flex-col gap-3">
                {status.installed.length > 0 || missingSelection ? (
                  <label className="flex flex-col gap-1 text-xs text-neutral-400">
                    当前模型
                    <Select ariaLabel="使用的本地隐私模型" value={selected} disabled={busy}
                      options={[
                        ...(missingSelection ? [{ value: selected, label: `${modelDisplayName(config)} · ${config.precision.toUpperCase()} · ${config.version || config.revision.slice(0, 7)}（文件缺失）`, disabled: true }] : []),
                        ...status.installed.map((item) => ({
                          value: keyOf(item),
                          label: `${modelDisplayName(item)} · ${item.precision.toUpperCase()} · ${item.version}`,
                          description: formatBytes(installationSize(item)),
                        })),
                      ]}
                      onChange={(value) => {
                        const installation = status.installed.find((item) => keyOf(item) === value);
                        if (installation) void run(() => configure({ ...config, ...installation }));
                      }} />
                  </label>
                ) : null}
                {status.canManage ? (
                  <div className="flex flex-col gap-1.5">
                    <div className="text-xs text-neutral-400">下载模型</div>
                    <div className="flex flex-wrap items-center gap-2">
                      <Select className="min-w-48 flex-1 sm:flex-none" ariaLabel="下载模型精度" value={precision}
                        disabled={busy || status.download.running || !supported}
                        options={(Object.keys(precisionNames) as PrivacyPrecision[]).map((value) => ({
                          value,
                          label: precisionNames[value],
                          description: `${precisionDescriptions[value]}${status.latestSizes?.[value] ? ` · 约 ${formatBytes(status.latestSizes[value])}` : ""}`,
                        }))}
                        onChange={(value) => setPrecision(value as PrivacyPrecision)} />
                      <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" disabled={busy || status.download.running || !supported}
                        onClick={() => void run(async () => { const next = await privacyApi.download(precision); if (mounted.current) setStatus(next); })}>
                        下载
                      </button>
                      <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={busy || !supported}
                        onClick={() => void run(async () => { await privacyApi.checkUpdates(); await refresh(); })}>
                        <IconRefresh size={12} />检查更新
                      </button>
                    </div>
                    <p className="nx-hint text-[11px]">下载后不会自动启用脱敏。</p>
                  </div>
                ) : (
                  <p className="nx-hint text-[11.5px]">模型由管理员在服务端下载和管理。</p>
                )}
                {status.installed.length > 0 && (
                  <div className="flex flex-col gap-1 border-t border-neutral-800/60 pt-2">
                    <div className="text-xs text-neutral-400">已下载 · 共占用 {formatBytes(status.diskUsage)}</div>
                    {status.installed.map((item) => (
                      <div key={keyOf(item)} className="flex items-center justify-between gap-2 text-[11.5px]">
                        <span>{modelDisplayName(item)} · {item.precision.toUpperCase()} · {item.version} · {formatBytes(installationSize(item))}{keyOf(item) === selected ? " · 当前" : ""}</span>
                        {status.canManage && (
                          <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy || status.download.running || (config.enabled && keyOf(item) === selected)}
                            title={config.enabled && keyOf(item) === selected ? "正在使用，请先关闭脱敏或切换到其他模型" : undefined}
                            onClick={() => void remove(item)}>删除</button>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </SummaryDialog>
          </div>
          {checkError && <p className="nx-hint text-[11.5px]">暂时无法获取模型更新：{checkError}</p>}
          {status.newerIncompatible && (
            <p className="nx-hint text-[11.5px]">新版本 {status.newerIncompatible.version} 需要先更新 NexTerm。</p>
          )}
          {/* Download state stays on the main panel so progress, completion and failures are never hidden in the dialog. */}
          {status.download.running && (
            <DownloadProgress label={`正在下载 ${status.download.precision.toUpperCase()}`}
              transferred={progress?.downloaded ?? 0} total={progress?.total ?? 0}
              cancelDisabled={busy} onCancel={() => void run(async () => { await privacyApi.cancelDownload(); await refresh(); })} />
          )}
          {!status.download.running && completed && selected !== completedKey && (
            <div role="status" className="flex flex-wrap items-center gap-2 text-[12px] text-green-300">
              <span>已下载 {modelDisplayName(completed)} {completed.version} {status.download.precision.toUpperCase()}</span>
              <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" disabled={busy}
                onClick={() => void run(() => configure({ ...config, ...completed, precision: status.download.precision }))}>使用此模型</button>
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
          <p className="nx-hint text-[11px]">模型从官方渠道下载，不随安装包分发。</p>
          <p className="nx-hint text-[11px]">脱敏可能漏检或误报，请勿将其作为唯一的保护措施。</p>
        </div>
      )}
    </section>
  );
}
